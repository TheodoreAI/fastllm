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
  return res.json()
}

export function fetchConversations() {
  return fetch('/api/conversations')
    .then((r) => okJson(r, 'fetchConversations'))
    .then((data) => data ?? [])
    .catch(() => [])
}

export function fetchMessages(conversationId) {
  return fetch(`/api/messages?conversation_id=${conversationId}`)
    .then((r) => okJson(r, 'fetchMessages'))
    .then((data) => data ?? [])
    .catch(() => [])
}

export function deleteConversation(id) {
  return fetch(`/api/conversations/${id}`, { method: 'DELETE' })
}

export function fetchDocuments() {
  return fetch('/api/documents')
    .then((r) => okJson(r, 'fetchDocuments'))
    .then((data) => data ?? [])
    .catch(() => [])
}

export function fetchSkills() {
  return fetch('/api/skills')
    .then((r) => okJson(r, 'fetchSkills'))
    .then((data) => data ?? [])
    .catch(() => [])
}

export function fetchModels() {
  return fetch('/api/models')
    .then((r) => okJson(r, 'fetchModels'))
    .then((data) => data ?? [])
    .catch(() => [])
}

export function fetchSettings() {
  return fetch('/api/settings')
    .then((r) => okJson(r, 'fetchSettings'))
    .catch(() => null)
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
export function uploadFile(file) {
  const form = new FormData()
  form.append('file', file, file.name)
  return fetch('/api/documents/upload', { method: 'POST', body: form })
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
    .catch(() => null)
}

export function fetchFileAccessSettings() {
  return fetch('/api/settings/files')
    .then((r) => okJson(r, 'fetchFileAccessSettings'))
    .catch(() => ({ root: '', read_enabled: false, write_enabled: false }))
}

export function fetchTerminalSettings() {
  return fetch('/api/settings/terminal')
    .then((r) => okJson(r, 'fetchTerminalSettings'))
    .catch(() => ({ enabled: false }))
}

// Opens a native OS folder-picker dialog on the machine running the
// server (not the browser) and resolves with the chosen absolute path.
// The request blocks server-side until the user picks a folder or
// cancels the dialog, so this can take a while — that's expected.
export function browseForFolder() {
  return fetch('/api/settings/files/browse', { method: 'POST' })
    .then((r) => okJson(r, 'browseForFolder'))
}

export function quitServer() {
  return fetch('/api/quit', { method: 'POST' })
}

export function fetchEditorTree() {
  return fetch('/api/editor/tree')
    .then((r) => okJson(r, 'fetchEditorTree'))
    .then((data) => data ?? [])
    .catch(() => [])
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
    .catch(() => [])
}

export function fetchGitStatus() {
  return fetch('/api/editor/git/status')
    .then((r) => okJson(r, 'fetchGitStatus'))
    .then((data) => data ?? [])
    .catch(() => [])
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
    .catch(() => [])
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

// Streams a chat response via SSE, invoking the provided callbacks as
// events arrive. Returns once the stream completes. Pass `signal` (from
// an AbortController) to let the caller cancel mid-stream — aborting the
// fetch closes the underlying connection, which Go's http.Server turns
// into context cancellation on the server, propagating all the way to
// the in-flight request to the LLM backend (see llm.Client.StreamChat),
// so this genuinely stops generation rather than just hiding it.
export async function streamChat({ message, model, skillId, conversationId, thinkLevel }, callbacks, signal) {
  const res = await fetch('/api/chat', {
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
