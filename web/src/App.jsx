import { useEffect, useRef, useState } from 'react'
import './App.css'

export default function App() {
  const [messages, setMessages] = useState([])
  const [input, setInput] = useState('')
  const [streaming, setStreaming] = useState(false)
  const [docText, setDocText] = useState('')
  const [docStatus, setDocStatus] = useState('')
  const [documents, setDocuments] = useState([])
  const [models, setModels] = useState([])
  const [model, setModel] = useState('')
  const bottomRef = useRef(null)

  useEffect(() => {
    fetch('/api/messages')
      .then((r) => r.json())
      .then((data) => setMessages(data ?? []))
      .catch(() => {})
    refreshDocuments()
    fetch('/api/models')
      .then((r) => r.json())
      .then((data) => {
        const list = data ?? []
        setModels(list)
        if (list.length > 0) setModel((m) => m || list[0].name)
      })
      .catch(() => {})
  }, [])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  function refreshDocuments() {
    fetch('/api/documents')
      .then((r) => r.json())
      .then((data) => setDocuments(data ?? []))
      .catch(() => {})
  }

  async function sendMessage(e) {
    e.preventDefault()
    const text = input.trim()
    if (!text || streaming) return

    setInput('')
    setStreaming(true)
    setMessages((prev) => [...prev, { role: 'user', content: text }, { role: 'assistant', content: '', sources: [] }])

    try {
      const res = await fetch('/api/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message: text, model }),
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

          if (eventType === 'sources' && payload.sources) {
            setMessages((prev) => {
              const next = [...prev]
              next[next.length - 1] = { ...next[next.length - 1], sources: payload.sources }
              return next
            })
          } else if (payload.token) {
            setMessages((prev) => {
              const next = [...prev]
              next[next.length - 1] = {
                ...next[next.length - 1],
                content: next[next.length - 1].content + payload.token,
              }
              return next
            })
          }
        }
      }
    } catch (err) {
      setMessages((prev) => [...prev, { role: 'assistant', content: `Error: ${err.message}` }])
    } finally {
      setStreaming(false)
    }
  }

  async function uploadDocument(e) {
    e.preventDefault()
    if (!docText.trim()) return
    setDocStatus('Uploading…')
    try {
      const res = await fetch('/api/documents', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ filename: 'pasted-text.txt', content: docText }),
      })
      if (!res.ok) throw new Error(await res.text())
      const data = await res.json()
      setDocStatus(`Indexed ${data.chunks} chunk(s).`)
      setDocText('')
      refreshDocuments()
    } catch (err) {
      setDocStatus(`Error: ${err.message}`)
    }
  }

  return (
    <div className="app">
      <div className="titlebar">
        <div className="traffic-lights">
          <span className="dot red" />
          <span className="dot yellow" />
          <span className="dot green" />
        </div>
        <span className="titlebar-title">fastllm</span>
      </div>

      <div className="body">
        <aside className="sidebar">
          <section className="panel">
            <h2>Model</h2>
            <select
              className="model-select"
              value={model}
              onChange={(e) => setModel(e.target.value)}
              disabled={models.length === 0}
            >
              {models.length === 0 && <option>Default</option>}
              {models.map((m) => (
                <option key={m.name} value={m.name}>
                  {m.name}
                </option>
              ))}
            </select>
          </section>

          <section className="panel">
            <h2>Knowledge base</h2>
            <form onSubmit={uploadDocument}>
              <textarea
                placeholder="Paste text to index for retrieval…"
                value={docText}
                onChange={(e) => setDocText(e.target.value)}
                rows={7}
              />
              <button type="submit" className="btn-primary">Index text</button>
            </form>
            {docStatus && <p className="status">{docStatus}</p>}

            <ul className="doc-list">
              {documents.length === 0 && <li className="doc-empty">Nothing indexed yet</li>}
              {documents.map((d) => (
                <li key={d.id} className="doc-item">
                  <span className="doc-name">{d.filename}</span>
                  <span className="doc-count">{d.chunk_count} chunk{d.chunk_count === 1 ? '' : 's'}</span>
                </li>
              ))}
            </ul>
          </section>
        </aside>

        <main className="chat">
          <div className="messages">
            {messages.map((m, i) => (
              <div key={i} className={`message ${m.role}`}>
                <span className="role">{m.role}</span>
                <p>{m.content}</p>
                {m.sources && m.sources.length > 0 && (
                  <details className="sources">
                    <summary>{m.sources.length} source{m.sources.length === 1 ? '' : 's'}</summary>
                    <ul>
                      {m.sources.map((s, si) => (
                        <li key={si}>{s.content}</li>
                      ))}
                    </ul>
                  </details>
                )}
              </div>
            ))}
            <div ref={bottomRef} />
          </div>

          <form className="composer" onSubmit={sendMessage}>
            <input
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder="Ask something…"
              disabled={streaming}
            />
            <button type="submit" className="btn-primary" disabled={streaming || !input.trim()}>
              {streaming ? 'Sending…' : 'Send'}
            </button>
          </form>
        </main>
      </div>
    </div>
  )
}
