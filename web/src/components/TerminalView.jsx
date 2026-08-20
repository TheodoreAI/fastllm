import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { terminalWSHost } from '../api'

// Matched to App.css's dark/light CSS custom properties (--bg-chat,
// --text, --accent, etc.) rather than read at runtime via
// getComputedStyle — xterm's theme isn't reactive to CSS changes anyway,
// so a plain object re-applied on theme change is simpler and avoids a
// CSS-load timing dependency.
const DARK_THEME = {
  background: '#1e1e1e',
  foreground: '#f2f2f2',
  cursor: '#f2f2f2',
  selectionBackground: 'rgba(10, 132, 255, 0.35)',
  black: '#1e1e1e',
  brightBlack: '#6e6e73',
  blue: '#0a84ff',
  brightBlue: '#409cff',
}

const LIGHT_THEME = {
  background: '#ffffff',
  foreground: '#1c1c1e',
  cursor: '#1c1c1e',
  selectionBackground: 'rgba(0, 122, 255, 0.25)',
  black: '#ffffff',
  brightBlack: '#8e8e93',
  blue: '#007aff',
  brightBlue: '#409cff',
}

async function wsURL() {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const host = await terminalWSHost()
  return `${proto}//${host}/api/terminal/ws`
}

// Owns one persistent PowerShell session for the lifetime of the mounted
// component. App.jsx keeps this mounted (just hidden) across tab switches
// so the session and scrollback survive navigating to Chat/Editor and back
// — see the always-mounted / display:none convention used for every other
// pane there.
export default function TerminalView({ theme, folderRoot }) {
  const containerRef = useRef(null)
  const termRef = useRef(null)
  const fitAddonRef = useRef(null)
  const socketRef = useRef(null)
  const unmountedRef = useRef(false)
  const folderRootRef = useRef(folderRoot)
  const [status, setStatus] = useState('connecting') // connecting | connected | disconnected | elevated

  useEffect(() => {
    // Reset explicitly on every mount, not just declared once via
    // useRef(false) — React StrictMode's dev-only double-invoke (mount ->
    // cleanup -> mount again, same component instance, same refs) leaves
    // this stuck at true forever after the first synthetic cleanup
    // otherwise, silently short-circuiting connect() below on every real
    // mount that follows (see the guard right after wsURL() resolves).
    // Harmless to also do this in a production build, which never
    // double-invokes and would just be re-setting an already-false value.
    unmountedRef.current = false
    const term = new Terminal({
      cursorBlink: true,
      fontFamily: 'Menlo, Consolas, "SF Mono", monospace',
      fontSize: 13,
      theme: theme === 'light' ? LIGHT_THEME : DARK_THEME,
      // Mouse drag-select copies to the clipboard immediately, matching
      // every native terminal emulator's convention — no explicit Ctrl+C
      // needed for the common case, which stays reserved for the shell.
      copyOnSelect: true,
    })
    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.open(containerRef.current)
    termRef.current = term
    fitAddonRef.current = fitAddon

    // xterm has no built-in copy/paste keybindings of its own — Ctrl+C/V
    // are terminal control characters (SIGINT / literal ^V), so without
    // this the browser's own defaults are the only thing deciding what
    // those keys do, which is inconsistent across browsers. Standard
    // terminal-emulator convention (used by VS Code's integrated
    // terminal, Windows Terminal, etc.) is Ctrl+Shift+C/V for
    // copy/paste, leaving plain Ctrl+C/V free for the shell.
    term.attachCustomKeyEventHandler((e) => {
      if (e.type !== 'keydown') return true
      if (e.ctrlKey && e.shiftKey && e.key.toLowerCase() === 'c') {
        const selection = term.getSelection()
        if (selection) navigator.clipboard.writeText(selection).catch(() => {})
        return false
      }
      if (e.ctrlKey && e.shiftKey && e.key.toLowerCase() === 'v') {
        navigator.clipboard
          .readText()
          .then((text) => term.paste(text))
          .catch(() => {})
        return false
      }
      return true
    })

    // Right-click pastes immediately (standard terminal-emulator
    // convention) instead of showing the browser's default context menu,
    // which has nothing useful to offer inside a terminal surface.
    function handleContextMenu(e) {
      e.preventDefault()
      navigator.clipboard
        .readText()
        .then((text) => term.paste(text))
        .catch(() => {})
    }
    const container = containerRef.current
    container.addEventListener('contextmenu', handleContextMenu)

    // Deferred one frame rather than called synchronously right after
    // open(): the container (.terminal-surface) may not have settled
    // into its final flex-computed size in the same paint yet, which was
    // producing an undersized initial render (small text area, lots of
    // unused space below the prompt) until something else — a manual
    // window resize — happened to trigger the ResizeObserver's
    // corrective re-fit below. Mirrors that same rAF-deferral pattern.
    const initialFitId = requestAnimationFrame(() => {
      fitAddon.fit()
    })

    connect(term, fitAddon)

    // Deferred via requestAnimationFrame rather than called directly from
    // the observer callback: fitAddon.fit() resizes the terminal's own
    // canvas inside the observed container, which can itself trigger
    // another ResizeObserver notification in the same frame — without
    // deferring, this becomes a same-frame observe -> mutate -> observe
    // loop that Chromium detects and kills the tab for ("ResizeObserver
    // loop completed with undelivered notifications"), confirmed by
    // reproducing a page crash without this guard.
    let rafId = null
    const resizeObserver = new ResizeObserver(() => {
      if (rafId != null) return
      rafId = requestAnimationFrame(() => {
        rafId = null
        fitAddon.fit()
        sendResize(term)
      })
    })
    resizeObserver.observe(containerRef.current)

    return () => {
      unmountedRef.current = true
      cancelAnimationFrame(initialFitId)
      if (rafId != null) cancelAnimationFrame(rafId)
      resizeObserver.disconnect()
      container.removeEventListener('contextmenu', handleContextMenu)
      socketRef.current?.close()
      term.dispose()
    }
    // Intentionally runs once on mount only — theme changes are handled by
    // the separate effect below rather than tearing down the session.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    if (termRef.current) {
      termRef.current.options.theme = theme === 'light' ? LIGHT_THEME : DARK_THEME
    }
  }, [theme])

  // folderRoot (fileAccessSettings.root, threaded down from App.jsx via
  // EditorView) changes once a folder picker selection actually completes
  // — unlike openFolderSignal, which only means "the picker dialog was
  // requested" and fires whether or not anything was picked (including on
  // a cancelled dialog). The session's cwd is fixed at spawn time — read
  // from fileReader.GetRoot() only when Start() is called (see
  // internal/terminal/handler.go) — so without this, switching folders
  // silently leaves every already-open terminal tab sitting in the old
  // folder with no indication anything's stale, since the shell has no way
  // to know the "opened folder" concept changed underneath it. Restarting
  // reconnects the WebSocket, which spawns a fresh session against
  // whatever folder is current by then. Guarded against firing on mount
  // (folderRootRef is seeded with the initial value at declare time, so
  // the first run here always sees "unchanged") since connect() is
  // already called once from the mount effect above.
  useEffect(() => {
    const prev = folderRootRef.current
    folderRootRef.current = folderRoot
    // Both sides must be a real, already-known root — folderRoot starts
    // as '' in App.jsx until the initial GET /api/settings/files resolves,
    // and that '' -> real-root transition on first load is not a folder
    // switch, just settings finishing hydration; restarting on it would
    // reconnect (and briefly flicker) every terminal tab on every app
    // launch for no reason.
    if (prev && folderRoot && prev !== folderRoot) {
      restart()
    }
    // restart intentionally omitted — it closes over refs, not state, so
    // it doesn't need to be in the dependency array.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [folderRoot])

  function sendResize(term) {
    const socket = socketRef.current
    if (socket && socket.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
    }
  }

  async function connect(term, fitAddon) {
    setStatus('connecting')
    term.reset()
    const url = await wsURL()
    // The awaited resolution above means the component may have unmounted
    // before this fires — guard against opening a socket nothing will
    // ever close via the effect cleanup below.
    if (unmountedRef.current) return
    const socket = new WebSocket(url)
    socket.binaryType = 'arraybuffer'
    socketRef.current = socket

    socket.onopen = () => {
      setStatus('connected')
      fitAddon.fit()
      sendResize(term)
    }
    socket.onmessage = (event) => {
      if (event.data instanceof ArrayBuffer) {
        term.write(new Uint8Array(event.data))
      }
    }
    socket.onclose = (event) => {
      // "elevated" is a short, stable machine-readable reason the server
      // sends (see internal/terminal/handler.go) when fastllm itself is
      // running as Administrator — a deterministic, permanent refusal,
      // not a dropped connection, so it gets its own message instead of
      // the generic "disconnected" state with a "Restart" button that
      // would just fail identically every time.
      setStatus(event.reason === 'elevated' ? 'elevated' : 'disconnected')
    }
    socket.onerror = () => setStatus('disconnected')

    term.onData((data) => {
      if (socket.readyState === WebSocket.OPEN) {
        socket.send(new TextEncoder().encode(data))
      }
    })
  }

  function restart() {
    socketRef.current?.close()
    connect(termRef.current, fitAddonRef.current)
  }

  return (
    <div className="terminal-view">
      <div ref={containerRef} className="terminal-surface" />
      {status === 'disconnected' && (
        <div className="terminal-disconnected-overlay">
          <p>Terminal disconnected.</p>
          <button type="button" onClick={restart}>
            Restart terminal
          </button>
        </div>
      )}
      {status === 'elevated' && (
        <div className="terminal-disconnected-overlay">
          <p>
            fastllm is running as Administrator. The terminal refuses to start an
            elevated shell — restart fastllm without admin rights to use it.
          </p>
        </div>
      )}
    </div>
  )
}
