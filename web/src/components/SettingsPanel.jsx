import { useEffect, useRef, useState } from 'react'
import { FONT_OPTIONS } from '../useFontFamily'
import { FONT_SCALE_OPTIONS } from '../useFontScale'
import { THEMES } from '../themes'
import { browseForFolder, isWails } from '../api'
import { useEscapeKey } from '../useEscapeKey'
import { useSettingsExpanded } from '../useSettingsExpanded'
import ConfirmDeleteModal from './ConfirmDeleteModal'

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

const CHEVRON = (
  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M9 6l6 6-6 6" />
  </svg>
)

// Every global keyboard shortcut fastllm defines, shown together in
// Settings → About so there's one place to look them up — the panel
// toggles and Save (native: false) are handled in this web codebase
// (App.jsx's keydown listener, EditorView's own handler) and work in any
// build. Open Folder/Exit/Undo/Redo (native: true) are native Wails menu
// items (see cmd/desktop/main.go's menu()) with no corresponding keydown
// handler anywhere in web/src — they only work in the packaged desktop
// app, not a plain browser tab, so they're flagged here rather than
// listed as if they were universal. None of these three places (App.jsx,
// EditorView.jsx, cmd/desktop/main.go) know about the other two, so this
// list has to be kept in sync by hand rather than generated from one
// source.
const KEYBINDINGS = [
  { keys: 'Ctrl+B', description: 'Toggle the editor’s Files panel', native: false },
  { keys: 'Ctrl+J', description: 'Toggle the terminal panel', native: false },
  { keys: 'Ctrl+Shift+M', description: 'Toggle the model settings panel', native: false },
  { keys: 'Ctrl+S', description: 'Save the current file (Editor tab)', native: false },
  { keys: 'Ctrl+O', description: 'Open folder', native: true },
  { keys: 'Ctrl+Q', description: 'Exit fastllm', native: true },
  { keys: 'Ctrl+Z', description: 'Undo', native: true },
  { keys: 'Ctrl+Y', description: 'Redo', native: true },
  { keys: 'Ctrl+X', description: 'Cut', native: true },
  { keys: 'Ctrl+C', description: 'Copy', native: true },
  { keys: 'Ctrl+V', description: 'Paste', native: true },
  { keys: 'Ctrl+A', description: 'Select all', native: true },
  { keys: 'Ctrl+Shift+C', description: 'Copy selection (in the Terminal panel)', native: false },
  { keys: 'Ctrl+Shift+V', description: 'Paste (in the Terminal panel)', native: false },
]

// One collapsible sub-block within the Settings panel — mirrors
// FileTree.jsx's folder-row chevron-rotate idiom (same interaction,
// applied to a settings heading instead of a directory) rather than
// inventing a new expand/collapse affordance.
function SubSection({ id, label, expanded, onToggle, children }) {
  return (
    <div className="settings-section">
      <button
        type="button"
        className="settings-subsection-toggle"
        onClick={() => onToggle(id)}
        aria-expanded={expanded}
      >
        <span className={`settings-subsection-chevron ${expanded ? 'is-open' : ''}`}>{CHEVRON}</span>
        <span className="settings-label">{label}</span>
      </button>
      {expanded && <div className="settings-subsection-body">{children}</div>}
    </div>
  )
}

// Settings, rendered as a floating panel with no dimming backdrop — unlike
// a normal modal, the rest of the app stays fully interactive underneath
// (e.g. you can enable file access here, then click straight into the
// Editor without closing this panel first). Closes via an explicit X,
// Escape, or clicking outside the panel (a document-level listener, since
// there's no full-screen overlay to catch that click).
export default function SettingsPanel({
  theme,
  onThemeChange,
  fontFamily,
  onFontFamilyChange,
  fontScale,
  onFontScaleChange,
  settings,
  ragSettings,
  onSaveRagSettings,
  fileAccessSettings,
  onSaveFileAccessSettings,
  terminalSettings,
  onSaveTerminalSettings,
  cloudProviderSettings,
  onSaveCloudProviderSettings,
  thinkLevel,
  onThinkLevelChange,
  onClearKnowledgeBase,
  onClearConversations,
  expandSection,
  onClose,
}) {
  const panelRef = useRef(null)

  useEscapeKey(onClose)

  useEffect(() => {
    function handlePointerDown(e) {
      if (panelRef.current && !panelRef.current.contains(e.target)) onClose()
    }
    document.addEventListener('mousedown', handlePointerDown)
    return () => document.removeEventListener('mousedown', handlePointerDown)
  }, [onClose])
  const [ragForm, setRagForm] = useState(ragSettings)
  const [ragStatus, setRagStatus] = useState('')
  const [fileAccessForm, setFileAccessForm] = useState(fileAccessSettings)
  const [fileAccessStatus, setFileAccessStatus] = useState('')
  const [terminalForm, setTerminalForm] = useState(terminalSettings)
  const [terminalStatus, setTerminalStatus] = useState('')
  // Keyed by provider id ('anthropic' | 'openai' | 'gemini') -> the new
  // key text the user has typed. A provider absent from this object was
  // never touched this session, so saving omits its field entirely and
  // whatever key (if any) is already stored server-side is left alone —
  // see UpdateCloudProviderSettings's doc comment for why that distinction
  // matters. The server never sends real key values back (only whether
  // one is configured), so there is no "current key" to prefill here.
  const [cloudProviderForm, setCloudProviderForm] = useState({})
  const [cloudProviderStatus, setCloudProviderStatus] = useState('')
  const [confirming, setConfirming] = useState(null) // 'kb' | 'conversations' | null
  const [dataStatus, setDataStatus] = useState('')
  const [browsing, setBrowsing] = useState(false)

  // Persisted across sessions (see useSettingsExpanded) — Appearance
  // starts open on a first-ever run since it's the sub-section most
  // people touch first, but a returning user's expand/collapse choices
  // now stick instead of resetting every time Settings reopens.
  const [expanded, setExpanded] = useSettingsExpanded()

  function toggleSection(id) {
    setExpanded((prev) => ({ ...prev, [id]: !prev[id] }))
  }

  // Lets a cross-link elsewhere in the app (ModelPicker's "manage
  // models") force one specific sub-section open — e.g. jumping to LLM
  // backend — without the panel otherwise needing to be a controlled
  // component. See App.jsx's onOpenSettings.
  useEffect(() => {
    if (expandSection) {
      setExpanded((prev) => ({ ...prev, [expandSection]: true }))
    }
  }, [expandSection, setExpanded])

  // Drives disabling the read/write checkboxes and Save button before an
  // invalid (checked-but-no-root) state can even be reached, rather than
  // only rejecting it after the fact — see handleSaveFileAccess's
  // pre-flight check below for the belt-and-suspenders case where root
  // gets cleared again after a box was already checked.
  const fileAccessRootEmpty = !fileAccessForm.root?.trim()

  useEffect(() => {
    setRagForm(ragSettings)
  }, [ragSettings])

  useEffect(() => {
    setFileAccessForm(fileAccessSettings)
  }, [fileAccessSettings])

  useEffect(() => {
    setTerminalForm(terminalSettings)
  }, [terminalSettings])

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
      setRagStatus(`Couldn't save retrieval settings: ${err.message}`)
    }
  }

  // Opens a native OS folder dialog on the server's own machine/desktop —
  // unlike a browser <input type=file webkitdirectory>, this returns a
  // real absolute filesystem path instead of just a folder-name hint,
  // since fastllm's server and its browser tab run on the same machine.
  async function handleBrowseForFolder() {
    setBrowsing(true)
    setFileAccessStatus('')
    try {
      const result = await browseForFolder()
      if (result.cancelled) return
      setFileAccessForm((prev) => ({ ...prev, root: result.path }))
    } catch (err) {
      setFileAccessStatus(`Couldn't open the folder picker: ${err.message}`)
    } finally {
      setBrowsing(false)
    }
  }

  async function handleSaveFileAccess(e) {
    e.preventDefault()
    setFileAccessStatus('Saving…')
    try {
      const next = {
        root: fileAccessForm.root?.trim() ?? '',
        read_enabled: !!fileAccessForm.read_enabled,
        write_enabled: !!fileAccessForm.write_enabled,
      }
      if (next.write_enabled && !next.read_enabled) {
        throw new Error('Write access requires read access to stay enabled.')
      }
      // Catch this before it ever reaches the server — the backend
      // rejects the same condition, but only with a terse "root is
      // required" message that doesn't say what to do about it. The
      // checkboxes are also disabled while root is empty (see below), so
      // this mainly guards someone re-clearing the field after checking
      // a box.
      if ((next.read_enabled || next.write_enabled) && !next.root) {
        throw new Error('Choose a folder or type a path before enabling read/write access.')
      }
      await onSaveFileAccessSettings(next)
      setFileAccessStatus('Saved. File access updates apply immediately without a server restart.')
    } catch (err) {
      setFileAccessStatus(`Couldn't save file access: ${err.message}`)
    }
  }

  async function handleSaveTerminal(e) {
    e.preventDefault()
    setTerminalStatus('Saving…')
    try {
      const next = { enabled: !!terminalForm.enabled }
      await onSaveTerminalSettings(next)
      setTerminalStatus('Saved. Terminal access updates apply immediately without a server restart.')
    } catch (err) {
      setTerminalStatus(`Couldn't save terminal access: ${err.message}`)
    }
  }

  // Only sends a field for a provider the user actually typed into this
  // session (see cloudProviderForm's declaration above) — an untouched
  // provider's key, if any, stays exactly as already stored.
  async function handleSaveCloudProviders(e) {
    e.preventDefault()
    setCloudProviderStatus('Saving…')
    try {
      const next = {}
      if ('anthropic' in cloudProviderForm) next.anthropic_api_key = cloudProviderForm.anthropic
      if ('openai' in cloudProviderForm) next.openai_api_key = cloudProviderForm.openai
      if ('gemini' in cloudProviderForm) next.gemini_api_key = cloudProviderForm.gemini
      await onSaveCloudProviderSettings(next)
      setCloudProviderForm({})
      setCloudProviderStatus('Saved. Newly added models appear in the Model list immediately.')
    } catch (err) {
      setCloudProviderStatus(`Couldn't save cloud provider settings: ${err.message}`)
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
      setDataStatus(`Couldn't clear ${action === 'kb' ? 'the knowledge base' : 'conversations'}: ${err.message}`)
    }
  }

  return (
    <div className="settings-floating-panel" ref={panelRef}>
      <div className="settings-floating-header">
        <h2>Settings</h2>
        <button type="button" className="settings-floating-close" title="Close" onClick={onClose}>
          ✕
        </button>
      </div>

      <div className="settings-floating-body">

      <SubSection id="appearance" label="Appearance" expanded={!!expanded.appearance} onToggle={toggleSection}>
        <div className="theme-toggle">
          {THEMES.map((t) => (
            <button
              key={t.id}
              type="button"
              className={`theme-option ${theme === t.id ? 'active' : ''}`}
              onClick={() => onThemeChange(t.id)}
            >
              {t.label}
            </button>
          ))}
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
      </SubSection>

      <SubSection id="account" label="Account" expanded={!!expanded.account} onToggle={toggleSection}>
        <div className="settings-row">
          <span className="settings-row-key">Signed in as</span>
          <span className="settings-row-value">{settings?.username ?? '—'}</span>
        </div>
      </SubSection>

      <SubSection id="thinking" label="Thinking" expanded={!!expanded.thinking} onToggle={toggleSection}>
        <label className="settings-field">
          <span>Reasoning effort</span>
          {/* These three values must match normalizeThinkLevel's accepted
              set in internal/chat/handler.go — nothing derives one list
              from the other, so an addition/rename needs both edits. */}
          <select className="model-select" value={thinkLevel || 'medium'} onChange={(e) => onThinkLevelChange(e.target.value)}>
            <option value="low">Low</option>
            <option value="medium">Medium</option>
            <option value="high">High</option>
          </select>
        </label>
        <p className="settings-hint">
          Only models that advertise native thinking support will honor this. Other models will ignore it harmlessly.
        </p>
      </SubSection>

      <SubSection id="fileAccess" label="File access" expanded={!!expanded.fileAccess} onToggle={toggleSection}>
        <form onSubmit={handleSaveFileAccess} className="rag-form">
          <label className="settings-field">
            <span>Project root</span>
            <div className="file-access-root-row">
              <input
                type="text"
                value={fileAccessForm.root ?? ''}
                placeholder="Leave blank to disable"
                onChange={(e) => {
                  const root = e.target.value
                  // Clearing the root also clears read/write, so the
                  // visible state is never "checked but disabled" —
                  // matches this field's own "Leave blank to disable"
                  // placeholder text literally.
                  const cleared = !root.trim()
                  setFileAccessForm((prev) => ({
                    ...prev,
                    root,
                    read_enabled: cleared ? false : prev.read_enabled,
                    write_enabled: cleared ? false : prev.write_enabled,
                  }))
                }}
              />
              <button type="button" className="btn-secondary" onClick={handleBrowseForFolder} disabled={browsing}>
                {browsing ? 'Waiting for dialog…' : 'Choose folder'}
              </button>
            </div>
          </label>
          <p className="settings-hint">
            "Choose folder" opens a native folder picker on this machine (fastllm's server and browser tab run on the same computer), so the path it fills in is real and ready to use. You can also type or paste a path directly.
          </p>
          <label
            className="settings-field checkbox-field settings-check-row"
            title={fileAccessRootEmpty ? 'Choose a folder or type a path first' : ''}
          >
            <input
              type="checkbox"
              checked={!!fileAccessForm.read_enabled}
              disabled={fileAccessRootEmpty}
              onChange={(e) => setFileAccessForm({ ...fileAccessForm, read_enabled: e.target.checked, write_enabled: e.target.checked ? fileAccessForm.write_enabled : false })}
            />
            <span>Allow reading files</span>
          </label>
          <label
            className="settings-field checkbox-field settings-check-row"
            title={fileAccessRootEmpty ? 'Choose a folder or type a path first' : ''}
          >
            <input
              type="checkbox"
              checked={!!fileAccessForm.write_enabled}
              disabled={fileAccessRootEmpty || !fileAccessForm.read_enabled}
              onChange={(e) => setFileAccessForm({ ...fileAccessForm, write_enabled: e.target.checked })}
            />
            <span>Allow writing files</span>
          </label>
          <button
            type="submit"
            className="btn-primary"
            disabled={(!!fileAccessForm.read_enabled || !!fileAccessForm.write_enabled) && fileAccessRootEmpty}
          >
            Save file access
          </button>
          {fileAccessStatus && (
            <p className={`status ${fileAccessStatus.startsWith("Couldn't") ? 'status-error' : ''}`}>{fileAccessStatus}</p>
          )}
        </form>
      </SubSection>

      <SubSection id="terminal" label="Terminal" expanded={!!expanded.terminal} onToggle={toggleSection}>
        <form onSubmit={handleSaveTerminal} className="rag-form">
          <label className="settings-field checkbox-field settings-check-row">
            <input
              type="checkbox"
              checked={!!terminalForm.enabled}
              onChange={(e) => setTerminalForm({ ...terminalForm, enabled: e.target.checked })}
            />
            <span>Enable interactive terminal</span>
          </label>
          <p className="settings-hint">
            Gives the Editor panel a real PowerShell session on this machine. Unlike file access, this isn't sandboxed — anything the terminal can run, it runs with full access as whatever account runs fastllm. Only enable this if you trust everyone who can reach this app.
          </p>
          <button type="submit" className="btn-primary">Save terminal access</button>
          {terminalStatus && (
            <p className={`status ${terminalStatus.startsWith("Couldn't") ? 'status-error' : ''}`}>{terminalStatus}</p>
          )}
        </form>
      </SubSection>

      <SubSection id="cloudProviders" label="Cloud providers" expanded={!!expanded.cloudProviders} onToggle={toggleSection}>
        <form onSubmit={handleSaveCloudProviders} className="rag-form">
          <p className="settings-hint">
            Add your own API key for a cloud provider to use its models from the Model picker alongside your local models. Keys are stored on this machine and sent only to that provider. Gemini and OpenAI models support file read/write tools when file access is enabled below; Anthropic (Claude) models don't yet.
          </p>
          {[
            { id: 'anthropic', label: 'Anthropic (Claude)', configured: cloudProviderSettings?.anthropic_configured },
            { id: 'openai', label: 'OpenAI (ChatGPT)', configured: cloudProviderSettings?.openai_configured },
            { id: 'gemini', label: 'Google (Gemini)', configured: cloudProviderSettings?.gemini_configured },
          ].map(({ id, label, configured }) => (
            <label className="settings-field" key={id}>
              <span>{label}{configured && !(id in cloudProviderForm) && <span className="settings-hint-inline"> (key saved)</span>}</span>
              <input
                type="password"
                autoComplete="off"
                value={cloudProviderForm[id] ?? ''}
                placeholder={configured ? 'Enter a new key to replace the saved one' : 'API key'}
                onChange={(e) => setCloudProviderForm((prev) => ({ ...prev, [id]: e.target.value }))}
              />
            </label>
          ))}
          <button type="submit" className="btn-primary" disabled={Object.keys(cloudProviderForm).length === 0}>
            Save cloud providers
          </button>
          {cloudProviderStatus && (
            <p className={`status ${cloudProviderStatus.startsWith("Couldn't") ? 'status-error' : ''}`}>{cloudProviderStatus}</p>
          )}
        </form>
      </SubSection>

      <SubSection id="llmBackend" label="LLM backend" expanded={!!expanded.llmBackend} onToggle={toggleSection}>
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
      </SubSection>

      <SubSection id="rag" label="Retrieval (RAG) tuning" expanded={!!expanded.rag} onToggle={toggleSection}>
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
            {ragStatus && (
              <p className={`status ${ragStatus.startsWith("Couldn't") ? 'status-error' : ''}`}>{ragStatus}</p>
            )}
            <p className="settings-hint">
              Chunk size/overlap only affect documents indexed after saving — existing
              documents keep the chunks they were indexed with.
            </p>
          </form>
        ) : (
          <p className="settings-hint">Loading…</p>
        )}
      </SubSection>

      <SubSection id="data" label="Data management" expanded={!!expanded.data} onToggle={toggleSection}>
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

        {dataStatus && (
          <p className={`status ${dataStatus.startsWith("Couldn't") ? 'status-error' : ''}`}>{dataStatus}</p>
        )}
      </SubSection>

      <SubSection id="about" label="About" expanded={!!expanded.about} onToggle={toggleSection}>
        <div className="settings-row">
          <span className="settings-row-key">Version</span>
          <span className="settings-row-value">{__APP_VERSION__}</span>
        </div>
        <div className="settings-row">
          <span className="settings-row-key">Built</span>
          <span className="settings-row-value" title={__BUILD_TIME__}>{formatBuildTime(__BUILD_TIME__)}</span>
        </div>

        <p className="settings-label settings-subheading">Keyboard shortcuts</p>
        {KEYBINDINGS.filter((b) => !b.native || isWails()).map(({ keys, description, native }) => (
          <div className="settings-row" key={keys}>
            <span className="settings-row-keybind">
              {keys.split('+').map((k) => <kbd key={k}>{k}</kbd>)}
            </span>
            <span className="settings-row-value settings-row-value-wrap">
              {description}
              {native && <span className="settings-hint-inline"> (desktop app)</span>}
            </span>
          </div>
        ))}
      </SubSection>

      </div>

      {confirming === 'conversations' && (
        <ConfirmDeleteModal
          heading="Clear all conversations?"
          description="Delete every conversation and message. This can't be undone."
          confirmLabel="Delete all conversations"
          onCancel={() => setConfirming(null)}
          onConfirm={() => runConfirmed('conversations')}
        />
      )}

      {confirming === 'kb' && (
        <ConfirmDeleteModal
          heading="Clear the knowledge base?"
          description="Delete every indexed document and chunk. This can't be undone."
          confirmLabel="Delete knowledge base"
          onCancel={() => setConfirming(null)}
          onConfirm={() => runConfirmed('kb')}
        />
      )}
    </div>
  )
}
