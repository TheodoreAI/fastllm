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
