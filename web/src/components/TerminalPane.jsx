import TerminalView from './TerminalView'

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
      <div className="terminal-panel-header">
        {terminalEnabled ? (
          <div className="terminal-tabs">
            {terminalTabs.map((id, index) => (
              <div key={id} className={`terminal-tab ${id === activeTerminalTab ? 'is-active' : ''}`}>
                <button type="button" className="terminal-tab-label" onClick={() => onSelectTab(id)}>
                  Terminal {index + 1}
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
        ) : (
          <span className="terminal-panel-title">Terminal</span>
        )}
      </div>
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
