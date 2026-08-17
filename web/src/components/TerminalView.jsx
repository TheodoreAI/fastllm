import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'

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

function wsURL() {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}/api/terminal/ws`
}

// Owns one persistent PowerShell session for the lifetime of the mounted
// component. App.jsx keeps this mounted (just hidden) across tab switches
// so the session and scrollback survive navigating to Chat/Editor and back
// — see the always-mounted / display:none convention used for every other
// pane there.
export default function TerminalView({ theme }) {
  const containerRef = useRef(null)
  const termRef = useRef(null)
  const fitAddonRef = useRef(null)
  const socketRef = useRef(null)
  const [status, setStatus] = useState('connecting') // connecting | connected | disconnected

  useEffect(() => {
    const term = new Terminal({
      cursorBlink: true,
      fontFamily: 'Menlo, Consolas, "SF Mono", monospace',
      fontSize: 13,
      theme: theme === 'light' ? LIGHT_THEME : DARK_THEME,
    })
    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.open(containerRef.current)
    fitAddon.fit()
    termRef.current = term
    fitAddonRef.current = fitAddon

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
      if (rafId != null) cancelAnimationFrame(rafId)
      resizeObserver.disconnect()
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

  function sendResize(term) {
    const socket = socketRef.current
    if (socket && socket.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
    }
  }

  function connect(term, fitAddon) {
    setStatus('connecting')
    term.reset()
    const socket = new WebSocket(wsURL())
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
    socket.onclose = () => setStatus('disconnected')
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
    </div>
  )
}
