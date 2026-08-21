import { useEffect, useRef, useState } from 'react'
import { fetchNotes, saveNotes } from '../api'

const SAVE_DEBOUNCE_MS = 600

// A shared scratchpad, persisted in the DB (see internal/chat/notes.go)
// rather than kept in the terminal's own scrollback — meant for a
// terminal-based AI CLI (Claude Code, etc.) to record what it's learning
// while experimenting against a local model, via
// $FASTLLM_BASE_URL/api/notes[/append] (injected into terminal sessions
// when Settings → Terminal's env-injection toggle is on), with this panel
// as where a human reads or edits the same content.
export default function NotesPanel() {
  const [content, setContent] = useState('')
  const [loaded, setLoaded] = useState(false)
  const [status, setStatus] = useState('idle') // 'idle' | 'saving' | 'saved'
  const saveTimeoutRef = useRef(null)
  const latestContentRef = useRef('')

  useEffect(() => {
    let cancelled = false
    fetchNotes().then((data) => {
      if (cancelled) return
      const initial = data.content || ''
      setContent(initial)
      latestContentRef.current = initial
      setLoaded(true)
    })
    return () => {
      cancelled = true
      if (saveTimeoutRef.current) clearTimeout(saveTimeoutRef.current)
    }
  }, [])

  function handleChange(e) {
    const next = e.target.value
    setContent(next)
    latestContentRef.current = next
    setStatus('idle')
    if (saveTimeoutRef.current) clearTimeout(saveTimeoutRef.current)
    saveTimeoutRef.current = setTimeout(() => {
      setStatus('saving')
      saveNotes(latestContentRef.current).then(() => setStatus('saved'))
    }, SAVE_DEBOUNCE_MS)
  }

  return (
    <div className="editor-panel-body editor-notes-panel">
      <div className="editor-notes-header">
        <div className="editor-notes-header-title">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M14 3H6.5A1.5 1.5 0 0 0 5 4.5v15A1.5 1.5 0 0 0 6.5 21h11a1.5 1.5 0 0 0 1.5-1.5V8Z" />
            <path d="M14 3v4a1 1 0 0 0 1 1h4M9 12h6M9 16h6" />
          </svg>
          <span>Scratchpad</span>
        </div>
        <span className={`editor-notes-status editor-notes-status-${status}`}>
          {status === 'saving' ? 'Saving…' : status === 'saved' ? 'Saved' : ''}
        </span>
      </div>
      <div className="editor-notes-hint">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <rect x="3" y="4" width="18" height="14" rx="2" />
          <path d="m7 9 3 3-3 3M13 15h4" />
        </svg>
        <p>
          A terminal AI CLI can read and append here via{' '}
          <code className="settings-inline-code">$FASTLLM_BASE_URL/api/notes</code> and{' '}
          <code className="settings-inline-code">$FASTLLM_BASE_URL/api/notes/append</code>. Enable Settings →
          Terminal's <strong>"Point local AI CLIs at fastllm"</strong> toggle first.
        </p>
      </div>
      <textarea
        className="editor-notes-textarea"
        value={content}
        onChange={handleChange}
        placeholder="Notes go here…"
        disabled={!loaded}
        spellCheck={false}
      />
    </div>
  )
}
