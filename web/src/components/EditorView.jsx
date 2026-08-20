import { useEffect, useMemo, useRef, useState } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { githubLight, githubDark } from '@uiw/codemirror-theme-github'
import { linter, lintGutter, forceLinting } from '@codemirror/lint'
import {
  fetchEditorTree,
  fetchEditorFile,
  saveEditorFile,
  createEditorFile,
  deleteEditorFile,
  fetchEditorFolderFileCount,
  deleteEditorFolder,
  renameEditorFile,
  searchEditor,
  fetchGitStatus,
  fetchGitDiff,
  stageGitPaths,
  unstageGitPaths,
  discardGitPaths,
  commitGit,
  pushGit,
  pushSetUpstreamGit,
  fetchGitBranches,
  switchGitBranch,
  createGitBranch,
  browseForFolder,
  saveFileAccessSettings,
  apiOrigin,
} from '../api'
import { languageExtensionFor, languageNameFor } from '../editorLanguages'
import { parseDiff } from '../diffFormat'
import { aiCompletionExtension } from '../aiCompletion'
import { useEditorSidebarWidth } from '../useEditorSidebarWidth'
import { useTerminalPanelHeight } from '../useTerminalPanelHeight'
import FileTree from './FileTree'
import TerminalView from './TerminalView'
import ConfirmDeleteModal from './ConfirmDeleteModal'

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
  onOpenPathChange,
  sidebarCollapsed,
  onSidebarCollapsedChange,
  terminalCollapsed,
  onTerminalCollapsedChange,
}) {
  const [tree, setTree] = useState([])
  const [treeStatus, setTreeStatus] = useState('')
  // Every currently open file is its own tab, each with its own buffer —
  // switching tabs must never lose an unsaved edit in another one, so
  // content/savedContent/fileStatus all live per-tab here rather than as
  // single top-level fields. Keyed by path since a file can only be open
  // once (opening an already-open path just activates its existing tab).
  const [openTabs, setOpenTabs] = useState([]) // [{ path, content, savedContent, fileStatus }]
  const [activeTabPath, setActiveTabPath] = useState(null)
  const openPath = activeTabPath
  const activeTab = openTabs.find((t) => t.path === activeTabPath) ?? null
  const content = activeTab?.content ?? ''
  const savedContent = activeTab?.savedContent ?? ''
  const fileStatus = activeTab?.fileStatus ?? ''
  function setFileStatus(value) {
    setOpenTabs((prev) => prev.map((t) => (t.path === activeTabPath ? { ...t, fileStatus: value } : t)))
  }
  // Lint findings from the most recent save (see api.js's saveEditorFile —
  // the backend lints JS/JSX files with the target project's own oxlint
  // right after writing). Kept in per-path refs (read by each tab's own
  // linter() extension source function below) rather than only React
  // state, since CodeMirror pulls diagnostics by calling that function
  // itself — forceLinting() after a save is what actually triggers a given
  // tab's view to re-read its entry. A plain object keyed by path, not a
  // single ref, because every open tab's CodeMirror instance stays mounted
  // at once (see the "full instance per tab" tab strategy below) and each
  // needs its own diagnostics untouched by saves happening in other tabs.
  const lintDiagnosticsByPath = useRef({})
  // Same per-path keying as lintDiagnosticsByPath, and for the same
  // reason: forceLinting() needs the specific tab's CodeMirror view, not
  // whichever tab happened to mount most recently.
  const codeMirrorViewsByPath = useRef({})
  const [saving, setSaving] = useState(false)

  const [query, setQuery] = useState('')
  const [searchResults, setSearchResults] = useState([])
  const [searching, setSearching] = useState(false)

  const [gitStatus, setGitStatus] = useState([])
  const [selectedPaths, setSelectedPaths] = useState(new Set())
  const [diffPath, setDiffPath] = useState(null)
  const [diffText, setDiffText] = useState('')
  const diffLines = useMemo(() => parseDiff(diffText), [diffText])
  const [commitMessage, setCommitMessage] = useState('')
  const [gitBusy, setGitBusy] = useState(false)
  // Commit/Stage/Unstage share one gitBusy flag (they're already mutually
  // exclusive — all three disable together while any one runs), but each
  // button still needs its OWN in-flight label rather than all three
  // flipping to the same text regardless of which one was actually
  // clicked, which would misleadingly show e.g. "Committing…" on the
  // Commit button while a Stage request is really what's running.
  const [gitBusyAction, setGitBusyAction] = useState(null) // 'commit' | 'stage' | 'unstage' | null
  const [gitError, setGitError] = useState('')
  const [pushing, setPushing] = useState(false)
  const [pushStatus, setPushStatus] = useState('')
  // True only for the one specific push failure with a single unambiguous
  // fix (see internal/gitrepo.ErrNoUpstream) — shows a "Set upstream &
  // push" button alongside the error instead of leaving the user to run
  // the git command by hand in a terminal. Cleared on every new push
  // attempt so a later, different failure doesn't keep showing a button
  // for a problem that's no longer the one in front of them.
  const [pushNeedsUpstream, setPushNeedsUpstream] = useState(false)

  const [branches, setBranches] = useState([])
  const [branchBusy, setBranchBusy] = useState(false)
  // branchBusy is shared with handleSwitchBranch (the <select> above), so
  // this tracks specifically whether it's the Create form's own submit
  // in flight — same reasoning as gitBusyAction above.
  const [creatingBranch, setCreatingBranch] = useState(false)
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
  // ever actually been made visible, so the first terminal tab only
  // mounts — and spawns a real shell — the first time the user navigates
  // here.
  const [everVisible, setEverVisible] = useState(false)
  useEffect(() => {
    if (visible) setEverVisible(true)
  }, [visible])

  // Each terminal tab is an independently mounted <TerminalView>, its own
  // xterm instance and WebSocket/PTY session server-side (see
  // internal/terminal/registry.go — already tracks any number of
  // sessions and tears every one of them down together on app shutdown,
  // so multiple tabs needed no backend changes). All tabs stay mounted
  // (display:none when inactive) so switching tabs never loses
  // scrollback or kills the underlying shell, mirroring the
  // always-mounted convention used for the Chat/Editor panes themselves.
  const [terminalTabs, setTerminalTabs] = useState(() => [1])
  const [activeTerminalTab, setActiveTerminalTab] = useState(1)
  const nextTerminalIdRef = useRef(2)

  function addTerminalTab() {
    const id = nextTerminalIdRef.current++
    setTerminalTabs((prev) => [...prev, id])
    setActiveTerminalTab(id)
  }

  function closeTerminalTab(id) {
    setTerminalTabs((prev) => {
      const next = prev.filter((t) => t !== id)
      // Closing the last tab still leaves at least one behind — a
      // terminal panel with zero tabs and no way to get one back short
      // of reloading the whole app would be a dead end, not a real
      // "closed" state.
      if (next.length === 0) {
        const freshId = nextTerminalIdRef.current++
        setActiveTerminalTab(freshId)
        return [freshId]
      }
      if (id === activeTerminalTab) {
        setActiveTerminalTab(next[next.length - 1])
      }
      return next
    })
  }

  const enabled = !!fileAccessSettings?.read_enabled
  const canWrite = !!fileAccessSettings?.write_enabled
  const dirty = content !== savedContent

  // Themeable, keyboard-accessible replacement for window.confirm() — same
  // ConfirmDeleteModal used for conversation/skill delete elsewhere in the
  // app, rather than a native browser dialog that can't be restyled and
  // looks like it belongs to a different app. confirmDialog(...) mirrors
  // window.confirm's call shape (await it, get a boolean back) so the four
  // call sites below only needed "window.confirm(x)" swapped for
  // "await confirmDialog(x)" — no control-flow restructuring beyond that.
  const [pendingConfirm, setPendingConfirm] = useState(null)
  function confirmDialog({ heading, description, confirmLabel }) {
    return new Promise((resolve) => {
      setPendingConfirm({ heading, description, confirmLabel, resolve })
    })
  }
  function resolvePendingConfirm(result) {
    pendingConfirm?.resolve(result)
    setPendingConfirm(null)
  }
  // closeTab below guards a tab's in-progress edit this same way — takes
  // the path explicitly since a tab can be closed without being the
  // active one, so it can't rely on the top-level openPath.
  function confirmDiscardChanges(path) {
    return confirmDialog({
      heading: 'Discard unsaved changes?',
      description: `"${path}" has unsaved changes that will be lost.`,
      confirmLabel: 'Discard changes',
    })
  }
  // Folder switches, branch switches, and "open folder" all used to
  // guard on the single `dirty` flag before nuking the one open file —
  // now that any number of tabs can be dirty at once, they need to know
  // whether ANY of them are, and confirmDiscardChanges' message doesn't
  // fit that plural case, so those call sites get their own dedicated
  // check instead of the single-file one below.
  const anyDirty = openTabs.some((t) => t.content !== t.savedContent)
  function confirmDiscardAllChanges() {
    return confirmDialog({
      heading: 'Discard unsaved changes?',
      description: 'One or more open files have unsaved changes that will be lost.',
      confirmLabel: 'Discard changes',
    })
  }

  useEffect(() => {
    if (!enabled) return
    refreshTree()
    refreshGitStatus()
    refreshBranches()
  }, [enabled])

  // Git state also changes from outside this panel entirely — the chat
  // model's write_file tool, the user typing `git commit`/`git branch`/etc.
  // directly into the Editor's own Terminal panel, or really any external
  // tool touching the working tree or .git while fastllm is open. Rather
  // than poll on a timer (which has an idle cost even when nothing
  // changes, and up to a full interval of lag when something does), this
  // subscribes to the backend's GET /api/editor/git/watch SSE stream —
  // internal/chat.EditorGitWatch — which is itself backed by a real
  // filesystem watcher on the whole working tree plus .git/HEAD,
  // .git/refs, and .git/index (see internal/gitrepo.Watch), the same
  // "notified, not polled" approach VS Code and other IDEs use for git
  // status. Subscribes any time file access is enabled at all — not just
  // while the Git sub-panel is the visible one — because the rail badge
  // (see App.jsx's gitChangeCount) needs a live count regardless of which
  // panel the user is looking at; gating this on panel === 'git' left the
  // badge frozen at whatever it was on mount whenever the user was
  // anywhere else. Switching to the Git panel still does one immediate
  // refetch of its own (see the `enabled` effect above), independent of
  // whatever the SSE stream reports afterward.
  useEffect(() => {
    if (!enabled) return
    refreshGitStatus()
    refreshBranches()
    // Routed through apiOrigin() for the same reason streamChat is (see
    // api.js): the desktop build's Wails in-process AssetServer bridge
    // (cmd/desktop/main.go) doesn't implement http.Flusher, so this SSE
    // stream 500s with "streaming unsupported" over that bridge and the
    // EventSource never receives a single event — the live git-change
    // badge/panel refresh silently never fires in the desktop app,
    // leaving it stuck on whatever the last one-off refetch above saw.
    // Plain relative URL is unaffected and still used for the
    // plain-browser build (apiOrigin() resolves to '' there).
    let source
    let cancelled = false
    apiOrigin().then((origin) => {
      if (cancelled) return
      source = new EventSource(`${origin}/api/editor/git/watch`)
      source.addEventListener('changed', () => {
        refreshGitStatus()
        refreshBranches()
      })
    })
    // EventSource retries on its own after a drop (e.g. the sandbox root
    // changed in Settings, closing the stream server-side) — no manual
    // reconnect logic needed here, same as the browser's default SSE
    // behavior anywhere else.
    return () => {
      cancelled = true
      source?.close()
    }
  }, [enabled])

  // Git's change count now surfaces as a badge on App.jsx's rail button
  // (see the Git button there) rather than only inside this component's
  // own tab row, so the count needs to travel up whenever it changes.
  useEffect(() => {
    onGitChangeCountChange?.(gitStatus.length)
  }, [gitStatus, onGitChangeCountChange])

  // The chat composer shows which file is currently open in the editor
  // (see App.jsx's activeEditorFile) so the model knows what "this file" /
  // "the current file" refers to, and so it's sent along with chat
  // requests as context. Same lift-state-up pattern as gitStatus above.
  useEffect(() => {
    onOpenPathChange?.(openPath)
  }, [openPath, onOpenPathChange])

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
    if (anyDirty && !(await confirmDiscardAllChanges())) return
    setBranchBusy(true)
    setBranchError('')
    try {
      const res = await switchGitBranch(name)
      if (!res.ok) throw new Error(await res.text())
      closeAllTabs()
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
    setCreatingBranch(true)
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
      setCreatingBranch(false)
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
      if (anyDirty && !(await confirmDiscardAllChanges())) return
      const res = await saveFileAccessSettings({
        ...fileAccessSettings,
        root: picked.path,
      })
      if (!res.ok) throw new Error(await res.text())
      const saved = await res.json()
      onFileAccessSettingsChange?.(saved)
      closeAllTabs()
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

  // Shared by openFile (new tab) and reloadTabFromDisk (existing tab,
  // forced refresh) below — fetches path's on-disk content and writes it
  // into whichever tab already has that path, clearing stale lint
  // diagnostics and re-triggering the linter against the new content.
  async function loadFileIntoTab(path) {
    try {
      const data = await fetchEditorFile(path)
      setOpenTabs((prev) =>
        prev.map((t) =>
          t.path === path
            ? {
                ...t,
                content: data.content,
                savedContent: data.content,
                fileStatus: data.truncated ? 'File truncated (too large to fully load).' : '',
              }
            : t
        )
      )
      delete lintDiagnosticsByPath.current[path]
      const view = codeMirrorViewsByPath.current[path]
      if (view) forceLinting(view)
    } catch (err) {
      setOpenTabs((prev) =>
        prev.map((t) => (t.path === path ? { ...t, fileStatus: `Couldn't open this file: ${err.message}` } : t))
      )
    }
  }

  // Opening a file that's already got a tab just activates that tab (same
  // as clicking an already-open tab in VS Code) rather than re-fetching it
  // from disk and clobbering whatever unsaved edits it might have — the
  // only path that touches the network is genuinely opening a new tab.
  async function openFile(path) {
    setDiffPath(null)
    if (openTabs.some((t) => t.path === path)) {
      setActiveTabPath(path)
      return
    }
    setOpenTabs((prev) => [...prev, { path, content: '', savedContent: '', fileStatus: 'Loading…' }])
    setActiveTabPath(path)
    await loadFileIntoTab(path)
  }

  // Re-fetches an already-open tab's content from disk, overwriting
  // whatever's in the buffer — used after a discard, where the in-memory
  // content is known-stale rather than a real unsaved edit worth keeping.
  function reloadTabFromDisk(path) {
    loadFileIntoTab(path)
  }

  async function closeTab(path) {
    const tab = openTabs.find((t) => t.path === path)
    if (tab && tab.content !== tab.savedContent && !(await confirmDiscardChanges(path))) return
    delete lintDiagnosticsByPath.current[path]
    delete codeMirrorViewsByPath.current[path]
    setOpenTabs((prev) => {
      const next = prev.filter((t) => t.path !== path)
      if (path === activeTabPath) {
        setActiveTabPath(next.length > 0 ? next[next.length - 1].path : null)
      }
      return next
    })
  }

  // Used where an operation invalidates every open file at once (folder
  // switch, branch switch) — callers are responsible for confirming with
  // anyDirty/confirmDiscardAllChanges first, since a per-tab confirm here
  // would mean answering one popup per open tab.
  function closeAllTabs() {
    lintDiagnosticsByPath.current = {}
    codeMirrorViewsByPath.current = {}
    setOpenTabs([])
    setActiveTabPath(null)
  }

  // Removes a tab without asking about unsaved changes — for the delete
  // and discard flows below, where the file's on-disk content just
  // changed out from under the tab (or vanished entirely), so keeping the
  // in-memory edit around isn't "unsaved work," it's already stale.
  function removeTabNoConfirm(path) {
    delete lintDiagnosticsByPath.current[path]
    delete codeMirrorViewsByPath.current[path]
    setOpenTabs((prev) => {
      const next = prev.filter((t) => t.path !== path)
      if (path === activeTabPath) {
        setActiveTabPath(next.length > 0 ? next[next.length - 1].path : null)
      }
      return next
    })
  }

  async function handleSave() {
    if (!openPath || !dirty) return
    const path = openPath
    const savingContent = content
    setSaving(true)
    setFileStatus('')
    try {
      const res = await saveEditorFile(path, savingContent)
      if (!res.ok) throw new Error(await res.text())
      const data = await res.json()
      setOpenTabs((prev) =>
        prev.map((t) => (t.path === path ? { ...t, savedContent: savingContent } : t))
      )
      lintDiagnosticsByPath.current[path] = data.diagnostics ?? []
      const view = codeMirrorViewsByPath.current[path]
      if (view) forceLinting(view)
      refreshGitStatus()
    } catch (err) {
      setFileStatus(`Couldn't save this file: ${err.message}`)
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
      setTreeStatus(`Couldn't create ${path}: ${err.message}`)
    }
  }

  async function handleDeleteFile(path) {
    const ok = await confirmDialog({
      heading: 'Delete this file?',
      description: `This deletes "${path}" from disk. This can't be undone.`,
      confirmLabel: 'Delete',
    })
    if (!ok) return
    try {
      const res = await deleteEditorFile(path)
      if (!res.ok) throw new Error(await res.text())
      if (openTabs.some((t) => t.path === path)) removeTabNoConfirm(path)
      refreshTree()
      refreshGitStatus()
    } catch (err) {
      setTreeStatus(`Couldn't delete ${path}: ${err.message}`)
    }
  }

  async function handleDeleteFolder(path) {
    let fileCount = null
    try {
      const info = await fetchEditorFolderFileCount(path)
      fileCount = info.file_count
    } catch (err) {
      setTreeStatus(`Couldn't check "${path}": ${err.message}`)
      return
    }
    const ok = await confirmDialog({
      heading: 'Delete this folder?',
      description: `This deletes "${path}" and everything in it (${fileCount} file${fileCount === 1 ? '' : 's'}) from disk. This can't be undone.`,
      confirmLabel: 'Delete folder',
    })
    if (!ok) return
    try {
      const res = await deleteEditorFolder(path)
      if (!res.ok) throw new Error(await res.text())
      // Any open tabs that lived inside the deleted folder — same
      // close-if-affected behavior as handleDeleteFile, just matching a
      // path prefix across every open tab instead of one exact path,
      // since a whole subtree just disappeared, not one file.
      for (const t of openTabs) {
        if (t.path === path || t.path.startsWith(`${path}/`)) removeTabNoConfirm(t.path)
      }
      refreshTree()
      refreshGitStatus()
    } catch (err) {
      setTreeStatus(`Couldn't delete ${path}: ${err.message}`)
    }
  }

  async function handleRenameFile(fromPath, toPath) {
    try {
      const res = await renameEditorFile(fromPath, toPath)
      if (!res.ok) throw new Error(await res.text())
      if (openTabs.some((t) => t.path === fromPath)) {
        lintDiagnosticsByPath.current[toPath] = lintDiagnosticsByPath.current[fromPath]
        delete lintDiagnosticsByPath.current[fromPath]
        codeMirrorViewsByPath.current[toPath] = codeMirrorViewsByPath.current[fromPath]
        delete codeMirrorViewsByPath.current[fromPath]
        setOpenTabs((prev) => prev.map((t) => (t.path === fromPath ? { ...t, path: toPath } : t)))
        if (activeTabPath === fromPath) setActiveTabPath(toPath)
      }
      refreshTree()
      refreshGitStatus()
    } catch (err) {
      setTreeStatus(`Couldn't rename ${fromPath}: ${err.message}`)
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
    setDiffPath(entry.path)
    setDiffText('Loading…')
    try {
      const data = await fetchGitDiff(entry.path, !!entry.staged)
      setDiffText(data.diff || '(no changes)')
    } catch (err) {
      setDiffText(`Couldn't load the diff: ${err.message}`)
    }
  }

  async function handleStageSelected() {
    if (selectedPaths.size === 0) return
    setGitBusy(true)
    setGitBusyAction('stage')
    setGitError('')
    try {
      await stageGitPaths([...selectedPaths])
      setSelectedPaths(new Set())
      refreshGitStatus()
    } catch (err) {
      setGitError(err.message)
    } finally {
      setGitBusy(false)
      setGitBusyAction(null)
    }
  }

  async function handleStageAll() {
    if (unstaged.length === 0) return
    setGitBusy(true)
    setGitBusyAction('stage')
    setGitError('')
    try {
      await stageGitPaths(unstaged.map((entry) => entry.path))
      setSelectedPaths(new Set())
      refreshGitStatus()
    } catch (err) {
      setGitError(err.message)
    } finally {
      setGitBusy(false)
      setGitBusyAction(null)
    }
  }

  async function handleUnstageSelected() {
    if (selectedPaths.size === 0) return
    setGitBusy(true)
    setGitBusyAction('unstage')
    setGitError('')
    try {
      await unstageGitPaths([...selectedPaths])
      setSelectedPaths(new Set())
      refreshGitStatus()
    } catch (err) {
      setGitError(err.message)
    } finally {
      setGitBusy(false)
      setGitBusyAction(null)
    }
  }

  // Discards the selected unstaged entries — the one destructive,
  // no-undo action in this panel, so it goes through the same confirm
  // dialog as file/folder delete. Only ever applied to unstaged() entries
  // (see the "Discard selected" button below, which is disabled unless
  // the selection overlaps unstaged) — a staged-only path has nothing for
  // `git restore` (no --staged) to act on anyway. Split into two groups
  // since `git restore` only makes sense for a file git has already
  // tracked at some point: an untracked file (status "?") has no
  // committed/staged content to restore back to, so discarding one of
  // those means deleting it outright instead (same as the file tree's
  // own delete), not a git restore call.
  async function handleDiscardSelected() {
    const unstagedPaths = unstaged.filter((entry) => selectedPaths.has(entry.path))
    if (unstagedPaths.length === 0) return
    const trackedPaths = unstagedPaths.filter((entry) => entry.unstaged !== '?').map((entry) => entry.path)
    const untrackedPaths = unstagedPaths.filter((entry) => entry.unstaged === '?').map((entry) => entry.path)

    const count = trackedPaths.length + untrackedPaths.length
    const ok = await confirmDialog({
      heading: count === 1 ? 'Discard this change?' : `Discard ${count} changes?`,
      description:
        count === 1
          ? `This reverts "${trackedPaths[0] ?? untrackedPaths[0]}" to its last committed version (or deletes it, if it's a new file). This can't be undone.`
          : "This reverts each selected file to its last committed version (or deletes it, if it's a new file). This can't be undone.",
      confirmLabel: 'Discard changes',
    })
    if (!ok) return

    setGitBusy(true)
    setGitBusyAction('discard')
    setGitError('')
    try {
      if (trackedPaths.length > 0) {
        await discardGitPaths(trackedPaths)
      }
      for (const path of untrackedPaths) {
        const res = await deleteEditorFile(path)
        if (!res.ok) throw new Error(await res.text())
      }
      const discardedPaths = new Set([...trackedPaths, ...untrackedPaths])
      for (const t of openTabs) {
        if (!discardedPaths.has(t.path)) continue
        if (trackedPaths.includes(t.path)) {
          // Re-fetch so the tab shows its reverted content instead of the
          // discarded in-memory edit (reloadTabFromDisk, not openFile —
          // openFile treats an already-open path as "just activate it,"
          // which would leave the stale discarded content on screen).
          reloadTabFromDisk(t.path)
        } else {
          removeTabNoConfirm(t.path)
        }
      }
      setSelectedPaths((prev) => {
        const next = new Set(prev)
        for (const path of discardedPaths) next.delete(path)
        return next
      })
      refreshTree()
      refreshGitStatus()
    } catch (err) {
      setGitError(err.message)
    } finally {
      setGitBusy(false)
      setGitBusyAction(null)
    }
  }

  async function handleCommit(e) {
    e.preventDefault()
    if (!commitMessage.trim()) return
    setGitBusy(true)
    setGitBusyAction('commit')
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
      setGitBusyAction(null)
    }
  }

  // Push is a separate, explicit action from Commit — never fired
  // automatically after a commit. A plain `git push`: if the remote has
  // diverged, this fails and the error (surfaced via gitError) is shown
  // as-is rather than silently force-pushing or resolving it any way. The
  // one exception is "this branch has never been pushed before" (no
  // upstream configured) — the backend flags that specific, unambiguous
  // case with { no_upstream: true } (see internal/chat.EditorGitPush), so
  // this offers a "Set upstream & push" button rather than only showing
  // the raw git error and leaving the fix to a terminal.
  async function handlePush() {
    setPushing(true)
    setGitError('')
    setPushStatus('')
    setPushNeedsUpstream(false)
    try {
      const res = await pushGit()
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        if (body?.no_upstream) {
          setGitError(body.error)
          setPushNeedsUpstream(true)
          return
        }
        throw new Error(body?.error ?? (await res.text().catch(() => res.statusText)))
      }
      setPushStatus('Pushed.')
    } catch (err) {
      setGitError(`Couldn't push: ${err.message}`)
    } finally {
      setPushing(false)
    }
  }

  async function handlePushSetUpstream() {
    setPushing(true)
    setGitError('')
    setPushStatus('')
    try {
      const res = await pushSetUpstreamGit()
      if (!res.ok) throw new Error(await res.text())
      setPushNeedsUpstream(false)
      setPushStatus('Pushed, and set as the upstream for this branch.')
    } catch (err) {
      setGitError(`Couldn't push: ${err.message}`)
    } finally {
      setPushing(false)
    }
  }

  // Every open tab mounts its own persistent CodeMirror instance (so
  // switching tabs preserves undo history/scroll/selection instead of
  // rebuilding the editor each time — see the tab strategy note by
  // openTabs above), which means each tab needs its OWN linter source
  // reading only ITS OWN path's diagnostics out of lintDiagnosticsByPath,
  // not one shared extension array like the single-file version had.
  // severity is used as-is — the backend (internal/lint.normalizeSeverity)
  // already collapses oxlint's full severity vocabulary down to exactly
  // "error"/"warning" before it ever reaches here, so this doesn't need
  // its own copy of that mapping.
  function oxlintExtensionFor(path) {
    return [
      linter(() =>
        (lintDiagnosticsByPath.current[path] ?? []).map((d) => ({
          from: Math.max(0, d.offset),
          to: Math.max(d.offset, d.offset + d.length),
          severity: d.severity,
          message: d.message,
          source: d.rule,
        }))
      ),
      lintGutter(),
    ]
  }
  // Built fresh per tab (not memoized across tabs — each path needs its
  // own aiCompletionExtension instance so one tab's ViewPlugin.destroy()
  // on close can't cancel another tab's in-flight suggestion). Gated on
  // canWrite the same way Save is — completion is pointless in a
  // read-only file access configuration.
  function languageExtensionsFor(path) {
    return [...languageExtensionFor(path), ...oxlintExtensionFor(path), ...aiCompletionExtension(languageNameFor(path), canWrite)]
  }
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
      {!sidebarCollapsed && (
      <aside
        ref={sidebarRef}
        className="editor-sidebar"
        style={{ width: sidebarWidth }}
      >
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
              onDeleteFolder={handleDeleteFolder}
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
                {searching ? 'Searching…' : 'Go'}
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
                  {creatingBranch ? 'Creating…' : 'Create'}
                </button>
              </form>
            )}

            {gitError && <p className="editor-error">{gitError}</p>}

            <div className="editor-git-actions">
              <button
                type="button"
                onClick={handleStageAll}
                disabled={gitBusy || unstaged.length === 0}
                title="Stage every unstaged change"
              >
                {gitBusy && gitBusyAction === 'stage' && selectedPaths.size === 0 ? 'Staging…' : 'Stage all'}
              </button>
              <button
                type="button"
                onClick={handleStageSelected}
                disabled={gitBusy || selectedPaths.size === 0}
                title="Stage the selected files"
              >
                {gitBusy && gitBusyAction === 'stage' && selectedPaths.size > 0 ? 'Staging…' : 'Stage'}
              </button>
              <button
                type="button"
                onClick={handleUnstageSelected}
                disabled={gitBusy || selectedPaths.size === 0}
                title="Unstage the selected files"
              >
                {gitBusy && gitBusyAction === 'unstage' ? 'Unstaging…' : 'Unstage'}
              </button>
              <button
                type="button"
                className="btn-danger-outline"
                onClick={handleDiscardSelected}
                disabled={gitBusy || !unstaged.some((entry) => selectedPaths.has(entry.path))}
                title="Revert selected unstaged changes to their last committed version"
              >
                {gitBusy && gitBusyAction === 'discard' ? 'Discarding…' : 'Discard'}
              </button>
            </div>

            <form onSubmit={handleCommit} className="editor-commit-form">
              <textarea
                value={commitMessage}
                onChange={(e) => setCommitMessage(e.target.value)}
                placeholder="Commit message"
                rows={2}
              />
              <div className="editor-commit-actions">
                <button type="submit" disabled={gitBusy || !commitMessage.trim() || staged.length === 0}>
                  {gitBusy && gitBusyAction === 'commit'
                    ? 'Committing…'
                    : `Commit ${staged.length > 0 ? `(${staged.length})` : ''}`}
                </button>
                <button type="button" onClick={handlePush} disabled={pushing} title="Push to the remote">
                  {pushing ? 'Pushing…' : 'Push'}
                </button>
                {pushNeedsUpstream && (
                  <button
                    type="button"
                    className="editor-push-upstream"
                    onClick={handlePushSetUpstream}
                    disabled={pushing}
                    title="This branch has no upstream yet — push and set one"
                  >
                    {pushing ? 'Pushing…' : 'Set upstream & push'}
                  </button>
                )}
              </div>
              {pushStatus && <span className="editor-push-status">{pushStatus}</span>}
            </form>

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
      )}

      {!sidebarCollapsed && (
        <div className="editor-sidebar-divider" onMouseDown={handleSidebarDragStart} />
      )}

      <main className="editor-main" ref={mainColumnRef}>
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
                      onClick={() => {
                        setDiffPath(null)
                        setActiveTabPath(t.path)
                      }}
                    >
                      {t.path.split(/[/\\]/).pop()}
                      {tabDirty && <span className="editor-tab-dirty">●</span>}
                    </button>
                    <button
                      type="button"
                      className="editor-tab-close"
                      title="Close"
                      onClick={() => closeTab(t.path)}
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
              <button
                type="button"
                className={`editor-save-button${saving ? ' is-saving' : ''}`}
                onClick={handleSave}
                disabled={!canWrite || !dirty || saving}
                title={canWrite ? '' : 'File writes are disabled in Settings → File access'}
              >
                {saving ? 'Saving…' : dirty ? 'Save' : 'Saved'}
              </button>
            </div>
          )}
          {/* Every open tab keeps its own CodeMirror instance mounted (just
              hidden) at all times, the same always-mounted-but-hidden
              pattern the Terminal tabs below use — switching tabs must not
              tear down and rebuild the editor, or undo history/scroll
              position/selection would all reset on every switch. */}
          {openTabs.map((t) => (
            <div
              key={t.path}
              className="editor-codemirror"
              style={{ display: t.path === activeTabPath && !diffPath ? 'block' : 'none' }}
              onKeyDown={handleEditorKeyDown}
            >
              <CodeMirror
                value={t.content}
                height="100%"
                theme={theme === 'light' ? githubLight : githubDark}
                extensions={languageExtensionsFor(t.path)}
                onChange={(value) =>
                  setOpenTabs((prev) => prev.map((x) => (x.path === t.path ? { ...x, content: value } : x)))
                }
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
                <button type="button" className="editor-diff-close" onClick={() => setDiffPath(null)}>Close</button>
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
              <p>Select a file to open it.</p>
            </div>
          ) : null}
        </div>

        {!terminalCollapsed && (
          <div className="terminal-panel-divider" onMouseDown={handleTerminalDragStart} />
        )}

        <div
          className={`terminal-panel ${terminalCollapsed ? 'is-collapsed' : ''}`}
          style={{ flex: terminalCollapsed ? '0 0 auto' : `0 0 ${terminalPanelHeight}px` }}
        >
          <div className="terminal-panel-header">
            {terminalEnabled && !terminalCollapsed ? (
              <div className="terminal-tabs">
                {terminalTabs.map((id, index) => (
                  <div
                    key={id}
                    className={`terminal-tab ${id === activeTerminalTab ? 'is-active' : ''}`}
                  >
                    <button
                      type="button"
                      className="terminal-tab-label"
                      onClick={() => setActiveTerminalTab(id)}
                    >
                      Terminal {index + 1}
                    </button>
                    <button
                      type="button"
                      className="terminal-tab-close"
                      title="Close terminal"
                      onClick={() => closeTerminalTab(id)}
                    >
                      ✕
                    </button>
                  </div>
                ))}
                <button
                  type="button"
                  className="terminal-tab-add"
                  title="New terminal"
                  onClick={addTerminalTab}
                >
                  +
                </button>
              </div>
            ) : (
              <span className="terminal-panel-title">Terminal</span>
            )}
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
              everVisible &&
              terminalTabs.map((id) => (
                <div key={id} style={{ display: id === activeTerminalTab ? 'contents' : 'none' }}>
                  <TerminalView theme={theme} folderRoot={fileAccessSettings?.root} />
                </div>
              ))
            ) : (
              <div className="terminal-disabled-state">
                <p>Terminal is disabled. Enable it in Settings → Terminal.</p>
              </div>
            )}
          </div>
        </div>
      </main>

      {pendingConfirm && (
        <ConfirmDeleteModal
          heading={pendingConfirm.heading}
          description={pendingConfirm.description}
          confirmLabel={pendingConfirm.confirmLabel}
          onCancel={() => resolvePendingConfirm(false)}
          onConfirm={() => resolvePendingConfirm(true)}
        />
      )}
    </div>
  )
}
