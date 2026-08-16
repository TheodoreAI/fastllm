import { useEffect, useState } from 'react'
import { useEscapeKey } from '../useEscapeKey'
import { FONT_OPTIONS } from '../useFontFamily'
import { FONT_SCALE_OPTIONS } from '../useFontScale'

// __APP_VERSION__ / __BUILD_TIME__ are baked in at build time by
// vite.config.js from package.json + the build clock — see there for why.
function formatBuildTime(iso) {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  })
}

export default function SettingsModal({
  theme,
  onThemeChange,
  fontFamily,
  onFontFamilyChange,
  fontScale,
  onFontScaleChange,
  settings,
  ragSettings,
  onSaveRagSettings,
  onClearKnowledgeBase,
  onClearConversations,
  onClose,
}) {
  const [ragForm, setRagForm] = useState(ragSettings)
  const [ragStatus, setRagStatus] = useState('')
  const [confirming, setConfirming] = useState(null) // 'kb' | 'conversations' | null
  const [dataStatus, setDataStatus] = useState('')

  useEffect(() => {
    setRagForm(ragSettings)
  }, [ragSettings])

  // Escape cancels an open danger-zone confirm first; a second press (or
  // pressing it when nothing's confirming) closes the whole modal.
  useEscapeKey(() => {
    if (confirming !== null) {
      setConfirming(null)
    } else {
      onClose()
    }
  })

  async function handleSaveRag(e) {
    e.preventDefault()
    setRagStatus('Saving…')
    try {
      await onSaveRagSettings({
        chunk_size: Number(ragForm.chunk_size),
        chunk_overlap: Number(ragForm.chunk_overlap),
        top_k: Number(ragForm.top_k),
      })
      setRagStatus('Saved. Applies to newly indexed documents and the next chat message.')
    } catch (err) {
      setRagStatus(`Error: ${err.message}`)
    }
  }

  async function runConfirmed(action) {
    setConfirming(null)
    setDataStatus('Clearing…')
    try {
      if (action === 'kb') await onClearKnowledgeBase()
      if (action === 'conversations') await onClearConversations()
      setDataStatus('Done.')
    } catch (err) {
      setDataStatus(`Error: ${err.message}`)
    }
  }

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal settings-panel" onClick={(e) => e.stopPropagation()}>
        <h3>Settings</h3>

        <div className="settings-section">
          <p className="settings-label">Appearance</p>
          <div className="theme-toggle">
            <button
              type="button"
              className={`theme-option ${theme === 'light' ? 'active' : ''}`}
              onClick={() => onThemeChange('light')}
            >
              Light
            </button>
            <button
              type="button"
              className={`theme-option ${theme === 'dark' ? 'active' : ''}`}
              onClick={() => onThemeChange('dark')}
            >
              Dark
            </button>
          </div>

          <label className="settings-field">
            <span>Font</span>
            <select
              className="model-select"
              value={fontFamily}
              onChange={(e) => onFontFamilyChange(e.target.value)}
            >
              {FONT_OPTIONS.map((group) => (
                <optgroup key={group.group} label={group.group}>
                  {group.choices.map((choice) => (
                    <option key={choice.label} value={choice.value}>
                      {choice.label}
                    </option>
                  ))}
                </optgroup>
              ))}
            </select>
          </label>

          <label className="settings-field">
            <span>Text size</span>
            <div className="theme-toggle">
              {FONT_SCALE_OPTIONS.map((opt) => (
                <button
                  key={opt.label}
                  type="button"
                  className={`theme-option ${fontScale === opt.value ? 'active' : ''}`}
                  onClick={() => onFontScaleChange(opt.value)}
                >
                  {opt.label}
                </button>
              ))}
            </div>
          </label>
        </div>

        <div className="settings-section">
          <p className="settings-label">Account</p>
          <div className="settings-row">
            <span className="settings-row-key">Signed in as</span>
            <span className="settings-row-value">{settings?.username ?? '—'}</span>
          </div>
        </div>

        <div className="settings-section">
          <p className="settings-label">LLM backend</p>
          <div className="settings-row">
            <span className="settings-row-key">Default chat model</span>
            <span className="settings-row-value" title={settings?.chat_model}>{settings?.chat_model ?? '—'}</span>
          </div>
          <div className="settings-row">
            <span className="settings-row-key">Embed model</span>
            <span className="settings-row-value" title={settings?.embed_model}>{settings?.embed_model ?? '—'}</span>
          </div>
          <div className="settings-row">
            <span className="settings-row-key">Endpoint</span>
            <span className="settings-row-value" title={settings?.llm_base_url}>{settings?.llm_base_url ?? '—'}</span>
          </div>
          <p className="settings-hint">
            The chat model used per-message is set in the sidebar's Model dropdown.
            This default only applies if a request doesn't specify one.
          </p>
        </div>

        <div className="settings-section">
          <p className="settings-label">Retrieval (RAG) tuning</p>
          {ragForm ? (
            <form onSubmit={handleSaveRag} className="rag-form">
              <label className="rag-field">
                <span>Chunk size (characters)</span>
                <input
                  type="number"
                  min={50}
                  value={ragForm.chunk_size}
                  onChange={(e) => setRagForm({ ...ragForm, chunk_size: e.target.value })}
                />
              </label>
              <label className="rag-field">
                <span>Chunk overlap</span>
                <input
                  type="number"
                  min={0}
                  value={ragForm.chunk_overlap}
                  onChange={(e) => setRagForm({ ...ragForm, chunk_overlap: e.target.value })}
                />
              </label>
              <label className="rag-field">
                <span>Chunks retrieved per question</span>
                <input
                  type="number"
                  min={1}
                  value={ragForm.top_k}
                  onChange={(e) => setRagForm({ ...ragForm, top_k: e.target.value })}
                />
              </label>
              <button type="submit" className="btn-primary">Save retrieval settings</button>
              {ragStatus && <p className="status">{ragStatus}</p>}
              <p className="settings-hint">
                Chunk size/overlap only affect documents indexed after saving — existing
                documents keep the chunks they were indexed with.
              </p>
            </form>
          ) : (
            <p className="settings-hint">Loading…</p>
          )}
        </div>

        <div className="settings-section">
          <p className="settings-label">Data management</p>

          {confirming === null && (
            <div className="danger-zone">
              <div className="danger-row">
                <span>Clear all conversations</span>
                <button type="button" className="btn-danger" onClick={() => setConfirming('conversations')}>
                  Clear…
                </button>
              </div>
              <div className="danger-row">
                <span>Clear knowledge base</span>
                <button type="button" className="btn-danger" onClick={() => setConfirming('kb')}>
                  Clear…
                </button>
              </div>
            </div>
          )}

          {confirming === 'conversations' && (
            <div className="danger-confirm">
              <p>Delete every conversation and message? This can't be undone.</p>
              <div className="modal-actions">
                <button type="button" className="btn-secondary" onClick={() => setConfirming(null)}>Cancel</button>
                <button type="button" className="btn-danger" onClick={() => runConfirmed('conversations')}>
                  Delete all conversations
                </button>
              </div>
            </div>
          )}

          {confirming === 'kb' && (
            <div className="danger-confirm">
              <p>Delete every indexed document and chunk? This can't be undone.</p>
              <div className="modal-actions">
                <button type="button" className="btn-secondary" onClick={() => setConfirming(null)}>Cancel</button>
                <button type="button" className="btn-danger" onClick={() => runConfirmed('kb')}>
                  Delete knowledge base
                </button>
              </div>
            </div>
          )}

          {dataStatus && <p className="status">{dataStatus}</p>}
        </div>

        <div className="settings-section">
          <p className="settings-label">About</p>
          <div className="settings-row">
            <span className="settings-row-key">Version</span>
            <span className="settings-row-value">{__APP_VERSION__}</span>
          </div>
          <div className="settings-row">
            <span className="settings-row-key">Built</span>
            <span className="settings-row-value" title={__BUILD_TIME__}>{formatBuildTime(__BUILD_TIME__)}</span>
          </div>
        </div>

        <div className="modal-actions">
          <button type="button" className="btn-secondary" onClick={onClose}>Close</button>
        </div>
      </div>
    </div>
  )
}
