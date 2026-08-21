import TerminalView from './TerminalView'

// A prompt glyph (">_") rather than a generic rectangle — reads as
// "terminal" at a glance the same way the view-rail's Files/Search/Git
// icons do, matching their stroke-based style (viewBox 24, strokeWidth
// 1.8) even though this one renders much smaller.
function TerminalTabIcon() {
  return (
    <svg
      className="terminal-tab-icon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="m7 8 4 4-4 4" />
      <path d="M13 16h4" />
    </svg>
  )
}

// The Terminal pane as a full-height column, a peer of Editor and Chat
// (see App.jsx's usePaneSlots) rather than a bottom-docked strip nested
// inside the editor column — all of its tab state lives in App.jsx now,
// since a top-level pane needs to survive being reordered independent of
// EditorView's own mount/unmount.
export default function TerminalPane({
  theme,
  folderRoot,
  terminalEnabled,
  terminalTabs,
  activeTerminalTab,
  onSelectTab,
  onAddTab,
  onCloseTab,
}) {
  return (
    <div className="terminal-pane">
      {/* No "Terminal" text here — the DraggableSection titlebar wrapping
          this whole pane already says "Terminal" (see App.jsx's
          PANE_LABELS), so each tab is just a prompt icon, with a number
          alongside it only once there's more than one tab to tell apart.
          When the terminal's disabled there's nothing to pick between, so
          the header is skipped entirely rather than repeating that same
          word a third time. */}
      {terminalEnabled && (
        <div className="terminal-panel-header">
          <div className="terminal-tabs">
            {terminalTabs.map((id, index) => (
              <div key={id} className={`terminal-tab ${id === activeTerminalTab ? 'is-active' : ''}`}>
                <button type="button" className="terminal-tab-label" onClick={() => onSelectTab(id)} title={`Terminal ${index + 1}`}>
                  <TerminalTabIcon />
                  {terminalTabs.length > 1 && <span>{index + 1}</span>}
                </button>
                <button
                  type="button"
                  className="terminal-tab-close"
                  title="Close terminal"
                  onClick={() => onCloseTab(id)}
                >
                  ✕
                </button>
              </div>
            ))}
            <button type="button" className="terminal-tab-add" title="New terminal" onClick={onAddTab}>
              +
            </button>
          </div>
        </div>
      )}
      <div className="terminal-panel-body">
        {terminalEnabled ? (
          terminalTabs.map((id) => (
            <div key={id} style={{ display: id === activeTerminalTab ? 'contents' : 'none' }}>
              <TerminalView theme={theme} folderRoot={folderRoot} />
            </div>
          ))
        ) : (
          <div className="terminal-disabled-state">
            <p>Terminal is disabled. Enable it in Settings → Terminal.</p>
          </div>
        )}
      </div>
    </div>
  )
}
