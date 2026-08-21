import { terminalWSHost } from './api'

// Not a React component — LspClient is shared across every open .go tab
// (EditorView.jsx owns one instance via a ref), unlike TerminalView.jsx's
// WebSocket which belongs to one rendered pane. The connection lifecycle
// (connect/restart, no auto-reconnect) still mirrors TerminalView's
// pattern: a dropped connection is surfaced to the caller, not silently
// retried.

async function wsURL() {
  // Same bridge port as the terminal WebSocket — LSP runs under the same
  // http.ServeMux (see internal/appserver.Build), so terminalWSHost()'s
  // Wails-bridge-vs-plain-browser routing applies unchanged.
  const host = await terminalWSHost()
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${host}/api/editor/lsp/ws`
}

// gopls (like every other LSP server) speaks 0-indexed line/character
// positions and file:// URIs — real absolute filesystem paths. Every
// path fastllm's editor API hands around (fetchEditorFile, tab paths,
// tree entries) is instead relative to the opened folder (see
// internal/files.Reader) — LspClient is constructed with that folder's
// absolute root (see EditorView.jsx's ensureLspOpen) specifically so
// pathToUri/uriToPath can bridge the two conventions; without the root
// prefix gopls receives a URI like "file:///internal/lsp/session.go"
// that doesn't correspond to any file in its workspace, and every
// request against it silently returns nothing.
function pathToUri(root, path) {
  const normalizedRoot = root.replace(/\\/g, '/').replace(/\/$/, '')
  const normalizedPath = path.replace(/\\/g, '/').replace(/^\//, '')
  const full = `${normalizedRoot}/${normalizedPath}`
  const withSlash = full.startsWith('/') ? full : `/${full}`
  return `file://${withSlash}`
}

function uriToPath(root, uri) {
  const absolute = decodeURIComponent(uri.replace(/^file:\/\//, ''))
  // Windows drive-letter paths come back as "/C:/foo/bar" — strip the
  // leading slash gopls added in front of the drive letter (see
  // internal/lsp/handler.go's pathToFileURI, the inverse operation) —
  // done before stripping the root prefix below so the comparison isn't
  // thrown off by it.
  const normalized = /^\/[A-Za-z]:\//.test(absolute) ? absolute.slice(1) : absolute
  const normalizedRoot = root.replace(/\\/g, '/').replace(/\/$/, '')
  if (normalized.startsWith(normalizedRoot + '/')) {
    return normalized.slice(normalizedRoot.length + 1)
  }
  // Outside the opened folder (e.g. a definition that resolves into
  // GOROOT/the module cache) — fastllm's editor can't open a path
  // outside its sandboxed root anyway, so this is returned as-is for
  // the caller to reject rather than silently opening the wrong tab.
  return normalized
}

export class LspClient {
  constructor({ onDiagnostics, root }) {
    this.socket = null
    this.nextId = 1
    this.pending = new Map()
    this.onDiagnostics = onDiagnostics
    this.root = root
    this.status = 'disconnected'
  }

  async connect() {
    const url = await wsURL()
    const socket = new WebSocket(url)
    this.socket = socket
    socket.onmessage = (ev) => {
      let msg
      try {
        msg = JSON.parse(ev.data)
      } catch {
        return
      }
      this._dispatch(msg)
    }
    socket.onclose = () => {
      this.status = 'disconnected'
      this._rejectAllPending(new Error('lsp: connection closed'))
    }
    await new Promise((resolve, reject) => {
      socket.onopen = () => {
        this.status = 'connected'
        resolve()
      }
      socket.onerror = () => reject(new Error('lsp: failed to connect'))
    })
  }

  restart() {
    this.socket?.close()
    this.pending.clear()
    return this.connect()
  }

  close() {
    this.socket?.close()
    this.socket = null
  }

  _dispatch(msg) {
    if (msg.id != null && this.pending.has(msg.id)) {
      const { resolve, reject } = this.pending.get(msg.id)
      this.pending.delete(msg.id)
      if (msg.error) reject(new Error(msg.error.message))
      else resolve(msg.result)
      return
    }
    if (msg.method === 'textDocument/publishDiagnostics') {
      const path = uriToPath(this.root, msg.params.uri)
      this.onDiagnostics?.(path, msg.params.diagnostics ?? [])
    }
    // Other server notifications (window/logMessage, window/showMessage,
    // $/progress, etc.) are intentionally dropped — none of them are part
    // of v1's scope (diagnostics, completion, hover, go-to-definition).
  }

  _rejectAllPending(err) {
    for (const { reject } of this.pending.values()) reject(err)
    this.pending.clear()
  }

  request(method, params) {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) {
      return Promise.reject(new Error('lsp: not connected'))
    }
    const id = this.nextId++
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject })
      this.socket.send(JSON.stringify({ jsonrpc: '2.0', id, method, params }))
    })
  }

  notify(method, params) {
    if (this.socket?.readyState === WebSocket.OPEN) {
      this.socket.send(JSON.stringify({ jsonrpc: '2.0', method, params }))
    }
  }

  didOpen(path, text) {
    this.notify('textDocument/didOpen', {
      textDocument: { uri: pathToUri(this.root, path), languageId: 'go', version: 1, text },
    })
  }

  didChange(path, text, version) {
    this.notify('textDocument/didChange', {
      textDocument: { uri: pathToUri(this.root, path), version },
      contentChanges: [{ text }],
    })
  }

  didClose(path) {
    this.notify('textDocument/didClose', { textDocument: { uri: pathToUri(this.root, path) } })
  }

  definition(path, line, character) {
    return this.request('textDocument/definition', {
      textDocument: { uri: pathToUri(this.root, path) },
      position: { line, character },
    })
  }

  completion(path, line, character) {
    return this.request('textDocument/completion', {
      textDocument: { uri: pathToUri(this.root, path) },
      position: { line, character },
    })
  }

  hover(path, line, character) {
    return this.request('textDocument/hover', {
      textDocument: { uri: pathToUri(this.root, path) },
      position: { line, character },
    })
  }
}

export { uriToPath, pathToUri }
