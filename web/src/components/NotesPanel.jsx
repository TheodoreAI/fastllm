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
      <p className="editor-hint">
        A terminal AI CLI can read and append here via{' '}
        <code className="settings-inline-code">$FASTLLM_BASE_URL/api/notes</code> and{' '}
        <code className="settings-inline-code">$FASTLLM_BASE_URL/api/notes/append</code>{' '}
        (enable Settings → Terminal's "Point local AI CLIs at fastllm" toggle first).
      </p>
      <textarea
        className="editor-notes-textarea"
        value={content}
        onChange={handleChange}
        placeholder="Notes go here…"
        disabled={!loaded}
        spellCheck={false}
      />
      <div className="editor-notes-status">{status === 'saving' ? 'Saving…' : status === 'saved' ? 'Saved' : ''}</div>
    </div>
  )
}
