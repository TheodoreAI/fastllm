import { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
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
import { LspClient } from '../lsp'
import {
  lspLintExtensionFor,
  lspCompletionExtensionFor,
  lspHoverExtensionFor,
  lspDefinitionExtensionFor,
  positionToOffset,
} from '../lspExtensions'
import { EditorSelection } from '@codemirror/state'
import { useEditorSidebarWidth } from '../useEditorSidebarWidth'
import FileTree from './FileTree'
import GitPanel from './GitPanel'
import SearchPanel from './SearchPanel'
import TestPanel from './TestPanel'
import NotesPanel from './NotesPanel'
import EditorPane from './EditorPane'
import ConfirmDeleteModal from './ConfirmDeleteModal'

// didChange debounce — mirrors aiCompletion.js's DEBOUNCE_MS, just scoped
// per open .go path here (see lspChangeTimers) since several tabs can be
// mid-edit independently.
const LSP_CHANGE_DEBOUNCE_MS = 400

function isGoFile(path) {
  return path.endsWith('.go')
}

// Terminal used to live nested inside this component (a bottom-docked
// panel under the file tabs) — it's now its own top-level pane in App.jsx,
// reorderable alongside Editor and Chat (see usePaneSlots), so none of its
// state/handlers live here anymore.
//
// Mounted exactly once, unconditionally, regardless of where Editor sits
// in App.jsx's pane grid — its own DOM output is entirely two portals
// (see the return below) rather than a normal inline render, so this
// component's state (open tabs, git status, the SSE git-watch
// subscription) survives Editor being dragged between slots or collapsed
// instead of remounting and losing it. `sidebarContainer` and
// `mainContainer` are DOM nodes App.jsx hands down (they can be the same
// node, when Editor is off alone in a slot with no column-mate, or two
// different nodes — the sidebar spanning the full height of a shared left
// column next to Terminal, mirroring a conventional IDE's file explorer —
// see App.jsx's renderPanes for which). Both are null for one initial
// render (App.jsx hasn't measured a DOM node to portal into yet), so nothing
// is portaled that render — not a bug, just a one-frame gap before content
// appears.
//
// Wrapped in forwardRef so App.jsx's QuickOpen (Ctrl/Cmd+P) can call
// openFile directly — App.jsx has no other way to reach into this
// always-mounted-but-often-hidden component's tab state, same reasoning as
// openFolderSignal above for the folder picker. onTreeChange bubbles the
// current file list up the same way onOpenPathChange/onGitChangeCountChange
// already bubble other internal state, so App.jsx doesn't need its own
// separate /api/editor/tree fetch.
const EditorView = forwardRef(function EditorView(
  {
    fileAccessSettings,
    onFileAccessSettingsChange,
    theme,
    terminalEnabled,
    openFolderSignal,
    onOpenSettings,
    panel,
    onPanelChange,
    onGitChangeCountChange,
    onOpenPathChange,
    onTreeChange,
    sidebarCollapsed,
    onSidebarCollapsedChange,
    sidebarContainer,
    mainContainer,
  },
  ref
) {
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
  // gopls diagnostics, keyed by path the same way lintDiagnosticsByPath
  // is — a separate ref (not merged into lintDiagnosticsByPath) since the
  // two sources have different shapes (line/character ranges here vs.
  // byte offset/length for oxlint) and lspLintExtensionFor reads this one
  // directly (see languageExtensionsFor below).
  const lspDiagnosticsByPath = useRef({})
  // didChange's version counter, per open .go file — LSP requires a
  // monotonically increasing version per textDocument/didChange.
  const lspVersionByPath = useRef({})
  // Debounce timers for didChange, keyed by path — mirrors
  // aiCompletion.js's DEBOUNCE_MS pattern, just per-path here since
  // multiple .go tabs can be mid-edit independently.
  const lspChangeTimers = useRef({})
  // Stable per-path completion source functions for
  // lspCompletionExtensionFor — see its doc comment: @codemirror/
  // autocomplete correlates an in-flight query by the source function's
  // own reference identity, so this cache is what keeps that reference
  // stable across re-renders instead of a fresh closure every keystroke.
  const lspCompletionSourceByPath = useRef({})
  // One LspClient shared across every open .go tab — there is only ever
  // one gopls process for the whole opened project (see internal/lsp),
  // so this is a single ref, not per-path like the ones above.
  const lspClientRef = useRef(null)
  const [saving, setSaving] = useState(false)

  const [query, setQuery] = useState('')
  const [searchResults, setSearchResults] = useState([])
  const [searching, setSearching] = useState(false)

  const [gitStatus, setGitStatus] = useState([])
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
  const sidebarRef = useRef(null)

  const enabled = !!fileAccessSettings?.read_enabled
  const canWrite = !!fileAccessSettings?.write_enabled
  const dirty = content !== savedContent

  // The three toggles a fresh install needs before the Editor/Terminal tab
  // does anything useful — ordered to match their real dependency, not
  // just listed alphabetically-by-feature: Settings' own "Allow reading
  // files" checkbox is disabled until a root is set (see SettingsPanel's
  // fileAccessRootEmpty), so picking a folder always has to come before
  // file access can actually be turned on. Terminal has no such
  // dependency on the other two, so it stays last. Surfaced as a
  // checklist in the empty-file-tree state (see editor-empty-state below)
  // rather than a blocking first-run wizard: it's just the same "select a
  // file" placeholder every other empty state already shows, made useful
  // for exactly as long as setup is actually incomplete, then it goes
  // back to being the plain placeholder — no new persisted "onboarding
  // done" flag to invent or reset.
  const setupSteps = [
    { key: 'folder', label: 'Open a folder', done: !!fileAccessSettings?.root, action: handleOpenFolder },
    { key: 'fileAccess', label: 'Allow file access', done: enabled, action: () => onOpenSettings?.('fileAccess') },
    { key: 'terminal', label: 'Enable the terminal', done: !!terminalEnabled, action: () => onOpenSettings?.('terminal') },
  ]
  const setupComplete = setupSteps.every((step) => step.done)
  const setupChecklist = (
    <div className="editor-setup-checklist">
      <p className="editor-setup-checklist-title">Get set up</p>
      <ul>
        {setupSteps.map((step) => (
          <li key={step.key} className={step.done ? 'is-done' : ''}>
            <span className="editor-setup-checklist-mark">{step.done ? '✓' : ''}</span>
            <span className="editor-setup-checklist-label">{step.label}</span>
            {!step.done && (
              <button type="button" className="btn-secondary" onClick={step.action}>
                {step.key === 'folder' ? 'Choose…' : 'Open Settings'}
              </button>
            )}
          </li>
        ))}
      </ul>
    </div>
  )

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

  useEffect(() => {
    onTreeChange?.(tree)
  }, [tree, onTreeChange])

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
  // click). App.jsx initializes it to null specifically so "never
  // requested" is a real, distinguishable value here — comparing against
  // null (rather than a ref that has to remember "was this the first
  // render") stays correct across React StrictMode's dev-only double
  // mount/unmount and Vite HMR, neither of which should re-arm a ref-based
  // guard but both of which have, in practice, popped this dialog on
  // launch when the guard was a ref.
  useEffect(() => {
    if (openFolderSignal == null) return
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
      return data.content
    } catch (err) {
      setOpenTabs((prev) =>
        prev.map((t) => (t.path === path ? { ...t, fileStatus: `Couldn't open this file: ${err.message}` } : t))
      )
      return undefined
    }
  }

  function handleLspDiagnostics(path, diagnostics) {
    lspDiagnosticsByPath.current[path] = diagnostics
    const view = codeMirrorViewsByPath.current[path]
    if (view) forceLinting(view)
  }

  // Lazy-start trigger for the whole LSP feature: the backend's WS handler
  // (internal/lsp/handler.go) is unconditionally eager once dialed — it's
  // this call, made only the first time a .go tab is actually opened, that
  // decides whether gopls ever gets spawned at all for this session. A
  // 404 (no gopls on PATH) or any connect failure must never throw into
  // the tab-open flow — a Go file with no language server behaves exactly
  // like any other file.
  async function ensureLspOpen(path, content) {
    if (!lspClientRef.current) {
      const client = new LspClient({ onDiagnostics: handleLspDiagnostics, root: fileAccessSettings.root })
      try {
        await client.connect()
      } catch {
        return
      }
      lspClientRef.current = client
    }
    lspVersionByPath.current[path] = 1
    lspClientRef.current.didOpen(path, content)
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
    const content = await loadFileIntoTab(path)
    if (content != null && isGoFile(path)) {
      await ensureLspOpen(path, content)
    }
  }

  // Opens (or activates) path, then scrolls the given gopls
  // line/character position into view — the go-to-definition target for
  // lspDefinitionExtensionFor (see languageExtensionsFor below). A
  // same-file jump ends up calling this too; openFile's "already open"
  // branch just activates the existing tab, so this still works.
  async function jumpToDefinition(path, line, character) {
    await openFile(path)
    // The target tab's CodeMirror instance may not have mounted yet (a
    // brand-new tab's onCreateEditor fires after openFile's awaited work
    // above resolves) — poll briefly rather than assuming it's ready.
    for (let attempt = 0; attempt < 40; attempt++) {
      const view = codeMirrorViewsByPath.current[path]
      if (view) {
        const offset = positionToOffset(view.state.doc, { line, character })
        view.dispatch({ selection: EditorSelection.cursor(offset), scrollIntoView: true })
        view.focus()
        return
      }
      await new Promise((resolve) => setTimeout(resolve, 25))
    }
  }

  // No deps array: openFile is a plain function redefined every render (it
  // closes over openTabs), so there's nothing stable to list — this just
  // means the exposed handle always calls the latest openFile, which is
  // exactly what's wanted here.
  useImperativeHandle(ref, () => ({ openFile }))

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
    closeLspDocument(path)
    setOpenTabs((prev) => {
      const next = prev.filter((t) => t.path !== path)
      if (path === activeTabPath) {
        setActiveTabPath(next.length > 0 ? next[next.length - 1].path : null)
      }
      return next
    })
  }

  // Shared didClose/cleanup for one path — called from both closeTab and
  // removeTabNoConfirm below, since both remove a tab, just with
  // different confirmation behavior.
  function closeLspDocument(path) {
    if (lspChangeTimers.current[path]) clearTimeout(lspChangeTimers.current[path])
    delete lspChangeTimers.current[path]
    delete lspDiagnosticsByPath.current[path]
    delete lspVersionByPath.current[path]
    delete lspCompletionSourceByPath.current[path]
    lspClientRef.current?.didClose(path)
  }

  // Used where an operation invalidates every open file at once (folder
  // switch, branch switch) — callers are responsible for confirming with
  // anyDirty/confirmDiscardAllChanges first, since a per-tab confirm here
  // would mean answering one popup per open tab.
  //
  // Unlike closeTab/removeTabNoConfirm's per-path didClose, the whole LSP
  // session is just closed outright here rather than looped over every
  // open tab: gopls's workspace root is fixed at process spawn (like
  // TerminalView's PTY cwd), so a folder switch makes the entire session
  // stale regardless of which files were open. It reconnects lazily the
  // next time a .go file is opened (see ensureLspOpen).
  function closeAllTabs() {
    lintDiagnosticsByPath.current = {}
    codeMirrorViewsByPath.current = {}
    for (const timer of Object.values(lspChangeTimers.current)) clearTimeout(timer)
    lspChangeTimers.current = {}
    lspDiagnosticsByPath.current = {}
    lspVersionByPath.current = {}
    lspCompletionSourceByPath.current = {}
    lspClientRef.current?.close()
    lspClientRef.current = null
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
    closeLspDocument(path)
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
      const renamedTab = openTabs.find((t) => t.path === fromPath)
      if (renamedTab) {
        lintDiagnosticsByPath.current[toPath] = lintDiagnosticsByPath.current[fromPath]
        delete lintDiagnosticsByPath.current[fromPath]
        codeMirrorViewsByPath.current[toPath] = codeMirrorViewsByPath.current[fromPath]
        delete codeMirrorViewsByPath.current[fromPath]
        // gopls tracks documents by URI — a rename of an open file needs
        // its own didClose(old)/didOpen(new), same as closing one tab and
        // opening another, or gopls would keep reporting diagnostics
        // against a path that no longer exists.
        if (isGoFile(fromPath) && lspClientRef.current) {
          lspClientRef.current.didClose(fromPath)
        }
        lspDiagnosticsByPath.current[toPath] = lspDiagnosticsByPath.current[fromPath]
        delete lspDiagnosticsByPath.current[fromPath]
        delete lspVersionByPath.current[fromPath]
        delete lspCompletionSourceByPath.current[fromPath]
        if (lspChangeTimers.current[fromPath]) clearTimeout(lspChangeTimers.current[fromPath])
        delete lspChangeTimers.current[fromPath]
        setOpenTabs((prev) => prev.map((t) => (t.path === fromPath ? { ...t, path: toPath } : t)))
        if (activeTabPath === fromPath) setActiveTabPath(toPath)
        if (isGoFile(toPath) && lspClientRef.current) {
          ensureLspOpen(toPath, renamedTab.content)
        }
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

  // Each row's checkbox IS the stage toggle now (checked = staged),
  // rather than a separate "select these, then click a bulk-action
  // button" mechanism — one click does what used to take a select-then-
  // act pair, and it's the same direct-manipulation pattern GitHub
  // Desktop uses for the same list.
  async function handleToggleStage(path, currentlyStaged) {
    setGitBusy(true)
    setGitBusyAction(currentlyStaged ? 'unstage' : 'stage')
    setGitError('')
    try {
      if (currentlyStaged) await unstageGitPaths([path])
      else await stageGitPaths([path])
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
      refreshGitStatus()
    } catch (err) {
      setGitError(err.message)
    } finally {
      setGitBusy(false)
      setGitBusyAction(null)
    }
  }

  async function handleUnstageAll() {
    if (staged.length === 0) return
    setGitBusy(true)
    setGitBusyAction('unstage')
    setGitError('')
    try {
      await unstageGitPaths(staged.map((entry) => entry.path))
      refreshGitStatus()
    } catch (err) {
      setGitError(err.message)
    } finally {
      setGitBusy(false)
      setGitBusyAction(null)
    }
  }

  // Discards unstaged entries — the one destructive, no-undo action in
  // this panel, so it goes through the same confirm dialog as file/folder
  // delete. Only ever applied to unstaged() entries — a staged-only path
  // has nothing for `git restore` (no --staged) to act on anyway. Split
  // into two groups since `git restore` only makes sense for a file git
  // has already tracked at some point: an untracked file (status "?") has
  // no committed/staged content to restore back to, so discarding one of
  // those means deleting it outright instead (same as the file tree's own
  // delete), not a git restore call.
  //
  // Takes an explicit path list — each row's own Discard action passes
  // just its own path, scoping the confirm dialog and the revert to that
  // one file.
  async function handleDiscardPath(paths) {
    const unstagedPaths = unstaged.filter((entry) => paths.includes(entry.path))
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
    const base = [...languageExtensionFor(path), ...oxlintExtensionFor(path), ...aiCompletionExtension(languageNameFor(path), canWrite)]
    if (!isGoFile(path) || !lspClientRef.current) return base
    return [
      ...base,
      ...lspLintExtensionFor(path, lspDiagnosticsByPath),
      lspCompletionExtensionFor(path, lspClientRef, flushLspChange, lspCompletionSourceByPath),
      lspHoverExtensionFor(path, lspClientRef, flushLspChange),
      ...lspDefinitionExtensionFor(path, lspClientRef, jumpToDefinition, flushLspChange),
    ]
  }

  // didChange, debounced per path (see LSP_CHANGE_DEBOUNCE_MS) so typing
  // doesn't send a WS message per keystroke — mirrors aiCompletion.js's
  // own debounce for the same reason.
  function handleTabContentChange(path, value) {
    setOpenTabs((prev) => prev.map((x) => (x.path === path ? { ...x, content: value } : x)))
    if (!isGoFile(path) || !lspClientRef.current) return
    if (lspChangeTimers.current[path]) clearTimeout(lspChangeTimers.current[path])
    lspChangeTimers.current[path] = setTimeout(() => {
      sendLspChange(path, value)
    }, LSP_CHANGE_DEBOUNCE_MS)
  }

  function sendLspChange(path, text) {
    delete lspChangeTimers.current[path]
    const version = (lspVersionByPath.current[path] ?? 1) + 1
    lspVersionByPath.current[path] = version
    lspClientRef.current?.didChange(path, text, version)
  }

  // Completion/hover/go-to-definition are all "answer this right now"
  // requests, unlike didChange's debounced background sync — if a
  // debounced edit is still pending when one of them fires (typing
  // "strings." and immediately wanting completions, well inside the
  // 400ms window), gopls would answer against whatever content it last
  // received, not what's actually on screen (observed as gopls's own
  // "column is beyond end of line" error during testing). Cancelling the
  // pending timer and sending the current buffer synchronously first
  // guarantees gopls is caught up before the request that needs the
  // answer is sent — WS delivery order guarantees gopls processes the
  // didChange notification before the following request (see
  // internal/lsp/handler.go's Forward, called once per frame in order).
  function flushLspChange(path, view) {
    if (!isGoFile(path) || !lspClientRef.current) return
    if (lspChangeTimers.current[path]) clearTimeout(lspChangeTimers.current[path])
    sendLspChange(path, view.state.doc.toString())
  }
  const staged = gitStatus.filter((s) => s.staged)
  const unstaged = gitStatus.filter((s) => s.unstaged)

  // Drags the sidebar's right-edge divider to resize the Files/Search/Git
  // panel horizontally. Tracks the cursor against the sidebar's own left
  // edge (rather than delta movement) so a fast drag can't desync from
  // the cursor. Clamped so the panel can't be dragged to nothing or to
  // swallow the whole editor.
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

  const sidebar = !sidebarCollapsed && (
    <>
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
            {/* A static label matching whichever panel the left rail's
                Files/Search/Git buttons (see App.jsx) currently has
                selected — not a clickable switcher itself, since that
                selection already lives there; this used to hardcode
                "Files" regardless of panel, which was wrong the moment
                Search or Git was actually the one showing below it. */}
            <div className="editor-panel-tabs">
              <span className="editor-panel-tab-label">
                {panel === 'search'
                  ? 'Search'
                  : panel === 'git'
                    ? 'Git'
                    : panel === 'tests'
                      ? 'Tests'
                      : panel === 'notes'
                        ? 'Notes'
                        : 'Files'}
              </span>
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
          <SearchPanel
            query={query}
            onQueryChange={setQuery}
            onSubmit={runSearch}
            searching={searching}
            searchResults={searchResults}
            onOpenFile={openFile}
          />
        )}

        {panel === 'git' && (
          <GitPanel
            branches={branches}
            branchBusy={branchBusy}
            branchError={branchError}
            onSwitchBranch={handleSwitchBranch}
            newBranchOpen={newBranchOpen}
            onToggleNewBranch={() => setNewBranchOpen((v) => !v)}
            newBranchName={newBranchName}
            onNewBranchNameChange={setNewBranchName}
            onCreateBranch={handleCreateBranch}
            creatingBranch={creatingBranch}
            gitStatus={gitStatus}
            staged={staged}
            unstaged={unstaged}
            gitBusy={gitBusy}
            gitBusyAction={gitBusyAction}
            gitError={gitError}
            onToggleStage={handleToggleStage}
            onStageAll={handleStageAll}
            onUnstageAll={handleUnstageAll}
            onDiscardPath={handleDiscardPath}
            onViewDiff={viewDiff}
            commitMessage={commitMessage}
            onCommitMessageChange={setCommitMessage}
            onCommit={handleCommit}
            pushing={pushing}
            pushStatus={pushStatus}
            pushNeedsUpstream={pushNeedsUpstream}
            onPush={handlePush}
            onPushSetUpstream={handlePushSetUpstream}
          />
        )}

        {panel === 'tests' && <TestPanel />}
        {panel === 'notes' && <NotesPanel />}
      </aside>

      <div className="editor-sidebar-divider" onMouseDown={handleSidebarDragStart} />
    </>
  )

  const main = !enabled ? (
    <div className="editor-view-empty editor-main">{setupChecklist}</div>
  ) : (
    <main className="editor-main">
      <EditorPane
        openTabs={openTabs}
        activeTabPath={activeTabPath}
        diffPath={diffPath}
        onSelectTab={(path) => {
          setDiffPath(null)
          setActiveTabPath(path)
        }}
        onCloseTab={closeTab}
        openPath={openPath}
        fileStatus={fileStatus}
        canWrite={canWrite}
        dirty={dirty}
        saving={saving}
        onSave={handleSave}
        theme={theme}
        languageExtensionsFor={languageExtensionsFor}
        onEditorKeyDown={handleEditorKeyDown}
        onTabContentChange={handleTabContentChange}
        codeMirrorViewsByPath={codeMirrorViewsByPath}
        diffLines={diffLines}
        onCloseDiff={() => setDiffPath(null)}
        setupComplete={setupComplete}
        setupChecklist={setupChecklist}
      />
    </main>
  )

  return (
    <>
      {/* No sidebar at all while file access is off — there's nothing to
          browse yet, and the checklist that explains why takes over the
          main portal instead (see `main` above). */}
      {enabled && sidebarContainer && createPortal(sidebar, sidebarContainer)}
      {mainContainer && createPortal(main, mainContainer)}
      {pendingConfirm && (
        <ConfirmDeleteModal
          heading={pendingConfirm.heading}
          description={pendingConfirm.description}
          confirmLabel={pendingConfirm.confirmLabel}
          onCancel={() => resolvePendingConfirm(false)}
          onConfirm={() => resolvePendingConfirm(true)}
        />
      )}
    </>
  )
})

export default EditorView
