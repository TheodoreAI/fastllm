export default function SettingsModal({ theme, onThemeChange, settings, onClose }) {
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

        <div className="modal-actions">
          <button type="button" className="btn-secondary" onClick={onClose}>Close</button>
        </div>
      </div>
    </div>
  )
}
