import { useEffect, useMemo, useState } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { githubLight, githubDark } from '@uiw/codemirror-theme-github'
import {
  fetchEditorTree,
  fetchEditorFile,
  saveEditorFile,
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
} from '../api'
import { languageExtensionFor } from '../editorLanguages'

const PANELS = { files: 'Files', search: 'Search', git: 'Git' }

export default function EditorView({ fileAccessSettings, theme }) {
  const [panel, setPanel] = useState('files')
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

  const enabled = !!fileAccessSettings?.read_enabled
  const canWrite = !!fileAccessSettings?.write_enabled
  const dirty = content !== savedContent

  useEffect(() => {
    if (!enabled) return
    refreshTree()
    refreshGitStatus()
    refreshBranches()
  }, [enabled])

  function refreshTree() {
    setTreeStatus('Loading…')
    fetchEditorTree().then((entries) => {
      setTree(entries)
      setTreeStatus(entries.length === 0 ? 'No files found (project folder may not be a git repository).' : '')
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

  if (!enabled) {
    return (
      <div className="editor-view editor-view-empty">
        <p>File access is off. Enable it in Settings → File access to use the editor.</p>
      </div>
    )
  }

  return (
    <div className="editor-view">
      <aside className="editor-sidebar">
        <div className="editor-panel-tabs">
          {Object.entries(PANELS).map(([key, label]) => (
            <button
              key={key}
              type="button"
              className={panel === key ? 'is-active' : ''}
              onClick={() => setPanel(key)}
            >
              {label}
              {key === 'git' && gitStatus.length > 0 && <span className="editor-badge">{gitStatus.length}</span>}
            </button>
          ))}
        </div>

        {panel === 'files' && (
          <div className="editor-panel-body">
            {treeStatus && <p className="editor-hint">{treeStatus}</p>}
            <ul className="editor-file-list">
              {tree.map((entry) => (
                <li key={entry.path}>
                  <button
                    type="button"
                    className={openPath === entry.path ? 'is-active' : ''}
                    onClick={() => openFile(entry.path)}
                    title={entry.path}
                  >
                    {entry.path}
                  </button>
                </li>
              ))}
            </ul>
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

        {panel === 'git' && (
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
      </aside>

      <main className="editor-main">
        {openPath ? (
          <>
            <div className="editor-file-header">
              <span className="editor-file-path">{openPath}</span>
              {fileStatus && <span className="editor-file-status">{fileStatus}</span>}
              <button
                type="button"
                className="btn-primary"
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
      </main>
    </div>
  )
}
