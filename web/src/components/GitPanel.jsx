import { useEffect, useState } from 'react'

// The Git sub-panel of the editor sidebar: branch switcher, stage/unstage,
// commit, and push — a numbered stepper (Branch → Stage changes → Commit →
// Push). Purely presentational: all state and API calls live in
// EditorView, which owns them because they're also needed outside this
// panel (e.g. the git-change badge on App.jsx's rail button stays live
// regardless of which panel is showing). Same props-down pattern already
// used for FileTree.
export default function GitPanel({
  branches,
  branchBusy,
  branchError,
  onSwitchBranch,
  newBranchOpen,
  onToggleNewBranch,
  newBranchName,
  onNewBranchNameChange,
  onCreateBranch,
  creatingBranch,

  gitStatus,
  staged,
  unstaged,
  gitBusy,
  gitBusyAction,
  gitError,
  onToggleStage,
  onStageAll,
  onUnstageAll,
  onDiscardPath,
  onViewDiff,

  commitMessage,
  onCommitMessageChange,
  onCommit,

  pushing,
  pushStatus,
  pushNeedsUpstream,
  onPush,
  onPushSetUpstream,
}) {
  // Sticky stepper progress: once a step's condition is met it stays lit,
  // even after the state that triggered it clears (e.g. staged.length
  // drops to 0 right after a commit) — otherwise the stepper regresses to
  // "not done" between commit and push, which reads as if the commit
  // didn't count. A successful push closes the loop, so it resets the
  // whole stepper back to step 1 for the next round of changes (after a
  // beat, so "4" still gets its moment lit up).
  const currentBranch = branches.find((b) => b.current)?.name
  const [maxStep, setMaxStep] = useState(1)

  useEffect(() => {
    setMaxStep(1)
  }, [currentBranch])

  useEffect(() => {
    if (gitStatus.length === 0) {
      setMaxStep(1)
    } else if (staged.length > 0) {
      setMaxStep((m) => Math.max(m, 3))
    } else {
      setMaxStep(2)
    }
  }, [gitStatus.length, staged.length])

  useEffect(() => {
    if (!pushStatus) return
    setMaxStep(4)
    const timer = setTimeout(() => setMaxStep(1), 1200)
    return () => clearTimeout(timer)
  }, [pushStatus])

  if (branches.length === 0) {
    return (
      <div className="editor-panel-body">
        <p className="editor-hint">This folder isn't a git repository, so version control isn't available here.</p>
      </div>
    )
  }

  return (
    <div className="editor-panel-body git-stepper">
      {branchError && <p className="editor-error">{branchError}</p>}
      {gitError && <p className="editor-error">{gitError}</p>}

      <div className="git-step">
        <div className="git-step-rail">
          <div className="git-step-dot on">1</div>
          <div className="git-step-rail-line" />
        </div>
        <div className="git-step-body">
          <div className="git-step-title">Branch</div>
          <div className="git-branch-row">
            <svg className="git-branch-icon" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><line x1="6" y1="3" x2="6" y2="15" /><circle cx="18" cy="6" r="3" /><circle cx="6" cy="18" r="3" /><path d="M18 9a9 9 0 0 1-9 9" /></svg>
            <select
              className="git-branch-select"
              value={branches.find((b) => b.current)?.name ?? ''}
              onChange={(e) => onSwitchBranch(e.target.value)}
              disabled={branchBusy || branches.length === 0}
            >
              {branches.map((b) => (
                <option key={b.name} value={b.name}>
                  {b.name}
                </option>
              ))}
            </select>
            <button
              type="button"
              className="git-icon-btn"
              title="New branch"
              onClick={onToggleNewBranch}
              disabled={branchBusy}
            >
              <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round"><path d="M12 5v14M5 12h14" /></svg>
            </button>
          </div>
          {newBranchOpen && (
            <form onSubmit={onCreateBranch} className="editor-search-form">
              <input
                value={newBranchName}
                onChange={(e) => onNewBranchNameChange(e.target.value)}
                placeholder="New branch name"
                autoFocus
              />
              <button type="submit" disabled={branchBusy || !newBranchName.trim()}>
                {creatingBranch ? 'Creating…' : 'Create'}
              </button>
            </form>
          )}
        </div>
      </div>

      <div className="git-step">
        <div className="git-step-rail">
          <div className={`git-step-dot ${maxStep >= 2 ? 'on' : 'off'}`}>2</div>
          <div className="git-step-rail-line" />
        </div>
        <div className="git-step-body">
          <div className="git-step-title">Stage changes</div>

          {staged.length > 0 && (
            <div className="git-card git-card-staged">
              <div className="git-card-head">
                <span className="title">STAGED · {staged.length}</span>
                <button type="button" onClick={onUnstageAll} disabled={gitBusy}>
                  {gitBusy && gitBusyAction === 'unstage' ? 'Unstaging…' : 'Unstage all'}
                </button>
              </div>
              <ul className="git-file-list">
                {staged.map((entry) => {
                  const slash = entry.path.lastIndexOf('/')
                  return (
                    <li key={entry.path} className="git-file-row">
                      <input
                        type="checkbox"
                        checked
                        disabled={gitBusy}
                        onChange={() => onToggleStage(entry.path, true)}
                        title="Unstage"
                      />
                      <button type="button" className="git-file-path" onClick={() => onViewDiff({ ...entry, staged: true })}>
                        {slash >= 0 && <span className="dir">{entry.path.slice(0, slash + 1)}</span>}
                        <span className="name">{slash >= 0 ? entry.path.slice(slash + 1) : entry.path}</span>
                      </button>
                      <span className={`git-badge git-badge-${entry.staged.toLowerCase()}`}>{entry.staged}</span>
                    </li>
                  )
                })}
              </ul>
            </div>
          )}

          {unstaged.length > 0 && (
            <div className="git-card git-card-unstaged">
              <div className="git-card-head">
                <span className="title">CHANGES · {unstaged.length}</span>
                <button type="button" onClick={onStageAll} disabled={gitBusy}>
                  {gitBusy && gitBusyAction === 'stage' ? 'Staging…' : 'Stage all'}
                </button>
              </div>
              <ul className="git-file-list">
                {unstaged.map((entry) => {
                  const slash = entry.path.lastIndexOf('/')
                  const code = entry.unstaged === '?' ? 'U' : entry.unstaged
                  return (
                    <li key={entry.path} className="git-file-row">
                      <input
                        type="checkbox"
                        checked={false}
                        disabled={gitBusy}
                        onChange={() => onToggleStage(entry.path, false)}
                        title="Stage"
                      />
                      <button type="button" className="git-file-path" onClick={() => onViewDiff({ ...entry, staged: false })}>
                        {slash >= 0 && <span className="dir">{entry.path.slice(0, slash + 1)}</span>}
                        <span className="name">{slash >= 0 ? entry.path.slice(slash + 1) : entry.path}</span>
                      </button>
                      <span className={`git-badge git-badge-${code.toLowerCase()}`}>{code}</span>
                      <button
                        type="button"
                        className="git-discard-link"
                        disabled={gitBusy}
                        onClick={() => onDiscardPath([entry.path])}
                        title="Revert this change to its last committed version"
                      >
                        {gitBusy && gitBusyAction === 'discard' ? '…' : 'Discard'}
                      </button>
                    </li>
                  )
                })}
              </ul>
            </div>
          )}

          {gitStatus.length === 0 && <p className="editor-hint">No changes.</p>}
        </div>
      </div>

      <div className="git-step">
        <div className="git-step-rail">
          <div className={`git-step-dot ${maxStep >= 3 ? 'on' : 'off'}`}>3</div>
          <div className="git-step-rail-line" />
        </div>
        <div className="git-step-body">
          <div className="git-step-title">Commit</div>
          <form onSubmit={onCommit} className="editor-commit-form">
            <textarea
              value={commitMessage}
              onChange={(e) => onCommitMessageChange(e.target.value)}
              placeholder="Commit message"
              rows={2}
            />
            <button type="submit" className="git-commit-btn" disabled={gitBusy || !commitMessage.trim() || staged.length === 0}>
              <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.6" strokeLinecap="round" strokeLinejoin="round"><path d="M5 13l4 4L19 7" /></svg>
              {gitBusy && gitBusyAction === 'commit'
                ? 'Committing…'
                : `Commit ${staged.length > 0 ? `(${staged.length})` : ''}`}
            </button>
          </form>
        </div>
      </div>

      <div className="git-step">
        <div className="git-step-rail">
          <div className={`git-step-dot ${maxStep >= 4 ? 'on' : 'off'}`}>4</div>
        </div>
        <div className="git-step-body">
          <div className="git-step-title">Push</div>
          <button type="button" className="git-push-btn" onClick={onPush} disabled={pushing}>
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round"><path d="M12 19V5M5 12l7-7 7 7" /></svg>
            {pushing ? 'Pushing…' : 'Push'}
          </button>
          {pushNeedsUpstream && (
            <button
              type="button"
              className="git-push-btn editor-push-upstream"
              onClick={onPushSetUpstream}
              disabled={pushing}
              title="This branch has no upstream yet — push and set one"
            >
              {pushing ? 'Pushing…' : 'Set upstream & push'}
            </button>
          )}
          {pushStatus && <span className="editor-push-status">{pushStatus}</span>}
        </div>
      </div>
    </div>
  )
}
