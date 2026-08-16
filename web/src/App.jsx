import { useEffect, useRef, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import rehypeKatex from 'rehype-katex'
import rehypeHighlight from 'rehype-highlight'
import 'katex/dist/katex.min.css'
import 'highlight.js/styles/github-dark.css'
import './App.css'

// Text files we accept for direct upload — anything else likely needs
// server-side extraction (e.g. PDFs) which isn't wired up yet.
const TEXT_FILE_PATTERN = /\.(txt|md|markdown|mdx|json|ya?ml|csv|tsv|log|go|js|jsx|ts|tsx|py|rb|java|c|cc|cpp|h|hpp|rs|sh|sql|html|css|xml)$/i

function MessageContent({ content }) {
  return (
    <div className="markdown">
      <ReactMarkdown
        remarkPlugins={[remarkGfm, remarkMath]}
        rehypePlugins={[rehypeKatex, rehypeHighlight]}
      >
        {content}
      </ReactMarkdown>
    </div>
  )
}

export default function App() {
  const [messages, setMessages] = useState([])
  const [input, setInput] = useState('')
  const [streaming, setStreaming] = useState(false)
  const [docText, setDocText] = useState('')
  const [docStatus, setDocStatus] = useState('')
  const [documents, setDocuments] = useState([])
  const [models, setModels] = useState([])
  const [model, setModel] = useState('')
  const [skills, setSkills] = useState([])
  const [skillId, setSkillId] = useState('')
  const [skillFormOpen, setSkillFormOpen] = useState(false)
  const [skillName, setSkillName] = useState('')
  const [skillPrompt, setSkillPrompt] = useState('')
  const bottomRef = useRef(null)
  const fileInputRef = useRef(null)

  useEffect(() => {
    fetch('/api/messages')
      .then((r) => r.json())
      .then((data) => setMessages(data ?? []))
      .catch(() => {})
    refreshDocuments()
    refreshSkills()
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

  function refreshSkills() {
    fetch('/api/skills')
      .then((r) => r.json())
      .then((data) => setSkills(data ?? []))
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
        body: JSON.stringify({ message: text, model, skill_id: skillId ? Number(skillId) : 0 }),
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

  async function indexContent(filename, content) {
    setDocStatus('Uploading…')
    try {
      const res = await fetch('/api/documents', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ filename, content }),
      })
      if (!res.ok) throw new Error(await res.text())
      const data = await res.json()
      setDocStatus(`Indexed ${data.chunks} chunk(s) from ${filename}.`)
      refreshDocuments()
    } catch (err) {
      setDocStatus(`Error: ${err.message}`)
    }
  }

  async function uploadDocument(e) {
    e.preventDefault()
    if (!docText.trim()) return
    await indexContent('pasted-text.txt', docText)
    setDocText('')
  }

  async function handleFilePicked(e) {
    const files = Array.from(e.target.files ?? [])
    e.target.value = '' // allow re-selecting the same file later

    for (const file of files) {
      if (!TEXT_FILE_PATTERN.test(file.name)) {
        setDocStatus(`Skipped ${file.name}: unsupported file type.`)
        continue
      }
      const text = await file.text()
      await indexContent(file.name, text)
    }
  }

  async function createSkill(e) {
    e.preventDefault()
    if (!skillName.trim() || !skillPrompt.trim()) return
    try {
      const res = await fetch('/api/skills', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: skillName, prompt: skillPrompt }),
      })
      if (!res.ok) throw new Error(await res.text())
      setSkillName('')
      setSkillPrompt('')
      setSkillFormOpen(false)
      refreshSkills()
    } catch {
      // Best-effort: leave the form open with the user's input intact.
    }
  }

  async function deleteSkill(id) {
    try {
      await fetch(`/api/skills/${id}`, { method: 'DELETE' })
      if (String(skillId) === String(id)) setSkillId('')
      refreshSkills()
    } catch {
      // ignore
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
            <h2>Skill</h2>
            <select
              className="model-select"
              value={skillId}
              onChange={(e) => setSkillId(e.target.value)}
            >
              <option value="">General assistant</option>
              {skills.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </select>

            {skills.length > 0 && (
              <ul className="doc-list">
                {skills.map((s) => (
                  <li key={s.id} className="doc-item">
                    <span className="doc-name">{s.name}</span>
                    <button type="button" className="btn-icon" title="Delete skill" onClick={() => deleteSkill(s.id)}>
                      ×
                    </button>
                  </li>
                ))}
              </ul>
            )}

            {skillFormOpen ? (
              <form onSubmit={createSkill} className="skill-form">
                <input
                  className="skill-name-input"
                  placeholder="Skill name (e.g. Code Reviewer)"
                  value={skillName}
                  onChange={(e) => setSkillName(e.target.value)}
                />
                <textarea
                  placeholder="System prompt for this skill…"
                  value={skillPrompt}
                  onChange={(e) => setSkillPrompt(e.target.value)}
                  rows={5}
                />
                <div className="skill-form-actions">
                  <button type="submit" className="btn-primary">Save skill</button>
                  <button type="button" className="btn-secondary" onClick={() => setSkillFormOpen(false)}>
                    Cancel
                  </button>
                </div>
              </form>
            ) : (
              <button type="button" className="btn-secondary" onClick={() => setSkillFormOpen(true)}>
                + New skill
              </button>
            )}
          </section>

          <section className="panel">
            <h2>Knowledge base</h2>
            <form onSubmit={uploadDocument}>
              <textarea
                placeholder="Paste text to index for retrieval…"
                value={docText}
                onChange={(e) => setDocText(e.target.value)}
                rows={6}
              />
              <button type="submit" className="btn-primary">Index text</button>
            </form>

            <input
              ref={fileInputRef}
              type="file"
              accept=".txt,.md,.markdown,.mdx,.json,.yaml,.yml,.csv,.tsv,.log,.go,.js,.jsx,.ts,.tsx,.py,.rb,.java,.c,.cc,.cpp,.h,.hpp,.rs,.sh,.sql,.html,.css,.xml"
              multiple
              hidden
              onChange={handleFilePicked}
            />
            <button type="button" className="btn-secondary" onClick={() => fileInputRef.current?.click()}>
              Upload files…
            </button>

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
                <MessageContent content={m.content} />
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
