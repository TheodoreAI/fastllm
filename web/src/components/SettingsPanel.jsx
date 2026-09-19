import { useEffect, useRef, useState } from 'react'
import { FONT_OPTIONS } from '../useFontFamily'
import { FONT_SCALE_OPTIONS } from '../useFontScale'
import { THEMES } from '../themes'
import { browseForFolder, isWails } from '../api'
import { useEscapeKey } from '../useEscapeKey'
import { useSettingsExpanded } from '../useSettingsExpanded'
import ConfirmDeleteModal from './ConfirmDeleteModal'
import GroupedDropdown from './GroupedDropdown'

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
  { keys: 'Ctrl+Shift+M', description: 'Toggle the model settings panel', native: false },
  { keys: 'Ctrl+O', description: 'Open folder', native: true },
  { keys: 'Ctrl+Q', description: 'Exit fastllm', native: true },
  { keys: 'Ctrl+Z', description: 'Undo', native: true },
  { keys: 'Ctrl+Y', description: 'Redo', native: true },
  { keys: 'Ctrl+X', description: 'Cut', native: true },
  { keys: 'Ctrl+C', description: 'Copy', native: true },
  { keys: 'Ctrl+V', description: 'Paste', native: true },
  { keys: 'Ctrl+A', description: 'Select all', native: true },
]

// One collapsible sub-block within the Settings panel — mirrors
// FileTree.jsx's folder-row chevron-rotate idiom (same interaction,
// applied to a settings heading instead of a directory) rather than
// inventing a new expand/collapse affordance.
function SubSection({ id, label, expanded, onToggle, children }) {
  return (
    // Real DOM id (not just the `expanded` state key) so a cross-link like
    // the Editor's "Enable the terminal" checklist item can scroll this
    // exact section into view once it's open — see the expandSection
    // effect below.
    <div className="settings-section" id={`settings-section-${id}`}>
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
  fileAccessSettings,
  onSaveFileAccessSettings,
  cloudProviderSettings,
  onSaveCloudProviderSettings,
  thinkLevel,
  onThinkLevelChange,
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
  const [fileAccessForm, setFileAccessForm] = useState(fileAccessSettings)
  const [fileAccessStatus, setFileAccessStatus] = useState('')
  const [cloudProviderForm, setCloudProviderForm] = useState({})
  const [cloudProviderStatus, setCloudProviderStatus] = useState('')
  const [confirming, setConfirming] = useState(null) // 'conversations' | null
  const [dataStatus, setDataStatus] = useState('')
  const [browsing, setBrowsing] = useState(false)

  const [expanded, setExpanded] = useSettingsExpanded()

  function toggleSection(id) {
    setExpanded((prev) => ({ ...prev, [id]: !prev[id] }))
  }

  useEffect(() => {
    if (!expandSection) return
    setExpanded((prev) => ({ ...prev, [expandSection]: true }))
    requestAnimationFrame(() => {
      document.getElementById(`settings-section-${expandSection}`)?.scrollIntoView({ block: 'start', behavior: 'smooth' })
    })
  }, [expandSection, setExpanded])

  const fileAccessRootEmpty = !fileAccessForm.root?.trim()

  useEffect(() => {
    setFileAccessForm(fileAccessSettings)
  }, [fileAccessSettings])

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
      if ('nvidia' in cloudProviderForm) next.nvidia_api_key = cloudProviderForm.nvidia
      if ('cloudflare' in cloudProviderForm) next.cloudflare_api_key = cloudProviderForm.cloudflare
      if ('cloudflare_account_id' in cloudProviderForm) next.cloudflare_account_id = cloudProviderForm.cloudflare_account_id
      if ('osu' in cloudProviderForm) next.osu_api_key = cloudProviderForm.osu
      if ('osu_base_url' in cloudProviderForm) next.osu_base_url = cloudProviderForm.osu_base_url
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
          <GroupedDropdown
            groups={FONT_OPTIONS.map((group) => ({
              key: group.group,
              label: group.group,
              options: group.choices.map((choice) => ({ value: choice.value, label: choice.label })),
            }))}
            value={fontFamily}
            onChange={onFontFamilyChange}
            placeholder="Select a font"
          />
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

      <SubSection id="cloudProviders" label="Cloud providers" expanded={!!expanded.cloudProviders} onToggle={toggleSection}>
        <form onSubmit={handleSaveCloudProviders} className="rag-form">
          <p className="settings-hint">
            Add your own API key for a cloud provider or OSU cluster to use its models from the Model picker alongside your local models. For OSU Cluster (Muse Glimmer 30B), run &apos;osu-llm up muse&apos; to start the SSH tunnel on port 8010.
          </p>
          {[
            { id: 'anthropic', label: 'Anthropic (Claude)', configured: cloudProviderSettings?.anthropic_configured },
            { id: 'openai', label: 'OpenAI (ChatGPT)', configured: cloudProviderSettings?.openai_configured },
            { id: 'gemini', label: 'Google (Gemini)', configured: cloudProviderSettings?.gemini_configured },
            { id: 'nvidia', label: 'NVIDIA Build', configured: cloudProviderSettings?.nvidia_configured },
            { id: 'osu', label: 'OSU Cluster (vLLM / Muse Glimmer)', configured: cloudProviderSettings?.osu_configured },
          ].map(({ id, label, configured }) => (
            <label className="settings-field" key={id}>
              <span>{label}{configured && !(id in cloudProviderForm) && <span className="settings-hint-inline"> (key saved)</span>}</span>
              <input
                type="password"
                autoComplete="off"
                value={cloudProviderForm[id] ?? ''}
                placeholder={configured ? 'Enter a new key to replace the saved one' : (id === 'osu' ? 'API key (auto-loaded from ~/.osu-llm/vllm-api-key if left empty)' : 'API key')}
                onChange={(e) => setCloudProviderForm((prev) => ({ ...prev, [id]: e.target.value }))}
              />
            </label>
          ))}
          <label className="settings-field">
            <span>
              OSU Cluster endpoint
              {cloudProviderSettings?.osu_base_url && !('osu_base_url' in cloudProviderForm) && (
                <span className="settings-hint-inline"> ({cloudProviderSettings.osu_base_url})</span>
              )}
            </span>
            <input
              type="text"
              autoComplete="off"
              value={cloudProviderForm.osu_base_url ?? ''}
              placeholder={cloudProviderSettings?.osu_base_url || 'http://127.0.0.1:8010/v1 (tunnel port)'}
              onChange={(e) => setCloudProviderForm((prev) => ({ ...prev, osu_base_url: e.target.value }))}
            />
          </label>
          {/* Cloudflare Workers AI needs both an API token AND an account
              ID to build a working client — its endpoint is scoped under
              /accounts/{id}/ai/v1/... rather than identified by the token
              alone (see llm.Router.SetCloudProviders), so it gets a second
              input the other providers above don't need. */}
          <label className="settings-field">
            <span>
              Cloudflare Workers AI (token)
              {cloudProviderSettings?.cloudflare_configured && !('cloudflare' in cloudProviderForm) && (
                <span className="settings-hint-inline"> (key saved)</span>
              )}
            </span>
            <input
              type="password"
              autoComplete="off"
              value={cloudProviderForm.cloudflare ?? ''}
              placeholder={cloudProviderSettings?.cloudflare_configured ? 'Enter a new token to replace the saved one' : 'API token'}
              onChange={(e) => setCloudProviderForm((prev) => ({ ...prev, cloudflare: e.target.value }))}
            />
          </label>
          <label className="settings-field">
            <span>Cloudflare account ID</span>
            <input
              type="text"
              autoComplete="off"
              value={cloudProviderForm.cloudflare_account_id ?? ''}
              placeholder="Found in the Cloudflare dashboard sidebar"
              onChange={(e) => setCloudProviderForm((prev) => ({ ...prev, cloudflare_account_id: e.target.value }))}
            />
          </label>
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

      <SubSection id="data" label="Data management" expanded={!!expanded.data} onToggle={toggleSection}>
        <div className="danger-zone">
          <div className="danger-row">
            <span>Clear all conversations</span>
            <button type="button" className="btn-danger" onClick={() => setConfirming('conversations')}>
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
    </div>
  )
}
