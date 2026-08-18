import { reportFetchFailure, reportFetchSuccess } from './connectionStatus'

// Parses a fetch response as JSON, but only if the request actually
// succeeded — calling r.json() on a non-OK response (which the backend
// sends as a plain-text error body, not JSON) throws an opaque
// SyntaxError that masks the real error message. Logs the real failure
// so it's at least visible in devtools instead of being fully silent.
async function okJson(res, context) {
  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText)
    console.error(`${context} failed (${res.status}): ${text}`)
    throw new Error(text || `request failed with ${res.status}`)
  }
  reportFetchSuccess()
  return res.json()
}

// The 13 background-refresh functions below (fetchConversations,
// fetchModels, fetchGitStatus, …) all resolve to a safe default on
// failure by design — every caller (App.jsx, EditorView.jsx) does
// `.then(setX)` and expects an array/object/null it can render directly,
// never a rejected promise it would need a try/catch around. That's
// deliberate and stays as-is. What was missing is that the failure was
// only ever visible in the console (via okJson's console.error above) —
// swallowNetworkError adds the one line each of those 13 needs to also
// flip the shared connection-status signal (see connectionStatus.js),
// which useConnectionStatus/App.jsx surface as a banner, without changing
// any of their resolved-value contracts.
function swallowNetworkError(fallback) {
  return (err) => {
    console.error(err)
    reportFetchFailure()
    return fallback
  }
}

export function fetchConversations() {
  return fetch('/api/conversations')
    .then((r) => okJson(r, 'fetchConversations'))
    .then((data) => data ?? [])
    .catch(swallowNetworkError([]))
}

export function fetchMessages(conversationId) {
  return fetch(`/api/messages?conversation_id=${conversationId}`)
    .then((r) => okJson(r, 'fetchMessages'))
    .then((data) => data ?? [])
    .catch(swallowNetworkError([]))
}

export function deleteConversation(id) {
  return fetch(`/api/conversations/${id}`, { method: 'DELETE' })
}

export function fetchDocuments() {
  return fetch('/api/documents')
    .then((r) => okJson(r, 'fetchDocuments'))
    .then((data) => data ?? [])
    .catch(swallowNetworkError([]))
}

export function fetchSkills() {
  return fetch('/api/skills')
    .then((r) => okJson(r, 'fetchSkills'))
    .then((data) => data ?? [])
    .catch(swallowNetworkError([]))
}

export function fetchModels() {
  return fetch('/api/models')
    .then((r) => okJson(r, 'fetchModels'))
    .then((data) => data ?? [])
    .catch(swallowNetworkError([]))
}

export function fetchSettings() {
  return fetch('/api/settings')
    .then((r) => okJson(r, 'fetchSettings'))
    .catch(swallowNetworkError(null))
}

export function createSkill(name, prompt) {
  return fetch('/api/skills', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, prompt }),
  })
}

export function deleteSkillById(id) {
  return fetch(`/api/skills/${id}`, { method: 'DELETE' })
}

export function indexDocument(filename, content) {
  return fetch('/api/documents', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ filename, content }),
  })
}

// Uploads one real file (used for PDFs and any file coming from a folder
// drop) via multipart/form-data so the server can extract text itself.
// Routed through apiOrigin() for the same reason streamChat is: Wails'
// in-process AssetServer bridge (see cmd/desktop/main.go) doesn't handle
// multipart/binary POST bodies reliably — file uploads over it can arrive
// truncated or empty server-side, which is what surfaced as every folder
// upload's files being wrongly flagged "empty" in the desktop build (the
// plain-browser build was never affected, since it always talks to a real
// net/http listener).
//
// relativePath, when given (folder uploads pass webkitRelativePath), is
// sent as a separate "path" field rather than as the multipart filename
// itself — Go's mime/multipart runs the filename field through
// filepath.Base() while parsing (path-traversal hardening on their end),
// so any '/' in it is silently dropped before the handler ever sees it.
// The server prefers this field when present (see UploadFile in
// internal/chat/handler.go), which is what lets the stored document
// filename carry its folder — KnowledgeBasePanel groups the list by
// directory instead of showing a flat pile of same-looking basenames
// (multiple index.ts, etc.).
export async function uploadFile(file, relativePath) {
  const origin = await apiOrigin()
  const form = new FormData()
  form.append('file', file, file.name)
  if (relativePath) form.append('path', relativePath)
  return fetch(`${origin}/api/documents/upload`, { method: 'POST', body: form })
}

export function clearKnowledgeBase() {
  return fetch('/api/documents', { method: 'DELETE' })
}

export function clearConversations() {
  return fetch('/api/conversations', { method: 'DELETE' })
}

export function fetchRagSettings() {
  return fetch('/api/settings/rag')
    .then((r) => okJson(r, 'fetchRagSettings'))
    .catch(swallowNetworkError(null))
}

export function fetchFileAccessSettings() {
  return fetch('/api/settings/files')
    .then((r) => okJson(r, 'fetchFileAccessSettings'))
    .catch(swallowNetworkError({ root: '', read_enabled: false, write_enabled: false }))
}

export function fetchTerminalSettings() {
  return fetch('/api/settings/terminal')
    .then((r) => okJson(r, 'fetchTerminalSettings'))
    .catch(swallowNetworkError({ enabled: false }))
}

export function fetchCloudProviderSettings() {
  return fetch('/api/settings/cloud-providers')
    .then((r) => okJson(r, 'fetchCloudProviderSettings'))
    .catch(swallowNetworkError({ anthropic_configured: false, openai_configured: false, gemini_configured: false }))
}

// Opens a native OS folder-picker dialog on the machine running the
// server (not the browser) and resolves with the chosen absolute path.
// The request blocks server-side until the user picks a folder or
// cancels the dialog, so this can take a while — that's expected.
export function browseForFolder() {
  return fetch('/api/settings/files/browse', { method: 'POST' })
    .then((r) => okJson(r, 'browseForFolder'))
}

// window.runtime is injected by Wails into every page it loads (see
// internal/frontend/runtime's JS bridge) — detecting it this way, rather
// than importing @wailsjs/runtime, means this file works unmodified in
// both the plain-browser build (cmd/server) and the desktop build
// (cmd/desktop) without adding a new frontend dependency, mirroring how
// the old Tauri branch detected window.__TAURI__ for the same purpose.
export function isWails() {
  return typeof window !== 'undefined' && typeof window.runtime?.Quit === 'function'
}

// The terminal WebSocket can't go through Wails' normal in-process bridge
// (see cmd/desktop/main.go's termListener comment for why) — it needs the
// real loopback TCP listener cmd/desktop opens alongside it, whose port is
// exposed via a bound Go method rather than a fixed/predictable one, since
// a fixed port could collide with another local process. Falls back to
// window.location.host for the plain-browser build, which has no such
// bridge to route around.
export async function terminalWSHost() {
  if (isWails() && window.go?.main?.terminalBridge?.TerminalPort) {
    const port = await window.go.main.terminalBridge.TerminalPort()
    return `127.0.0.1:${port}`
  }
  return window.location.host
}

// SSE streaming has the same problem as the terminal WebSocket above: Wails'
// in-process AssetServer bridge doesn't implement http.Flusher (see
// cmd/desktop/main.go's termListener comment), so a chat response never
// flushes and the request just hangs until the server 500s with "streaming
// unsupported". Routing through the same real loopback listener the
// terminal bridge already opens fixes it, since that listener is a genuine
// net/http connection. Falls back to a relative URL for the plain-browser
// build, which has no such bridge to route around.
export async function apiOrigin() {
  if (isWails() && window.go?.main?.terminalBridge?.TerminalPort) {
    const port = await window.go.main.terminalBridge.TerminalPort()
    return `http://127.0.0.1:${port}`
  }
  return ''
}

export function quitServer() {
  if (isWails()) {
    // cmd/desktop's OnShutdown hook (see cmd/desktop/main.go) does the
    // same terminal-session cleanup /api/quit does server-side — calling
    // the native Quit() closes the window, which triggers that hook,
    // rather than POSTing to a server the desktop app doesn't expose a
    // fetchable /api/quit distinction for.
    window.runtime.Quit()
    return Promise.resolve()
  }
  return fetch('/api/quit', { method: 'POST' })
}

// GET /api/screenshot only exists on cmd/desktop's mux (see
// cmd/desktop/main.go's screenshotHandler) — a plain browser tab has no
// OS window to capture, so this is only ever called from behind an
// isWails() check.
export function captureScreenshot() {
  return fetch('/api/screenshot').then((r) => {
    if (!r.ok) throw new Error(`captureScreenshot failed: ${r.status}`)
    return r.blob()
  })
}

export function fetchEditorTree() {
  return fetch('/api/editor/tree')
    .then((r) => okJson(r, 'fetchEditorTree'))
    .then((data) => data ?? [])
    .catch(swallowNetworkError([]))
}

export function fetchEditorFile(path) {
  return fetch(`/api/editor/file?path=${encodeURIComponent(path)}`)
    .then((r) => okJson(r, 'fetchEditorFile'))
}

export function saveEditorFile(path, content) {
  return fetch('/api/editor/file', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path, content }),
  })
}

// Creating a file is just saving an empty (or given) body to a
// not-yet-existing path — the backend's Write already creates parent
// directories as needed, so there's no separate "create" endpoint.
export function createEditorFile(path, content = '') {
  return saveEditorFile(path, content)
}

export function deleteEditorFile(path) {
  return fetch('/api/editor/file', {
    method: 'DELETE',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path }),
  })
}

export function fetchEditorFolderFileCount(path) {
  return fetch(`/api/editor/folder/file-count?path=${encodeURIComponent(path)}`)
    .then((r) => okJson(r, 'fetchEditorFolderFileCount'))
}

export function deleteEditorFolder(path) {
  return fetch('/api/editor/folder', {
    method: 'DELETE',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path }),
  })
}

export function renameEditorFile(from, to) {
  return fetch('/api/editor/file/rename', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ from, to }),
  })
}

export function searchEditor(query) {
  return fetch(`/api/editor/search?q=${encodeURIComponent(query)}`)
    .then((r) => okJson(r, 'searchEditor'))
    .then((data) => data ?? [])
    .catch(swallowNetworkError([]))
}

export function fetchGitStatus() {
  return fetch('/api/editor/git/status')
    .then((r) => okJson(r, 'fetchGitStatus'))
    .then((data) => data ?? [])
    .catch(swallowNetworkError([]))
}

export function fetchGitDiff(path, staged) {
  return fetch(`/api/editor/git/diff?path=${encodeURIComponent(path)}${staged ? '&staged=1' : ''}`)
    .then((r) => okJson(r, 'fetchGitDiff'))
}

export function stageGitPaths(paths) {
  return fetch('/api/editor/git/stage', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ paths }),
  })
}

export function unstageGitPaths(paths) {
  return fetch('/api/editor/git/unstage', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ paths }),
  })
}

export function commitGit(message) {
  return fetch('/api/editor/git/commit', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ message }),
  })
}

// Pushes the current branch to its configured upstream — a plain `git
// push`, never a force-push. If the remote has diverged (or there's no
// upstream configured), the request fails and the caller shows git's
// own error as-is rather than resolving it automatically.
export function pushGit() {
  return fetch('/api/editor/git/push', { method: 'POST' })
}

export function fetchGitBranches() {
  return fetch('/api/editor/git/branches')
    .then((r) => okJson(r, 'fetchGitBranches'))
    .then((data) => data ?? [])
    .catch(swallowNetworkError([]))
}

// Switches to an existing local branch. If uncommitted changes would be
// overwritten by the target branch, this fails (a 500/error response)
// rather than discarding or auto-stashing them.
export function switchGitBranch(name) {
  return fetch('/api/editor/git/switch', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  })
}

// Creates a new branch from the current HEAD and switches to it. Fails
// if a branch with that name already exists.
export function createGitBranch(name) {
  return fetch('/api/editor/git/branch', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  })
}

export function approveWrite(id) {
  return fetch(`/api/writes/${id}/approve`, { method: 'POST' })
}

export function rejectWrite(id) {
  return fetch(`/api/writes/${id}/reject`, { method: 'POST' })
}

export function saveRagSettings(settings) {
  return fetch('/api/settings/rag', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(settings),
  })
}

export function saveFileAccessSettings(settings) {
  return fetch('/api/settings/files', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(settings),
  })
}

export function saveTerminalSettings(settings) {
  return fetch('/api/settings/terminal', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(settings),
  })
}

export function saveCloudProviderSettings(settings) {
  return fetch('/api/settings/cloud-providers', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(settings),
  })
}

// Streams a chat response via SSE, invoking the provided callbacks as
// events arrive. Returns once the stream completes. Pass `signal` (from
// an AbortController) to let the caller cancel mid-stream — aborting the
// fetch closes the underlying connection, which Go's http.Server turns
// into context cancellation on the server, propagating all the way to
// the in-flight request to the LLM backend (see llm.Client.StreamChat),
// so this genuinely stops generation rather than just hiding it.
export async function streamChat({ message, model, skillId, conversationId, thinkLevel }, callbacks, signal) {
  const origin = await apiOrigin()
  const res = await fetch(`${origin}/api/chat`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      message,
      model,
      skill_id: skillId ? Number(skillId) : 0,
      conversation_id: conversationId ?? 0,
      think_level: thinkLevel || '',
    }),
    signal,
  })
  if (!res.ok || !res.body) throw new Error(await res.text())

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  for (;;) {
    const { value, done } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })

    const events = buffer.split('\n\n')
    buffer = events.pop() ?? ''

    for (const event of events) {
      const lines = event.split('\n')
      const eventLine = lines.find((l) => l.startsWith('event:'))
      const dataLine = lines.find((l) => l.startsWith('data:'))
      if (!dataLine) continue
      const eventType = eventLine ? eventLine.slice(6).trim() : 'message'
      const payload = JSON.parse(dataLine.slice(5).trim())

      if (eventType === 'conversation' && payload.conversation_id) {
        callbacks.onConversation?.(payload.conversation_id)
      } else if (eventType === 'sources' && payload.sources) {
        callbacks.onSources?.(payload.sources)
      } else if (eventType === 'reasoning' && payload.reasoning) {
        callbacks.onReasoning?.(payload.reasoning)
      } else if (eventType === 'tool_call') {
        callbacks.onToolCall?.(payload)
      } else if (eventType === 'pending_write') {
        callbacks.onPendingWrite?.(payload)
      } else if (eventType === 'build_check') {
        callbacks.onBuildCheck?.(payload)
      } else if (eventType === 'error') {
        callbacks.onError?.(payload.error || 'The model backend returned an error.')
      } else if (payload.token) {
        callbacks.onToken?.(payload.token)
      }
    }
  }
}
