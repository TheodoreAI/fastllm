// WCAG 2.x relative-luminance contrast ratio between two colors, used by
// TweakBar's live accessibility readouts (see components/TweakBar.jsx).
// Accepts any CSS color string a browser can parse (#hex, rgb(), named
// colors, etc.) by resolving it through a throwaway element's computed
// style rather than hand-rolling a parser for every format.

function resolveToRgb(cssColor) {
  const probe = document.createElement('div')
  probe.style.color = cssColor
  document.body.appendChild(probe)
  const rgb = getComputedStyle(probe).color
  document.body.removeChild(probe)
  const m = rgb.match(/[\d.]+/g)
  if (!m) return [0, 0, 0]
  return m.slice(0, 3).map(Number)
}

function relativeLuminance([r, g, b]) {
  const channel = (c) => {
    const v = c / 255
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
}

export function contrastRatio(colorA, colorB) {
  const lumA = relativeLuminance(resolveToRgb(colorA))
  const lumB = relativeLuminance(resolveToRgb(colorB))
  const lighter = Math.max(lumA, lumB)
  const darker = Math.min(lumA, lumB)
  return (lighter + 0.05) / (darker + 0.05)
}

// WCAG 2.x AA thresholds: 4.5:1 for normal text, 3:1 for large text
// (≥18pt, or ≥14pt bold) and for non-text UI components/graphics.
export function aaLevel(ratio, { large = false } = {}) {
  const threshold = large ? 3.0 : 4.5
  return ratio >= threshold ? 'pass' : 'fail'
}
