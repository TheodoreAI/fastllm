import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { contrastRatio, aaLevel } from '../wcagContrast'
import {
  DEFAULTS,
  STORAGE_KEY,
  FONT_STACKS,
  MOTION_KEYFRAMES,
  MOTION_ANIM_NAME,
  INSPECT_STYLE_KEYS,
} from '../tweakBarConstants'

// Dev tool, not a feature — a floating panel for live-overriding fastllm's
// design tokens (color, typography, spacing, motion) plus a click-to-copy
// element inspector, with live WCAG contrast readouts on every color pair
// so a color/background combo can be tweaked until it actually passes AA
// rather than eyeballing it. Rendered from App.jsx, always available but
// collapsed by default (fastllm has no dev/prod split to gate this on —
// it's a single-user local desktop app).
//
// fastllm's themes are all selected via :root[data-theme='x'] (see
// themes.js / App.css), which beats a bare :root rule on CSS specificity
// (0-1-1 vs 0-0-1) regardless of source order — the classic override trap.
// Every custom-property override below therefore carries !important.
//
// The panel-open toggle lives in App.jsx's own view-rail (a plain SVG
// button matching Files/Search/Git), not inside this component —
// `open`/`onToggle` are lifted up so App.jsx can own that button the
// same way it owns every other rail button's active state.
export default function TweakBar({ open, onToggle }) {
  const [state, setState] = useState(() => loadInitialState())
  const styleTagRef = useRef(null)
  const motionStyleTagRef = useRef(null)
  const panelRef = useRef(null)

  const [inspecting, setInspecting] = useState(false)
  const [flashMessage, setFlashMessage] = useState('')
  const hoverOutlineRef = useRef(null)
  const hoveredElRef = useRef(null)

  useEffect(() => {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(state))
    } catch {
      // Storage full/disabled — the tweak bar still works for this
      // session, it just won't persist across reloads.
    }
  }, [state])

  const patch = useCallback((partial) => {
    setState((prev) => ({ ...prev, ...partial }))
  }, [])

  // --- style injection ---------------------------------------------------

  useEffect(() => {
    if (!styleTagRef.current) {
      const tag = document.createElement('style')
      tag.id = 'tweak-bar-overrides'
      document.head.appendChild(tag)
      styleTagRef.current = tag
    }
    return () => {
      styleTagRef.current?.remove()
      styleTagRef.current = null
    }
  }, [])

  useEffect(() => {
    const tag = styleTagRef.current
    if (!tag) return
    const rules = []

    rules.push(':root {')
    rules.push(`  --font-ui: ${state.fontFamily} !important;`)
    rules.push(`  --font-scale: ${state.fontSize} !important;`)
    if (state.textColor) rules.push(`  --text: ${state.textColor} !important;`)
    if (state.textDimColor) rules.push(`  --text-dim: ${state.textDimColor} !important;`)
    if (state.bgColor) {
      rules.push(`  --bg-chat: ${state.bgColor} !important;`)
      rules.push(`  --bg-window: ${state.bgColor} !important;`)
    }
    if (state.bgPanelColor) rules.push(`  --bg-panel: ${state.bgPanelColor} !important;`)
    if (state.bubbleUserColor) rules.push(`  --bg-bubble-user: ${state.bubbleUserColor} !important;`)
    if (state.accentColor) rules.push(`  --accent: ${state.accentColor} !important;`)
    if (state.radius !== null && state.radius !== undefined) {
      rules.push(`  --radius: ${state.radius}px !important;`)
      rules.push(`  --radius-sm: ${Math.max(0, state.radius - 1)}px !important;`)
      rules.push(`  --radius-window: ${state.radius}px !important;`)
    }
    const s1 = 4 * state.spaceScale
    rules.push(`  --space-1: ${s1}px !important;`)
    rules.push(`  --space-2: ${s1 * 2}px !important;`)
    rules.push(`  --space-3: ${s1 * 3}px !important;`)
    rules.push(`  --space-4: ${s1 * 4}px !important;`)
    rules.push(`  --space-5: ${s1 * 6}px !important;`)
    rules.push('}')

    // Direct backstop: html's font-size reads --font-scale via calc() at
    // parse time (see App.css), which already re-resolves on var() change
    // in evergreen browsers, but pinning it directly here costs nothing
    // and guards against any engine that doesn't recompute the calc().
    rules.push(`html { font-size: calc(16px * ${state.fontSize}) !important; }`)
    rules.push(
      `body { letter-spacing: ${state.letterSpacing}px !important; font-weight: ${state.fontWeight} !important; }`,
    )
    if (state.fontWeight !== '400') {
      rules.push(`.message, .composer textarea, button { font-weight: ${state.fontWeight} !important; }`)
    }

    // .panel/.message/.composer are the real content containers (see
    // App.css) — scoping space overrides here instead of blanket-applying
    // to .app avoids breaking the fixed sidebar/titlebar shell layout.
    if (state.spaceScale !== 1) {
      rules.push('.panel, .message, .composer { padding: calc(var(--space-3) * 1px); }')
    }

    tag.textContent = rules.join('\n')
  }, [state])

  useEffect(() => {
    if (!motionStyleTagRef.current) {
      const tag = document.createElement('style')
      tag.id = 'tweak-bar-motion'
      document.head.appendChild(tag)
      motionStyleTagRef.current = tag
    }
    return () => {
      motionStyleTagRef.current?.remove()
      motionStyleTagRef.current = null
    }
  }, [])

  useEffect(() => {
    const tag = motionStyleTagRef.current
    if (!tag) return
    if (state.motionPreset === 'none') {
      tag.textContent = ''
      return
    }
    const duration = (1 / state.motionSpeed).toFixed(2)
    const animName = MOTION_ANIM_NAME[state.motionPreset]
    const iteration = state.motionPreset === 'pulse-accent' ? 'infinite' : '1'
    tag.textContent = `
      ${MOTION_KEYFRAMES[state.motionPreset]}
      @media (prefers-reduced-motion: no-preference) {
        .message, .panel > * { animation: ${animName} ${duration}s ease both ${iteration}; }
      }
    `
  }, [state.motionPreset, state.motionSpeed])

  // --- contrast readouts ---------------------------------------------------

  // Bumped whenever a theme token could have changed for a reason outside
  // this component's own state — the app's theme picker in Settings, or
  // (the bug this fixes) useTheme's effect setting data-theme on
  // <html> landing in a render after TweakBar's own first paint, which
  // otherwise left the very first contrast readout frozen on whatever
  // --text/--bg-chat/etc. resolved to before the real theme applied.
  const [themeTick, setThemeTick] = useState(0)
  useEffect(() => {
    // useContrastPairs's useMemo runs during TweakBar's very first render,
    // which happens before useTheme's effect (in App.jsx) has set
    // data-theme on <html> — so its first computation reads --text/
    // --bg-chat/etc. before the real theme's values exist, and silently
    // falls back to this hook's dark-theme defaults instead. Bumping the
    // tick once here (after mount, once the DOM has actually settled)
    // forces exactly one recompute against the real values. The
    // MutationObserver below then keeps it correct for later theme
    // changes (Settings), since by the time it attaches the initial
    // data-theme mutation has usually already happened and won't fire
    // again on its own.
    setThemeTick((t) => t + 1)

    const observer = new MutationObserver(() => setThemeTick((t) => t + 1))
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => observer.disconnect()
  }, [])

  const contrastPairs = useContrastPairs(state, themeTick)

  // --- element inspector ---------------------------------------------------

  useEffect(() => {
    if (!inspecting) return

    if (!hoverOutlineRef.current) {
      const el = document.createElement('div')
      el.id = 'tweak-bar-hover-outline'
      document.body.appendChild(el)
      hoverOutlineRef.current = el
    }
    const outline = hoverOutlineRef.current
    document.body.classList.add('picking-mode')

    function isExcluded(el) {
      return !!el.closest('#tweak-bar-panel, #view-rail-tweak-bar, #tweak-bar-hover-outline')
    }

    function onMove(e) {
      const el = e.target
      if (isExcluded(el)) {
        outline.style.display = 'none'
        hoveredElRef.current = null
        return
      }
      hoveredElRef.current = el
      outline.style.display = 'block'
      const r = el.getBoundingClientRect()
      outline.style.left = `${r.left}px`
      outline.style.top = `${r.top}px`
      outline.style.width = `${r.width}px`
      outline.style.height = `${r.height}px`
      outline.dataset.label = el.tagName.toLowerCase() + (el.id ? `#${el.id}` : '')
    }

    async function onClick(e) {
      if (!hoveredElRef.current || isExcluded(e.target)) return
      e.preventDefault()
      e.stopPropagation()

      const description = describeElement(hoveredElRef.current)
      const payload = JSON.stringify(description, null, 2)
      try {
        await navigator.clipboard.writeText(payload)
      } catch {
        const ta = document.createElement('textarea')
        ta.value = payload
        ta.style.position = 'fixed'
        ta.style.opacity = '0'
        document.body.appendChild(ta)
        ta.select()
        document.execCommand('copy')
        document.body.removeChild(ta)
      }
      setInspecting(false)
      if (!open) onToggle()
      setFlashMessage(`Copied ${description.selector}`)
    }

    function onKeydown(e) {
      if (e.key === 'Escape') setInspecting(false)
    }

    document.addEventListener('mousemove', onMove, true)
    document.addEventListener('click', onClick, true)
    document.addEventListener('keydown', onKeydown, true)
    return () => {
      document.body.classList.remove('picking-mode')
      outline.style.display = 'none'
      document.removeEventListener('mousemove', onMove, true)
      document.removeEventListener('click', onClick, true)
      document.removeEventListener('keydown', onKeydown, true)
    }
  }, [inspecting, open, onToggle])

  useEffect(() => {
    return () => hoverOutlineRef.current?.remove()
  }, [])

  useEffect(() => {
    if (!flashMessage) return
    const t = setTimeout(() => setFlashMessage(''), 2200)
    return () => clearTimeout(t)
  }, [flashMessage])

  function resetAll() {
    setState({ ...DEFAULTS })
  }

  if (!open) return null

  return (
    <div id="tweak-bar-panel" ref={panelRef}>
      <div className="tb-header">
        <span>Tweak bar</span>
        <button type="button" onClick={resetAll}>
          Reset
        </button>
      </div>

      <div className="tb-row tb-row-full">
        <button type="button" onClick={() => setInspecting((v) => !v)}>
          {inspecting ? 'Cancel (Esc)' : 'Select element'}
        </button>
      </div>

      <div className="tb-row tb-row-full">
        <label>
          Font family
          <select value={state.fontFamily} onChange={(e) => patch({ fontFamily: e.target.value })}>
            {Object.entries(FONT_STACKS).map(([label, stack]) => (
              <option key={label} value={stack}>
                {label}
              </option>
            ))}
          </select>
        </label>
      </div>

      <div className="tb-group">
        <div className="tb-group-title">Typography</div>
        <label>
          Weight
          <select value={state.fontWeight} onChange={(e) => patch({ fontWeight: e.target.value })}>
            <option value="300">Light</option>
            <option value="400">Regular</option>
            <option value="500">Medium</option>
            <option value="600">Semibold</option>
            <option value="700">Bold</option>
          </select>
        </label>
        <label>
          Size <span>{Math.round(state.fontSize * 100)}%</span>
          <input
            type="range"
            min="0.75"
            max="1.5"
            step="0.05"
            value={state.fontSize}
            onChange={(e) => patch({ fontSize: Number(e.target.value) })}
          />
        </label>
        <label>
          Letter spacing <span>{state.letterSpacing}px</span>
          <input
            type="range"
            min="-1"
            max="3"
            step="0.1"
            value={state.letterSpacing}
            onChange={(e) => patch({ letterSpacing: Number(e.target.value) })}
          />
        </label>
      </div>

      <div className="tb-group">
        <div className="tb-group-title">
          Color
          <span className="tb-group-hint">Ratios update live — green = passes WCAG AA</span>
        </div>

        <ColorControl
          label="Text"
          value={state.textColor}
          tokenVar="--text"
          themeTick={themeTick}
          onChange={(v) => patch({ textColor: v })}
        />
        <ColorControl
          label="Muted text"
          value={state.textDimColor}
          tokenVar="--text-dim"
          themeTick={themeTick}
          onChange={(v) => patch({ textDimColor: v })}
        />
        <ColorControl
          label="Background"
          value={state.bgColor}
          tokenVar="--bg-chat"
          themeTick={themeTick}
          onChange={(v) => patch({ bgColor: v })}
        />
        <ColorControl
          label="Panel background"
          value={state.bgPanelColor}
          tokenVar="--bg-panel"
          themeTick={themeTick}
          onChange={(v) => patch({ bgPanelColor: v })}
        />
        <ColorControl
          label="Accent"
          value={state.accentColor}
          tokenVar="--accent"
          themeTick={themeTick}
          onChange={(v) => patch({ accentColor: v })}
        />
        <ColorControl
          label="User bubble bg"
          value={state.bubbleUserColor}
          tokenVar="--bg-bubble-user"
          themeTick={themeTick}
          onChange={(v) => patch({ bubbleUserColor: v })}
        />

        <ContrastReadouts pairs={contrastPairs} />
      </div>

      <div className="tb-group">
        <div className="tb-group-title">Space</div>
        <label>
          Density <span>{Math.round(state.spaceScale * 100)}%</span>
          <input
            type="range"
            min="0.5"
            max="2"
            step="0.1"
            value={state.spaceScale}
            onChange={(e) => patch({ spaceScale: Number(e.target.value) })}
          />
        </label>
        <label>
          Corner radius <span>{state.radius ?? currentRadius()}px</span>
          <input
            type="range"
            min="0"
            max="20"
            step="1"
            value={state.radius ?? currentRadius()}
            onChange={(e) => patch({ radius: Number(e.target.value) })}
          />
        </label>
      </div>

      <div className="tb-group">
        <div className="tb-group-title">Motion</div>
        <label>
          Preset
          <select value={state.motionPreset} onChange={(e) => patch({ motionPreset: e.target.value })}>
            <option value="none">None</option>
            <option value="fade-in">Fade in</option>
            <option value="rise-in">Rise in</option>
            <option value="pulse-accent">Pulse accent</option>
          </select>
        </label>
        <label>
          Speed <span>{state.motionSpeed}x</span>
          <input
            type="range"
            min="0.25"
            max="3"
            step="0.25"
            value={state.motionSpeed}
            onChange={(e) => patch({ motionSpeed: Number(e.target.value) })}
          />
        </label>
      </div>

      {flashMessage && <div className="tb-flash">{flashMessage}</div>}
    </div>
  )
}

// --- helpers ---------------------------------------------------------------

function loadInitialState() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw) return { ...DEFAULTS, ...JSON.parse(raw) }
  } catch {
    // Corrupt/old localStorage payload — fall back to defaults rather
    // than blocking the tweak bar from loading at all.
  }
  return { ...DEFAULTS }
}

function currentToken(name, fallback) {
  if (typeof document === 'undefined') return fallback
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return v || fallback
}

function currentRadius() {
  return parseInt(currentToken('--radius', '4'), 10) || 4
}

// Named CSS colors and rgb()/oklch() etc. won't round-trip through
// <input type="color"> (it only accepts #rrggbb) — resolve via a
// throwaway element so any computed-style value works as a starting
// point, not just hex.
function toHex(cssColor) {
  if (/^#[0-9a-f]{6}$/i.test(cssColor)) return cssColor
  const probe = document.createElement('div')
  probe.style.color = cssColor
  document.body.appendChild(probe)
  const rgb = getComputedStyle(probe).color
  document.body.removeChild(probe)
  const m = rgb.match(/\d+/g)
  if (!m) return '#000000'
  return (
    '#' +
    m
      .slice(0, 3)
      .map((n) => Number(n).toString(16).padStart(2, '0'))
      .join('')
  )
}

// One color picker row: swatch input, defaulting to the live value of the
// CSS variable it overrides (tokenVar) until the user actually picks a
// color, at which point `value` (from TweakBar's state) takes over.
// themeTick forces a re-resolve when the app's theme changes elsewhere
// (Settings, or useTheme's effect landing after this component's first
// paint) — see the comment above the MutationObserver in TweakBar for why
// this can't just depend on [value, tokenVar].
function ColorControl({ label, value, tokenVar, onChange, themeTick }) {
  // themeTick isn't read inside the memo body — it's an invalidation
  // signal only, forcing a re-resolve of the *live* currentToken() read
  // when nothing else in this deps array would otherwise change. Don't
  // remove it as "unused": that reintroduces the exact bug it fixes (see
  // the long comment on TweakBar's own themeTick effect).
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const resolved = useMemo(() => toHex(value || currentToken(tokenVar, '#000000')), [value, tokenVar, themeTick])
  return (
    <label>
      {label}
      <input type="color" value={resolved} onChange={(e) => onChange(e.target.value)} />
    </label>
  )
}

// The real fastllm surface/text pairings worth checking — mirrors the
// pairs manually audited across all themes in App.css's WCAG pass. Falls
// back to each surface's live CSS-variable value so pairs still render
// (and update as OTHER controls change) before every color has been
// hand-picked in this session.
function useContrastPairs(state, themeTick) {
  return useMemo(() => {
    const text = state.textColor || currentToken('--text', '#f2f2f2')
    const textDim = state.textDimColor || currentToken('--text-dim', '#a8a8ad')
    const bg = state.bgColor || currentToken('--bg-chat', '#1e1e1e')
    const bgPanel = state.bgPanelColor || currentToken('--bg-panel', '#2c2c2e')
    const accent = state.accentColor || currentToken('--accent', '#0a84ff')
    const bubbleUser = state.bubbleUserColor || currentToken('--bg-bubble-user', '#0875e2')

    const pairs = [
      { label: 'Text on background', fg: text, bg, large: false },
      { label: 'Muted text on background', fg: textDim, bg, large: false },
      { label: 'Muted text on panel', fg: textDim, bg: bgPanel, large: false },
      { label: 'White on accent', fg: '#ffffff', bg: accent, large: false },
      { label: 'White on user bubble', fg: '#ffffff', bg: bubbleUser, large: false },
      { label: 'Accent on background', fg: accent, bg, large: true },
    ]

    return pairs.map((p) => {
      const ratio = contrastRatio(p.fg, p.bg)
      return { ...p, ratio, level: aaLevel(ratio, { large: p.large }) }
    })
    // themeTick isn't read inside the memo body above — see the identical
    // note on ColorControl; it's an invalidation-only signal here too.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    state.textColor,
    state.textDimColor,
    state.bgColor,
    state.bgPanelColor,
    state.accentColor,
    state.bubbleUserColor,
    themeTick,
  ])
}

function ContrastReadouts({ pairs }) {
  return (
    <div className="tb-contrast-list">
      {pairs.map((p) => (
        <div key={p.label} className={`tb-contrast-row tb-contrast-${p.level}`}>
          <span className="tb-contrast-label">{p.label}</span>
          <span className="tb-contrast-ratio">
            {p.ratio.toFixed(2)}:1 <span className="tb-contrast-threshold">(need {p.large ? '3.0' : '4.5'})</span>
          </span>
        </div>
      ))}
    </div>
  )
}

function cssPath(el) {
  const parts = []
  let node = el
  let depth = 0
  while (node && node.nodeType === 1 && depth < 6) {
    if (node.id) {
      parts.unshift(`#${node.id}`)
      break
    }
    let selector = node.tagName.toLowerCase()
    const cls = (node.className && typeof node.className === 'string' ? node.className : '')
      .trim()
      .split(/\s+/)
      .filter(Boolean)
      .slice(0, 2)
    if (cls.length) selector += '.' + cls.join('.')
    const siblings = node.parentElement
      ? Array.from(node.parentElement.children).filter((c) => c.tagName === node.tagName)
      : []
    if (siblings.length > 1) {
      selector += `:nth-of-type(${siblings.indexOf(node) + 1})`
    }
    parts.unshift(selector)
    node = node.parentElement
    depth++
  }
  return parts.join(' > ')
}

function describeElement(el) {
  const cs = getComputedStyle(el)
  const styles = {}
  for (const key of INSPECT_STYLE_KEYS) styles[key] = cs[key]

  const attrs = {}
  for (const a of el.attributes) {
    if (a.name === 'style') continue
    attrs[a.name] = a.value
  }

  const text = (el.textContent || '').trim().slice(0, 200)
  let html = el.outerHTML
  if (html.length > 2000) {
    html = el.outerHTML.slice(0, el.outerHTML.indexOf('>') + 1)
  }

  return {
    selector: cssPath(el),
    tag: el.tagName.toLowerCase(),
    attributes: attrs,
    text,
    computedStyles: styles,
    outerHTML: html,
    contrastToBackground: (() => {
      const bg = getComputedStyle(document.body).backgroundColor
      try {
        return `${contrastRatio(cs.color, bg).toFixed(2)}:1`
      } catch {
        return null
      }
    })(),
  }
}
