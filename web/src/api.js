import { reportFetchFailure, reportFetchSuccess } from './connectionStatus'

async function okJson(res, context) {
  reportFetchSuccess()
  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText)
    console.error(`${context} failed (${res.status}): ${text}`)
    throw new Error(text || `request failed with ${res.status}`)
  }
  return res.json()
}

function swallowNetworkError(fallback) {
  return (err) => {
    console.error(err)
    if (err instanceof TypeError) reportFetchFailure()
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

export function clearConversations() {
  return fetch('/api/conversations', { method: 'DELETE' })
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

export function fetchFileAccessSettings() {
  return fetch('/api/settings/files')
    .then((r) => okJson(r, 'fetchFileAccessSettings'))
    .catch(swallowNetworkError({ root: '', read_enabled: false, write_enabled: false }))
}

export function saveFileAccessSettings(settings) {
  return fetch('/api/settings/files', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(settings),
  }).then((r) => okJson(r, 'saveFileAccessSettings'))
}

export function fetchCloudProviderSettings() {
  return fetch('/api/settings/cloud-providers')
    .then((r) => okJson(r, 'fetchCloudProviderSettings'))
    .catch(swallowNetworkError({
      anthropic_configured: false,
      openai_configured: false,
      gemini_configured: false,
      nvidia_configured: false,
      cloudflare_configured: false,
      osu_configured: false,
    }))
}

export function saveCloudProviderSettings(settings) {
  return fetch('/api/settings/cloud-providers', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(settings),
  }).then((r) => okJson(r, 'saveCloudProviderSettings'))
}

export function fetchNotes() {
  return fetch('/api/notes')
    .then((r) => okJson(r, 'fetchNotes'))
    .catch(swallowNetworkError({ content: '' }))
}

export function saveNotes(content) {
  return fetch('/api/notes', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content }),
  })
}

export function browseForFolder() {
  return fetch('/api/settings/files/browse', { method: 'POST' })
    .then((r) => okJson(r, 'browseForFolder'))
}

export function quitServer() {
  return fetch('/api/quit', { method: 'POST' })
}

export function isWails() {
  return false
}

const STALL_TIMEOUT_MS = 30000

export async function streamChat(
  message,
  model,
  conversationId,
  thinkLevel,
  callbacks,
  signal,
  images,
  activeFile,
) {
  const res = await fetch('/api/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      message,
      model,
      conversation_id: conversationId ?? 0,
      think_level: thinkLevel || '',
      images: images && images.length > 0 ? images : undefined,
      active_file: activeFile || undefined,
    }),
    signal,
  })
  if (!res.ok || !res.body) throw new Error(await res.text())

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  let stalled = false
  let stallTimer = setTimeout(() => {
    stalled = true
    reader.cancel().catch(() => {})
  }, STALL_TIMEOUT_MS)

  try {
    for (;;) {
      const { value, done } = await reader.read()
      clearTimeout(stallTimer)
      if (done) break
      stallTimer = setTimeout(() => {
        stalled = true
        reader.cancel().catch(() => {})
      }, STALL_TIMEOUT_MS)
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
        } else if (eventType === 'reasoning' && payload.reasoning) {
          callbacks.onReasoning?.(payload.reasoning)
        } else if (eventType === 'usage') {
          callbacks.onUsage?.(payload)
        } else if (eventType === 'tool_call') {
          callbacks.onToolCall?.(payload)
        } else if (eventType === 'build_check') {
          callbacks.onBuildCheck?.(payload)
        } else if (eventType === 'test_check') {
          callbacks.onTestCheck?.(payload)
        } else if (eventType === 'error') {
          callbacks.onError?.(payload.error || 'The model backend returned an error.')
        } else if (payload.token) {
          callbacks.onToken?.(payload.token)
        }
      }
    }
  } catch (err) {
    if (stalled) {
      throw new Error(`The model stopped responding (no output for ${STALL_TIMEOUT_MS / 1000}s). It may be stuck — try again or switch models.`)
    }
    throw err
  } finally {
    clearTimeout(stallTimer)
  }
}

// streamHarnessRun executes an autonomous agent task on the backend via SSE streaming.
export async function streamHarnessRun(payload, onEvent, onError, signal) {
  try {
    const res = await fetch('/api/harness/run?stream=true', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Accept: 'text/event-stream',
      },
      body: JSON.stringify(payload),
      signal,
    })

    if (!res.ok) {
      const errText = await res.text().catch(() => res.statusText)
      onError?.(new Error(errText || `Harness task failed (${res.status})`))
      return
    }

    const reader = res.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''

    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })

      const chunks = buffer.split('\n\n')
      buffer = chunks.pop() ?? ''

      for (const chunk of chunks) {
        const lines = chunk.split('\n')
        const dataLine = lines.find((l) => l.startsWith('data:'))
        if (!dataLine) continue
        try {
          const ev = JSON.parse(dataLine.slice(5).trim())
          onEvent?.(ev)
        } catch (e) {
          console.error('Failed to parse harness SSE event:', chunk, e)
        }
      }
    }
  } catch (err) {
    if (err.name !== 'AbortError') {
      onError?.(err)
    }
  }
}

export function setLiveTarget(conversationId) {
  return fetch('/api/live/target', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ conversation_id: conversationId }),
  })
}

export async function subscribeLiveChat(callbacks, signal) {
  const res = await fetch('/api/live/stream', { signal })
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
      const dataLine = event.split('\n').find((l) => l.startsWith('data:'))
      if (!dataLine) continue
      const payload = JSON.parse(dataLine.slice(5).trim())
      callbacks.onEvent?.(payload)
    }
  }
}
