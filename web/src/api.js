export function fetchConversations() {
  return fetch('/api/conversations')
    .then((r) => r.json())
    .then((data) => data ?? [])
    .catch(() => [])
}

export function fetchMessages(conversationId) {
  return fetch(`/api/messages?conversation_id=${conversationId}`)
    .then((r) => r.json())
    .then((data) => data ?? [])
    .catch(() => [])
}

export function deleteConversation(id) {
  return fetch(`/api/conversations/${id}`, { method: 'DELETE' })
}

export function fetchDocuments() {
  return fetch('/api/documents')
    .then((r) => r.json())
    .then((data) => data ?? [])
    .catch(() => [])
}

export function fetchSkills() {
  return fetch('/api/skills')
    .then((r) => r.json())
    .then((data) => data ?? [])
    .catch(() => [])
}

export function fetchModels() {
  return fetch('/api/models')
    .then((r) => r.json())
    .then((data) => data ?? [])
    .catch(() => [])
}

export function fetchSettings() {
  return fetch('/api/settings')
    .then((r) => r.json())
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

// Streams a chat response via SSE, invoking the provided callbacks as
// events arrive. Returns once the stream completes.
export async function streamChat({ message, model, skillId, conversationId }, callbacks) {
  const res = await fetch('/api/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      message,
      model,
      skill_id: skillId ? Number(skillId) : 0,
      conversation_id: conversationId ?? 0,
    }),
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
      } else if (payload.token) {
        callbacks.onToken?.(payload.token)
      }
    }
  }
}
