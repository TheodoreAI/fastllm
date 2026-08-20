import CodeMirror from '@uiw/react-codemirror'
import { githubLight, githubDark } from '@uiw/codemirror-theme-github'

// The main editor column's tab strip, file header, CodeMirror instances,
// diff view, and empty state. Pulled out of EditorView for readability —
// codeMirrorViewsByPath stays a ref owned by EditorView (also written by
// loadFileIntoTab/handleSave/closeTab/removeTabNoConfirm/handleRenameFile
// there), so it's threaded through as a prop rather than duplicated here.
export default function EditorPane({
  openTabs,
  activeTabPath,
  diffPath,
  onSelectTab,
  onCloseTab,
  openPath,
  fileStatus,
  canWrite,
  dirty,
  saving,
  onSave,
  theme,
  languageExtensionsFor,
  onEditorKeyDown,
  onTabContentChange,
  codeMirrorViewsByPath,
  diffLines,
  onCloseDiff,
  setupComplete,
  setupChecklist,
}) {
  return (
    <div className="editor-main-content">
      {openTabs.length > 0 && (
        <div className="editor-tabs">
          {openTabs.map((t) => {
            const tabDirty = t.content !== t.savedContent
            return (
              <div
                key={t.path}
                className={`editor-tab ${t.path === activeTabPath && !diffPath ? 'is-active' : ''}`}
              >
                <button
                  type="button"
                  className="editor-tab-label"
                  title={t.path}
                  onClick={() => onSelectTab(t.path)}
                >
                  {t.path.split(/[/\\]/).pop()}
                  {tabDirty && <span className="editor-tab-dirty">●</span>}
                </button>
                <button
                  type="button"
                  className="editor-tab-close"
                  title="Close"
                  onClick={() => onCloseTab(t.path)}
                >
                  ✕
                </button>
              </div>
            )
          })}
        </div>
      )}
      {openTabs.length > 0 && !diffPath && (
        <div className="editor-file-header">
          <span className="editor-file-path">{openPath}</span>
          {fileStatus && <span className="editor-file-status">{fileStatus}</span>}
          {!canWrite && (
            <span className="editor-readonly-badge" title="File writes are disabled in Settings → File access">
              Read-only
            </span>
          )}
          <button
            type="button"
            className={`editor-save-button${saving ? ' is-saving' : ''}`}
            onClick={onSave}
            disabled={!canWrite || !dirty || saving}
            title={canWrite ? '' : 'File writes are disabled in Settings → File access'}
          >
            {saving ? 'Saving…' : dirty ? 'Save' : 'Saved'}
          </button>
        </div>
      )}
      {/* Every open tab keeps its own CodeMirror instance mounted (just
          hidden) at all times, the same always-mounted-but-hidden pattern
          the Terminal tabs use — switching tabs must not tear down and
          rebuild the editor, or undo history/scroll position/selection
          would all reset on every switch. */}
      {openTabs.map((t) => (
        <div
          key={t.path}
          className="editor-codemirror"
          style={{ display: t.path === activeTabPath && !diffPath ? 'block' : 'none' }}
          onKeyDown={onEditorKeyDown}
        >
          <CodeMirror
            value={t.content}
            height="100%"
            theme={theme === 'light' ? githubLight : githubDark}
            extensions={languageExtensionsFor(t.path)}
            onChange={(value) => onTabContentChange(t.path, value)}
            onCreateEditor={(view) => {
              codeMirrorViewsByPath.current[t.path] = view
            }}
            readOnly={!canWrite}
          />
        </div>
      ))}
      {diffPath ? (
        <>
          <div className="editor-file-header">
            <span className="editor-file-path">Diff: {diffPath}</span>
            <button type="button" className="editor-diff-close" onClick={onCloseDiff}>Close</button>
          </div>
          <div className="editor-diff">
            {diffLines.map((line, i) => (
              <div key={i} className={`editor-diff-line editor-diff-line--${line.type}`}>
                <span className="editor-diff-gutter">
                  {line.type === 'add' ? '+' : line.type === 'del' ? '−' : ''}
                </span>
                <span className="editor-diff-text">{line.text}</span>
              </div>
            ))}
          </div>
        </>
      ) : openTabs.length === 0 ? (
        <div className="editor-empty-state">
          {setupComplete ? <p>Select a file to open it.</p> : setupChecklist}
        </div>
      ) : null}
    </div>
  )
}
