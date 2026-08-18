import { useEffect, useMemo, useRef, useState } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { githubLight, githubDark } from '@uiw/codemirror-theme-github'
import {
  fetchEditorTree,
  fetchEditorFile,
  saveEditorFile,
  createEditorFile,
  deleteEditorFile,
  renameEditorFile,
  searchEditor,
  fetchGitStatus,
  fetchGitDiff,
  stageGitPaths,
  unstageGitPaths,
  commitGit,
  pushGit,
  fetchGitBranches,
  switchGitBranch,
  createGitBranch,
  browseForFolder,
  saveFileAccessSettings,
} from '../api'
import { languageExtensionFor } from '../editorLanguages'
import { useEditorSidebarWidth } from '../useEditorSidebarWidth'
import { useTerminalPanelHeight } from '../useTerminalPanelHeight'
import FileTree from './FileTree'
import TerminalView from './TerminalView'

export default function EditorView({
  fileAccessSettings,
  onFileAccessSettingsChange,
  theme,
  terminalEnabled,
  visible,
  openFolderSignal,
  panel,
  onPanelChange,
  onGitChangeCountChange,
  sidebarCollapsed,
  onSidebarCollapsedChange,
  terminalCollapsed,
  onTerminalCollapsedChange,
}) {
  const [tree, setTree] = useState([])
  const [treeStatus, setTreeStatus] = useState('')
  const [openPath, setOpenPath] = useState(null)
  const [content, setContent] = useState('')
  const [savedContent, setSavedContent] = useState('')
  const [fileStatus, setFileStatus] = useState('')
  const [saving, setSaving] = useState(false)

  const [query, setQuery] = useState('')
  const [searchResults, setSearchResults] = useState([])
  const [searching, setSearching] = useState(false)

  const [gitStatus, setGitStatus] = useState([])
  const [selectedPaths, setSelectedPaths] = useState(new Set())
  const [diffPath, setDiffPath] = useState(null)
  const [diffText, setDiffText] = useState('')
  const [commitMessage, setCommitMessage] = useState('')
  const [gitBusy, setGitBusy] = useState(false)
  const [gitError, setGitError] = useState('')
  const [pushing, setPushing] = useState(false)
  const [pushStatus, setPushStatus] = useState('')

  const [branches, setBranches] = useState([])
  const [branchBusy, setBranchBusy] = useState(false)
  const [branchError, setBranchError] = useState('')
  const [newBranchOpen, setNewBranchOpen] = useState(false)
  const [newBranchName, setNewBranchName] = useState('')
  const [openingFolder, setOpeningFolder] = useState(false)
  const [folderError, setFolderError] = useState('')
  const setSidebarCollapsed = onSidebarCollapsedChange
  const [sidebarWidth, setSidebarWidth] = useEditorSidebarWidth()
  const [terminalPanelHeight, setTerminalPanelHeight] = useTerminalPanelHeight()
  const setTerminalCollapsed = onTerminalCollapsedChange
  const mainColumnRef = useRef(null)
  const sidebarRef = useRef(null)
  // EditorView is always mounted (just hidden) so Chat<->Editor switches
  // don't lose state — but that means the terminal shouldn't mount along
  // with it, or every app load would spawn a PowerShell session even for
  // someone who never opens the Editor tab. Tracks whether this view has
  // ever actually been made visible, so the terminal only mounts — and
  // spawns a real shell — the first time the user navigates here.
  const [everVisible, setEverVisible] = useState(false)
  useEffect(() => {
    if (visible) setEverVisible(true)
  }, [visible])

  const enabled = !!fileAccessSettings?.read_enabled
  const canWrite = !!fileAccessSettings?.write_enabled
  const dirty = content !== savedContent

  useEffect(() => {
    if (!enabled) return
    refreshTree()
    refreshGitStatus()
    refreshBranches()
  }, [enabled])

  // Git's change count now surfaces as a badge on App.jsx's rail button
  // (see the Git button there) rather than only inside this component's
  // own tab row, so the count needs to travel up whenever it changes.
  useEffect(() => {
    onGitChangeCountChange?.(gitStatus.length)
  }, [gitStatus, onGitChangeCountChange])

  function refreshTree() {
    setTreeStatus('Loading…')
    fetchEditorTree().then((entries) => {
      setTree(entries)
      setTreeStatus(entries.length === 0 ? 'No files found in this folder.' : '')
    })
  }

  function refreshGitStatus() {
    fetchGitStatus().then(setGitStatus)
  }

  function refreshBranches() {
    fetchGitBranches().then(setBranches)
  }

  // Switching branches can carry over non-conflicting uncommitted
  // changes (same as running `git switch` by hand) or fail outright if
  // they'd be overwritten — either way the file tree, open file, and
  // git status can all be stale afterward, so refresh everything rather
  // than just the branch list.
  async function handleSwitchBranch(name) {
    setBranchBusy(true)
    setBranchError('')
    try {
      const res = await switchGitBranch(name)
      if (!res.ok) throw new Error(await res.text())
      setOpenPath(null)
      setDiffPath(null)
      refreshTree()
      refreshGitStatus()
      refreshBranches()
    } catch (err) {
      setBranchError(err.message)
    } finally {
      setBranchBusy(false)
    }
  }

  async function handleCreateBranch(e) {
    e.preventDefault()
    if (!newBranchName.trim()) return
    setBranchBusy(true)
    setBranchError('')
    try {
      const res = await createGitBranch(newBranchName.trim())
      if (!res.ok) throw new Error(await res.text())
      setNewBranchName('')
      setNewBranchOpen(false)
      refreshGitStatus()
      refreshBranches()
    } catch (err) {
      setBranchError(err.message)
    } finally {
      setBranchBusy(false)
    }
  }

  // Opens the native OS folder picker and, if a folder is chosen, makes
  // it the new sandbox root for both the editor and the chat model's
  // file tools (they share one root — see internal/chat.editorRoot).
  // Preserves whatever read/write flags were already set; only the root
  // changes. Everything scoped to the old root (open file, diff, tree,
  // git status/branches) is stale afterward, so this clears/refreshes
  // all of it.
  async function handleOpenFolder() {
    setOpeningFolder(true)
    setFolderError('')
    try {
      const picked = await browseForFolder()
      if (picked.cancelled || !picked.path) return
      if (dirty && !window.confirm(`Discard unsaved changes to ${openPath}?`)) return
      const res = await saveFileAccessSettings({
        ...fileAccessSettings,
        root: picked.path,
      })
      if (!res.ok) throw new Error(await res.text())
      const saved = await res.json()
      onFileAccessSettingsChange?.(saved)
      setOpenPath(null)
      setDiffPath(null)
      setSearchResults([])
      refreshTree()
      refreshGitStatus()
      refreshBranches()
    } catch (err) {
      setFolderError(err.message)
    } finally {
      setOpeningFolder(false)
    }
  }

  // openFolderSignal is a bump counter (see App.jsx), not a boolean —
  // App.jsx has no other way to reach into this always-mounted-but-often-
  // hidden component to trigger its folder picker (e.g. from the native
  // File → Open Folder… menu item), and a counter re-fires this effect on
  // every click even if the user picks the same signal value twice in a
  // row (a boolean toggled true/false wouldn't change on every other
  // click). Skips the mount-time run (undefined/0) so opening the app
  // doesn't immediately pop the folder dialog.
  const isFirstOpenFolderSignal = useRef(true)
  useEffect(() => {
    if (isFirstOpenFolderSignal.current) {
      isFirstOpenFolderSignal.current = false
      return
    }
    handleOpenFolder()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [openFolderSignal])

  async function openFile(path) {
    if (dirty && !window.confirm(`Discard unsaved changes to ${openPath}?`)) return
    setDiffPath(null)
    setFileStatus('Loading…')
    try {
      const data = await fetchEditorFile(path)
      setOpenPath(path)
      setContent(data.content)
      setSavedContent(data.content)
      setFileStatus(data.truncated ? 'File truncated (too large to fully load).' : '')
    } catch (err) {
      setFileStatus(`Error opening file: ${err.message}`)
    }
  }

  async function handleSave() {
    if (!openPath || !dirty) return
    setSaving(true)
    setFileStatus('')
    try {
      await saveEditorFile(openPath, content)
      setSavedContent(content)
      refreshGitStatus()
    } catch (err) {
      setFileStatus(`Error saving: ${err.message}`)
    } finally {
      setSaving(false)
    }
  }

  function handleEditorKeyDown(e) {
    if ((e.metaKey || e.ctrlKey) && e.key === 's') {
      e.preventDefault()
      handleSave()
    }
  }

  // Creating is just saving an empty file to a path that doesn't exist
  // yet (see createEditorFile in api.js) — succeeds even for an empty
  // name-only file, then opens it immediately so the human can start
  // typing right away.
  async function handleCreateFile(path) {
    setTreeStatus('')
    try {
      const res = await createEditorFile(path)
      if (!res.ok) throw new Error(await res.text())
      refreshTree()
      refreshGitStatus()
      openFile(path)
    } catch (err) {
      setTreeStatus(`Error creating ${path}: ${err.message}`)
    }
  }

  async function handleDeleteFile(path) {
    if (!window.confirm(`Delete ${path}? This can't be undone.`)) return
    try {
      const res = await deleteEditorFile(path)
      if (!res.ok) throw new Error(await res.text())
      if (openPath === path) {
        setOpenPath(null)
        setContent('')
        setSavedContent('')
      }
      refreshTree()
      refreshGitStatus()
    } catch (err) {
      setTreeStatus(`Error deleting ${path}: ${err.message}`)
    }
  }

  async function handleRenameFile(fromPath, toPath) {
    try {
      const res = await renameEditorFile(fromPath, toPath)
      if (!res.ok) throw new Error(await res.text())
      if (openPath === fromPath) setOpenPath(toPath)
      refreshTree()
      refreshGitStatus()
    } catch (err) {
      setTreeStatus(`Error renaming ${fromPath}: ${err.message}`)
    }
  }

  async function runSearch(e) {
    e.preventDefault()
    if (!query.trim()) return
    setSearching(true)
    try {
      const results = await searchEditor(query.trim())
      setSearchResults(results)
    } finally {
      setSearching(false)
    }
  }

  function togglePathSelection(path) {
    setSelectedPaths((prev) => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  async function viewDiff(entry) {
    if (dirty && !window.confirm(`Discard unsaved changes to ${openPath}?`)) return
    setOpenPath(null)
    setDiffPath(entry.path)
    setDiffText('Loading…')
    try {
      const data = await fetchGitDiff(entry.path, !!entry.staged)
      setDiffText(data.diff || '(no changes)')
    } catch (err) {
      setDiffText(`Error loading diff: ${err.message}`)
    }
  }

  async function handleStageSelected() {
    if (selectedPaths.size === 0) return
    setGitBusy(true)
    setGitError('')
    try {
      await stageGitPaths([...selectedPaths])
      setSelectedPaths(new Set())
      refreshGitStatus()
    } catch (err) {
      setGitError(err.message)
    } finally {
      setGitBusy(false)
    }
  }

  async function handleUnstageSelected() {
    if (selectedPaths.size === 0) return
    setGitBusy(true)
    setGitError('')
    try {
      await unstageGitPaths([...selectedPaths])
      setSelectedPaths(new Set())
      refreshGitStatus()
    } catch (err) {
      setGitError(err.message)
    } finally {
      setGitBusy(false)
    }
  }

  async function handleCommit(e) {
    e.preventDefault()
    if (!commitMessage.trim()) return
    setGitBusy(true)
    setGitError('')
    try {
      const res = await commitGit(commitMessage.trim())
      if (!res.ok) throw new Error(await res.text())
      setCommitMessage('')
      refreshGitStatus()
    } catch (err) {
      setGitError(err.message)
    } finally {
      setGitBusy(false)
    }
  }

  // Push is a separate, explicit action from Commit — never fired
  // automatically after a commit. A plain `git push`: if the remote has
  // diverged, this fails and the error (surfaced via gitError) is shown
  // as-is rather than silently force-pushing or resolving it any way.
  async function handlePush() {
    setPushing(true)
    setGitError('')
    setPushStatus('')
    try {
      const res = await pushGit()
      if (!res.ok) throw new Error(await res.text())
      setPushStatus('Pushed.')
    } catch (err) {
      setGitError(`Push failed: ${err.message}`)
    } finally {
      setPushing(false)
    }
  }

  const languageExtensions = useMemo(() => (openPath ? languageExtensionFor(openPath) : []), [openPath])
  const staged = gitStatus.filter((s) => s.staged)
  const unstaged = gitStatus.filter((s) => s.unstaged)

  // Drags the terminal panel's top divider to resize it vertically.
  // Tracks the cursor against the main column's own bottom edge (rather
  // than delta movement) so a fast drag can't desync from the cursor.
  // Clamped so the panel can't be dragged to nothing or to swallow the
  // whole column.
  function handleTerminalDragStart(e) {
    e.preventDefault()
    const container = mainColumnRef.current
    if (!container) return

    const previousUserSelect = document.body.style.userSelect
    document.body.style.userSelect = 'none'

    function handleMove(moveEvent) {
      const rect = container.getBoundingClientRect()
      const height = rect.bottom - moveEvent.clientY
      setTerminalPanelHeight(Math.min(rect.height - 120, Math.max(120, height)))
    }
    function handleUp() {
      window.removeEventListener('mousemove', handleMove)
      window.removeEventListener('mouseup', handleUp)
      document.body.style.userSelect = previousUserSelect
    }
    window.addEventListener('mousemove', handleMove)
    window.addEventListener('mouseup', handleUp)
  }

  // Drags the sidebar's right-edge divider to resize the Files/Search/Git
  // panel horizontally. Tracks the cursor against the sidebar's own left
  // edge (rather than delta movement) so a fast drag can't desync from
  // the cursor — same approach as handleTerminalDragStart above, just on
  // the horizontal axis. Clamped so the panel can't be dragged to nothing
  // or to swallow the whole editor.
  function handleSidebarDragStart(e) {
    e.preventDefault()
    const el = sidebarRef.current
    if (!el) return

    const previousUserSelect = document.body.style.userSelect
    document.body.style.userSelect = 'none'

    function handleMove(moveEvent) {
      const rect = el.getBoundingClientRect()
      const width = moveEvent.clientX - rect.left
      setSidebarWidth(Math.min(500, Math.max(160, width)))
    }
    function handleUp() {
      window.removeEventListener('mousemove', handleMove)
      window.removeEventListener('mouseup', handleUp)
      document.body.style.userSelect = previousUserSelect
    }
    window.addEventListener('mousemove', handleMove)
    window.addEventListener('mouseup', handleUp)
  }

  if (!enabled) {
    return (
      <div className="editor-view editor-view-empty">
        <p>File access is off. Enable it in Settings → File access to use the editor.</p>
      </div>
    )
  }

  return (
    <div className="editor-view">
      <aside
        ref={sidebarRef}
        className={`editor-sidebar ${sidebarCollapsed ? 'is-collapsed' : ''}`}
        style={sidebarCollapsed ? undefined : { width: sidebarWidth }}
      >
        <button
          type="button"
          className="editor-sidebar-collapse-toggle"
          title={sidebarCollapsed ? 'Expand file panel' : 'Collapse file panel'}
          onClick={() => setSidebarCollapsed((c) => !c)}
        >
          {sidebarCollapsed ? '»' : '«'}
        </button>

        {!sidebarCollapsed && (
          <>
            <div className="editor-folder-row">
              <button type="button" onClick={handleOpenFolder} disabled={openingFolder} title={fileAccessSettings?.root}>
                {openingFolder ? 'Opening…' : 'Open Folder…'}
              </button>
              {fileAccessSettings?.root && (
                <span className="editor-folder-name" title={fileAccessSettings.root}>
                  {fileAccessSettings.root.split(/[/\\]/).filter(Boolean).pop()}
                </span>
              )}
            </div>
            {folderError && <p className="editor-error">{folderError}</p>}
            <div className="editor-panel-tabs">
              <button
                type="button"
                className={panel === 'files' ? 'is-active' : ''}
                onClick={() => onPanelChange?.('files')}
              >
                Files
              </button>
            </div>

        {panel === 'files' && (
          <div className="editor-panel-body editor-panel-body-tree">
            {treeStatus && <p className="editor-hint">{treeStatus}</p>}
            <FileTree
              paths={tree.map((entry) => entry.path)}
              openPath={openPath}
              onOpenFile={openFile}
              canWrite={canWrite}
              onCreateFile={handleCreateFile}
              onDeleteFile={handleDeleteFile}
              onRenameFile={handleRenameFile}
            />
          </div>
        )}

        {panel === 'search' && (
          <div className="editor-panel-body">
            <form onSubmit={runSearch} className="editor-search-form">
              <input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Search all files…"
              />
              <button type="submit" disabled={searching || !query.trim()}>
                {searching ? '…' : 'Go'}
              </button>
            </form>
            <ul className="editor-search-results">
              {searchResults.map((m, i) => (
                <li key={i}>
                  <button type="button" onClick={() => openFile(m.path)}>
                    <span className="editor-search-path">{m.path}:{m.line}</span>
                    <span className="editor-search-text">{m.text.trim()}</span>
                  </button>
                </li>
              ))}
              {searchResults.length === 0 && query && !searching && (
                <li className="editor-hint">No matches.</li>
              )}
            </ul>
          </div>
        )}

        {panel === 'git' && branches.length === 0 ? (
          <div className="editor-panel-body">
            <p className="editor-hint">This folder isn't a git repository, so version control isn't available here.</p>
          </div>
        ) : panel === 'git' && (
          <div className="editor-panel-body">
            {branchError && <p className="editor-error">{branchError}</p>}
            <div className="editor-branch-row">
              <select
                className="editor-branch-select"
                value={branches.find((b) => b.current)?.name ?? ''}
                onChange={(e) => handleSwitchBranch(e.target.value)}
                disabled={branchBusy || branches.length === 0}
              >
                {branches.map((b) => (
                  <option key={b.name} value={b.name}>
                    {b.name}
                  </option>
                ))}
              </select>
              <button type="button" onClick={() => setNewBranchOpen((v) => !v)} disabled={branchBusy}>
                New
              </button>
            </div>
            {newBranchOpen && (
              <form onSubmit={handleCreateBranch} className="editor-search-form">
                <input
                  value={newBranchName}
                  onChange={(e) => setNewBranchName(e.target.value)}
                  placeholder="New branch name"
                  autoFocus
                />
                <button type="submit" disabled={branchBusy || !newBranchName.trim()}>
                  Create
                </button>
              </form>
            )}

            {gitError && <p className="editor-error">{gitError}</p>}
            <form onSubmit={handleCommit} className="editor-commit-form">
              <textarea
                value={commitMessage}
                onChange={(e) => setCommitMessage(e.target.value)}
                placeholder="Commit message"
                rows={2}
              />
              <button type="submit" disabled={gitBusy || !commitMessage.trim() || staged.length === 0}>
                Commit {staged.length > 0 ? `(${staged.length})` : ''}
              </button>
            </form>

            <div className="editor-push-row">
              <button type="button" className="btn-secondary" onClick={handlePush} disabled={pushing}>
                {pushing ? 'Pushing…' : 'Push'}
              </button>
              {pushStatus && <span className="editor-push-status">{pushStatus}</span>}
            </div>

            <div className="editor-git-actions">
              <button type="button" onClick={handleStageSelected} disabled={gitBusy || selectedPaths.size === 0}>
                Stage selected
              </button>
              <button type="button" onClick={handleUnstageSelected} disabled={gitBusy || selectedPaths.size === 0}>
                Unstage selected
              </button>
            </div>

            {staged.length > 0 && (
              <>
                <h4>Staged</h4>
                <ul className="editor-git-list">
                  {staged.map((entry) => (
                    <li key={entry.path}>
                      <input
                        type="checkbox"
                        checked={selectedPaths.has(entry.path)}
                        onChange={() => togglePathSelection(entry.path)}
                      />
                      <button type="button" onClick={() => viewDiff({ ...entry, staged: true })}>
                        <span className={`editor-git-code editor-git-${entry.staged.toLowerCase()}`}>{entry.staged}</span>
                        {entry.path}
                      </button>
                    </li>
                  ))}
                </ul>
              </>
            )}

            {unstaged.length > 0 && (
              <>
                <h4>Changes</h4>
                <ul className="editor-git-list">
                  {unstaged.map((entry) => (
                    <li key={entry.path}>
                      <input
                        type="checkbox"
                        checked={selectedPaths.has(entry.path)}
                        onChange={() => togglePathSelection(entry.path)}
                      />
                      <button type="button" onClick={() => viewDiff({ ...entry, staged: false })}>
                        <span className={`editor-git-code editor-git-${entry.unstaged === '?' ? 'untracked' : entry.unstaged.toLowerCase()}`}>
                          {entry.unstaged === '?' ? 'U' : entry.unstaged}
                        </span>
                        {entry.path}
                      </button>
                    </li>
                  ))}
                </ul>
              </>
            )}

            {gitStatus.length === 0 && <p className="editor-hint">No changes.</p>}
          </div>
        )}
          </>
        )}
      </aside>

      {!sidebarCollapsed && (
        <div className="editor-sidebar-divider" onMouseDown={handleSidebarDragStart} />
      )}

      <main className="editor-main" ref={mainColumnRef}>
        <div className="editor-main-content">
          {openPath ? (
            <>
              <div className="editor-file-header">
                <span className="editor-file-path">{openPath}</span>
                {fileStatus && <span className="editor-file-status">{fileStatus}</span>}
                <button
                  type="button"
                  className="editor-save-button"
                  onClick={handleSave}
                  disabled={!canWrite || !dirty || saving}
                  title={canWrite ? '' : 'File writes are disabled in Settings → File access'}
                >
                  {saving ? 'Saving…' : dirty ? 'Save' : 'Saved'}
                </button>
              </div>
              <div className="editor-codemirror" onKeyDown={handleEditorKeyDown}>
                <CodeMirror
                  value={content}
                  height="100%"
                  theme={theme === 'light' ? githubLight : githubDark}
                  extensions={languageExtensions}
                  onChange={setContent}
                  readOnly={!canWrite}
                />
              </div>
            </>
          ) : diffPath ? (
            <>
              <div className="editor-file-header">
                <span className="editor-file-path">Diff: {diffPath}</span>
                <button type="button" onClick={() => setDiffPath(null)}>Close</button>
              </div>
              <pre className="editor-diff">{diffText}</pre>
            </>
          ) : (
            <div className="editor-empty-state">
              <p>Select a file to open it.</p>
            </div>
          )}
        </div>

        {!terminalCollapsed && (
          <div className="terminal-panel-divider" onMouseDown={handleTerminalDragStart} />
        )}

        <div
          className={`terminal-panel ${terminalCollapsed ? 'is-collapsed' : ''}`}
          style={{ flex: terminalCollapsed ? '0 0 auto' : `0 0 ${terminalPanelHeight}px` }}
        >
          <div className="terminal-panel-header">
            <span className="terminal-panel-title">Terminal</span>
            <button
              type="button"
              className="terminal-panel-collapse-toggle"
              title={terminalCollapsed ? 'Expand terminal' : 'Collapse terminal'}
              onClick={() => setTerminalCollapsed((c) => !c)}
            >
              {terminalCollapsed ? '▲' : '▼'}
            </button>
          </div>
          <div
            className="terminal-panel-body"
            style={{ display: terminalCollapsed ? 'none' : undefined }}
          >
            {terminalEnabled ? (
              everVisible && <TerminalView theme={theme} />
            ) : (
              <div className="terminal-disabled-state">
                <p>Terminal is disabled. Enable it in Settings → Terminal.</p>
              </div>
            )}
          </div>
        </div>
      </main>
    </div>
  )
}
