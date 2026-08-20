import { useEffect, useRef, useState } from 'react'
import './App.css'
import ConversationList from './components/ConversationList'
import ModelPicker from './components/ModelPicker'
import SkillPanel from './components/SkillPanel'
import KnowledgeBasePanel from './components/KnowledgeBasePanel'
import ChatPanel from './components/ChatPanel'
import EditorView from './components/EditorView'
import TerminalPane from './components/TerminalPane'
import ConfirmDeleteModal from './components/ConfirmDeleteModal'
import SettingsPanel from './components/SettingsPanel'
import ScreenshotPreviewModal from './components/ScreenshotPreviewModal'
import DraggableSection from './components/DraggableSection'
import SectionIcon from './components/SectionIcon'
import TweakBar from './components/TweakBar'
import TopBar from './components/TopBar'
import { useTheme } from './useTheme'
import { useFontFamily } from './useFontFamily'
import { useFontScale } from './useFontScale'
import { useSectionOrder } from './useSectionOrder'
import { useSidebarCollapsed } from './useSidebarCollapsed'
import { useEditorSidebarCollapsed } from './useEditorSidebarCollapsed'
import { useEditorPaneCollapsed } from './useEditorPaneCollapsed'
import { useTerminalPaneCollapsed } from './useTerminalPaneCollapsed'
import { usePaneSlots } from './usePaneSlots'
import { usePaneGridSizes } from './usePaneGridSizes'
import { useConnectionStatus } from './useConnectionStatus'
import { useModel } from './useModel'
import { useSkillId } from './useSkillId'
import { useEditorPanel } from './useEditorPanel'
import {
  fetchConversations,
  fetchMessages,
  deleteConversation,
  fetchDocuments,
  fetchSkills,
  fetchModels,
  fetchSettings,
  fetchRagSettings,
  saveRagSettings,
  fetchFileAccessSettings,
  saveFileAccessSettings,
  fetchTerminalSettings,
  saveTerminalSettings,
  fetchCloudProviderSettings,
  saveCloudProviderSettings,
  fetchEditorSettings,
  saveEditorSettings,
  clearKnowledgeBase,
  clearConversations,
  createSkill as apiCreateSkill,
  deleteSkillById,
  indexDocument,
  uploadFile,
  streamChat,
  quitServer,
  isWails,
  captureScreenshot,
  approveWrite,
  rejectWrite,
} from './api'

// Files we accept for upload: plain-text-like formats (indexed as-is,
// client never needs to read their bytes) plus PDF (extracted server-side).
const UPLOAD_FILE_PATTERN = /\.(txt|md|markdown|mdx|json|ya?ml|csv|tsv|log|go|js|jsx|ts|tsx|py|rb|java|c|cc|cpp|h|hpp|rs|sh|sql|html|css|xml|pdf)$/i

// Directories to skip entirely on a folder upload — dependency/build/VCS
// output that's typically huge, low-value to index, and would otherwise
// slip through UPLOAD_FILE_PATTERN anyway since e.g. node_modules is full
// of .js/.json/.md files that individually look legitimate. Matched
// against any path segment in webkitRelativePath, not just the folder
// root, so a nested node_modules (or a vendored copy) is skipped too.
const UPLOAD_EXCLUDED_DIRS = new Set([
  'node_modules',
  '.yarn', // yarn's own cache/unplugged storage under the repo, not source
  '.git',
  '.hg',
  '.svn',
  'dist',
  'build',
  'out',
  '.next',
  '.nuxt',
  '.turbo',
  '.cache',
  'coverage',
  'venv',
  '.venv',
  '__pycache__',
  'target', // Rust/Java build output
  'vendor', // Go/PHP dependency vendoring
  '.dart_tool', // Dart/Flutter
  '.pub-cache', // Dart/Flutter
  '.stack-work', // Haskell (stack)
  'dist-newstyle', // Haskell (cabal)
  '.idea', // JetBrains project metadata
  '.vs', // Visual Studio project metadata
  'obj', // MSBuild intermediate output (C++/.NET)
  '.react-router', // React Router v7 codegen (route types/manifests), not source
  '.svelte-kit', // SvelteKit build/codegen output
  '.vercel', // Vercel deployment output/cache
  '.netlify', // Netlify deployment output/cache
  '.wrangler', // Cloudflare Workers local dev/build output
])

function isInExcludedDir(relativePath) {
  const segments = relativePath.split(/[/\\]/)
  return segments.some((segment) => UPLOAD_EXCLUDED_DIRS.has(segment))
}

// Lockfiles are the single worst case for folder upload: a single
// package-lock.json can be several hundred KB of near-random dependency
// hashes, which at the default 800-character chunk size chunks into the
// hundreds — each chunk needs its own sequential embedding call (see
// indexText in internal/chat/handler.go), so one lockfile can dominate an
// entire folder upload's total time while contributing nothing anyone
// would ever semantically search for. Matched by exact filename rather
// than extension, since most of these are .json/.lock/.toml — formats
// that are otherwise perfectly legitimate to index.
const UPLOAD_EXCLUDED_FILENAMES = new Set([
  'package-lock.json',
  'yarn.lock',
  'pnpm-lock.yaml',
  'bun.lockb',
  'bun.lock',
  'go.sum',
  'Cargo.lock',
  'poetry.lock',
  'Pipfile.lock',
  'composer.lock',
  'mix.lock',
  'Gemfile.lock',
  'pubspec.lock', // Dart/Flutter
  'stack.yaml.lock', // Haskell
  'cabal.project.freeze', // Haskell
])

function isExcludedFilename(filename) {
  return UPLOAD_EXCLUDED_FILENAMES.has(filename)
}

const DEFAULT_SECTION_ORDER = ['conversations', 'model', 'skills', 'knowledge']

// The fixed grid this app now uses for Editor/Terminal/Chat: top-left and
// bottom-left stacked on the left, one full-height slot on the right — see
// usePaneSlots and App.jsx's renderPanes.
const DEFAULT_SLOT_ASSIGNMENT = { 'top-left': 'editor', 'bottom-left': 'terminal', right: 'chat' }

const PANE_LABELS = { editor: 'Editor', terminal: 'Terminal', chat: 'Chat' }

const SECTION_LABELS = {
  conversations: 'Conversations',
  model: 'Model',
  skills: 'Skills',
  knowledge: 'Knowledge base',
}

export default function App() {
  const [messages, setMessages] = useState([])
  const [messagesLoading, setMessagesLoading] = useState(false)
  const [input, setInput] = useState('')
  // Images pasted/dropped into the composer, waiting to be sent with the
  // next message — [{ dataUri, name }]. Cleared on send, same lifecycle
  // as `input`. Not persisted (unlike model/skill/theme): an in-progress
  // attachment is exactly the kind of ephemeral draft state localStorage
  // is deliberately NOT used for elsewhere in this app either.
  const [pendingImages, setPendingImages] = useState([])
  // Set when a paste/attach is rejected (wrong model, bad file, too
  // large) so the composer can show why nothing was attached — cleared
  // on the next successful attach or on send, same lifecycle as
  // pendingImages itself.
  const [composerImageError, setComposerImageError] = useState('')
  const [streaming, setStreaming] = useState(false)
  // The file currently open in the Editor tab (see EditorView's
  // onOpenPathChange), shown under the composer and sent with each chat
  // request so the model knows what "this file"/"the current file" means
  // and can read it via the file-read tool without being told the path.
  const [activeEditorFile, setActiveEditorFile] = useState(null)
  const [docText, setDocText] = useState('')
  const [docStatus, setDocStatus] = useState('')
  // Set only while a batch upload (folder or multi-file) is running; drives
  // the progress bar. null the rest of the time so the bar unmounts.
  const [uploadProgress, setUploadProgress] = useState(null)
  // Persistent per-file failures from the current/last batch — unlike
  // docStatus (which one file's outcome overwrites the next), these stick
  // around after the batch finishes so a failure buried in the middle of a
  // large folder upload isn't just flashed past and lost.
  const [uploadErrors, setUploadErrors] = useState([])
  const [documents, setDocuments] = useState([])
  const [models, setModels] = useState([])
  const [model, setModel] = useModel()
  // A composer image-attach rejection ("X doesn't support images") is
  // specific to whichever model was selected at paste time — switching
  // to a vision-capable model should drop it immediately rather than
  // leaving a now-stale error sitting above the composer until the next
  // paste attempt overwrites it.
  useEffect(() => {
    setComposerImageError('')
  }, [model])
  const [skills, setSkills] = useState([])
  const [skillId, setSkillId] = useSkillId()
  const [skillFormOpen, setSkillFormOpen] = useState(false)
  const [skillName, setSkillName] = useState('')
  const [skillPrompt, setSkillPrompt] = useState('')
  const [skillToDelete, setSkillToDelete] = useState(null)
  const [skillError, setSkillError] = useState('')
  const [conversations, setConversations] = useState([])
  const [conversationId, setConversationId] = useState(null)
  const [conversationError, setConversationError] = useState('')
  // Collapsed to a slim strip only as a side effect of closing the
  // active chat (see handleCloseChat below), freeing up width for the
  // editor pane — expanded again by opening a conversation or starting
  // a new one.
  const [chatCollapsed, setChatCollapsed] = useState(false)
  const [settings, setSettings] = useState(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  // Set (to a sub-section id like 'llmBackend') to force that Settings
  // sub-section open when the panel is opened — see ModelPicker's
  // onOpenSettings below.
  const [settingsExpandRequest, setSettingsExpandRequest] = useState(null)
  const [quitConfirmOpen, setQuitConfirmOpen] = useState(false)
  const [quitting, setQuitting] = useState(false)
  const [screenshotBlob, setScreenshotBlob] = useState(null)
  const [capturingScreenshot, setCapturingScreenshot] = useState(false)
  const [serverStopped, setServerStopped] = useState(false)
  const [ragSettings, setRagSettings] = useState(null)
  const [fileAccessSettings, setFileAccessSettings] = useState({ root: '', read_enabled: false, write_enabled: false })
  const [terminalSettings, setTerminalSettings] = useState({ enabled: false })
  const [cloudProviderSettings, setCloudProviderSettings] = useState({ anthropic_configured: false, openai_configured: false, gemini_configured: false })
  const [editorSettings, setEditorSettings] = useState({ completion_model: '' })
  const [thinkLevel, setThinkLevel] = useState('medium')
  const [theme, setTheme] = useTheme()
  const offline = useConnectionStatus()
  // Dismissing the banner only hides it for the CURRENT outage — reset by
  // the effect below as soon as offline flips back to false, so a later,
  // genuinely new disconnect isn't silently suppressed by a dismissal the
  // user gave for a previous, already-resolved one.
  const [offlineDismissed, setOfflineDismissed] = useState(false)
  useEffect(() => {
    if (!offline) setOfflineDismissed(false)
  }, [offline])
  const [fontFamily, setFontFamily] = useFontFamily()
  const [fontScale, setFontScale] = useFontScale()
  const [sectionOrder, moveSection] = useSectionOrder(DEFAULT_SECTION_ORDER)
  const [sidebarCollapsed, setSidebarCollapsed] = useSidebarCollapsed()
  const [editorSidebarCollapsed, setEditorSidebarCollapsed] = useEditorSidebarCollapsed()
  const [editorPaneCollapsed, setEditorPaneCollapsed] = useEditorPaneCollapsed()
  const [terminalPaneCollapsed, setTerminalPaneCollapsed] = useTerminalPaneCollapsed()
  const [tweakBarOpen, setTweakBarOpen] = useState(false)
  // Bump counter telling EditorView to open the native folder picker —
  // null (not 0) means "never requested yet", so EditorView's effect can
  // tell "app just started" apart from "the counter really did change"
  // by comparing against null instead of relying on a ref that has to
  // remember whether this is the first render (fragile under React
  // StrictMode's dev-only double-invoke and under Vite HMR, both of which
  // can re-run an effect without a fresh ref).
  const [openFolderSignal, setOpenFolderSignal] = useState(null)
  const [editorPanel, setEditorPanel] = useEditorPanel()
  const [gitChangeCount, setGitChangeCount] = useState(0)
  // Which pane sits in which of the Editor/Terminal/Chat grid's three fixed
  // slots, and each split's size — see usePaneSlots/usePaneGridSizes' own
  // doc comments. Reassignable the same way the right sidebar's sections
  // are reordered (DraggableSection), just by swapping instead of splicing.
  const [paneSlots, swapPanes] = usePaneSlots(DEFAULT_SLOT_ASSIGNMENT)
  const [paneGridSizes, setLeftWidth, setTopHeight] = usePaneGridSizes()
  // DOM targets EditorView portals its sidebar/main content into — see
  // renderPanes below and EditorView's own doc comment. State (not refs)
  // because a portal target has to be there BY RENDER TIME for
  // createPortal to use it; a ref wouldn't trigger EditorView's own
  // re-render once the node actually mounts.
  const [editorSidebarContainer, setEditorSidebarContainer] = useState(null)
  const [editorMainContainer, setEditorMainContainer] = useState(null)
  // Each terminal tab is an independently mounted <TerminalView>, its own
  // xterm instance and WebSocket/PTY session server-side (see
  // internal/terminal/registry.go). Lives here rather than inside
  // TerminalPane since that pane can unmount/remount as it's reordered —
  // this state needs to survive that. All tabs stay mounted (display:none
  // when inactive) so switching tabs never loses scrollback or kills the
  // underlying shell.
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
      // terminal pane with zero tabs and no way to get one back short of
      // reloading the whole app would be a dead end, not a real "closed"
      // state.
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

  const bottomRef = useRef(null)
  const fileInputRef = useRef(null)
  const folderInputRef = useRef(null)
  const abortControllerRef = useRef(null)

  useEffect(() => {
    refreshConversations().then((list) => {
      if (list.length > 0) openConversation(list[0].id)
    })
    refreshDocuments()
    refreshSkills()
    fetchModels().then((list) => {
      setModels(list)
      if (list.length > 0) setModel((m) => m || list[0].name)
    })
    fetchSettings().then(setSettings)
    fetchRagSettings().then(setRagSettings)
    fetchFileAccessSettings().then((settings) => setFileAccessSettings(settings ?? { root: '', read_enabled: false, write_enabled: false }))
    fetchTerminalSettings().then((settings) => setTerminalSettings(settings ?? { enabled: false }))
    fetchCloudProviderSettings().then((settings) =>
      setCloudProviderSettings(settings ?? { anthropic_configured: false, openai_configured: false, gemini_configured: false }),
    )
    fetchEditorSettings().then((settings) => setEditorSettings(settings ?? { completion_model: '' }))
  }, [])


  // VS Code-style global panel shortcuts: Ctrl+B toggles the Files
  // sidebar, Ctrl+J toggles the terminal pane's presence in the
  // Editor/Terminal/Chat row (see usePaneSlots) — both panels are always
  // part of the (permanently split) layout, so this is a plain toggle
  // with no view to switch into first. Ctrl+Shift+M toggles the
  // right-hand model-settings panel; plain Ctrl+M was avoided as a
  // pairing with Ctrl+B/Ctrl+J since VS Code itself reserves unshifted
  // Ctrl+M for focus-tabbing, and Ctrl+C (as literally requested) was
  // ruled out because it's the OS copy shortcut used throughout chat, the
  // code editor, and the terminal.
  useEffect(() => {
    function handleKeyDown(e) {
      if (!(e.ctrlKey || e.metaKey)) return
      const key = e.key.toLowerCase()
      if (key === 'b' && !e.shiftKey) {
        e.preventDefault()
        setEditorSidebarCollapsed((c) => !c)
      } else if (key === 'j' && !e.shiftKey) {
        e.preventDefault()
        setTerminalPaneCollapsed((c) => !c)
      } else if (key === 'm' && e.shiftKey) {
        e.preventDefault()
        setSidebarCollapsed((c) => !c)
      } else if (key === 'o' && !e.shiftKey && isWails()) {
        // Matches TopBar's File → Open Folder… — Ctrl+O had a real native
        // accelerator when that menu item was a Win32 HMENU entry (see
        // TopBar.jsx's doc comment for why it's an in-app component now);
        // a plain <button> gets no OS-level shortcut for free, so this
        // keeps the binding working. Desktop-only, matching TopBar's own
        // isWails() gating — there's no folder-open concept in the
        // browser build this would otherwise shadow.
        e.preventDefault()
        setOpenFolderSignal((n) => (n ?? 0) + 1)
      } else if (key === 'q' && !e.shiftKey && isWails()) {
        // Matches TopBar's File → Exit, same reasoning as Ctrl+O above.
        e.preventDefault()
        setQuitConfirmOpen(true)
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [setEditorSidebarCollapsed, setTerminalPaneCollapsed, setSidebarCollapsed])

  // Search and Git live on the main rail (see the Search/Git buttons
  // below) rather than as tabs inside EditorView's own sidebar. Mirrors
  // toggleFilesPanel's behavior below: clicking the button for the panel
  // that's already open collapses the sidebar (a second click re-expands,
  // same toggle Ctrl+B already does); clicking it from anywhere else
  // selects that panel and makes sure the sidebar is actually expanded to
  // show it, rather than leaving it collapsed from an earlier
  // Ctrl+B/manual collapse. Also un-collapses the whole editor pane
  // (editorPaneCollapsed — see toggleEditorPaneCollapsed below) if that's
  // what's hiding it — Search/Git/Files should always be reachable from
  // their own rail buttons regardless of that toggle's state, same as
  // clicking one while the sidebar itself was manually collapsed already
  // does.
  function openEditorPanel(panel) {
    const alreadyShowingPanel = editorPanel === panel && !editorSidebarCollapsed && !editorPaneCollapsed
    if (alreadyShowingPanel) {
      setEditorSidebarCollapsed(true)
      return
    }
    setEditorPanel(panel)
    setEditorSidebarCollapsed(false)
    setEditorPaneCollapsed(false)
  }

  // The Files rail button mirrors VS Code's Explorer icon: clicking it
  // while Files is already the visible panel collapses the sidebar
  // (a second click re-expands, same toggle Ctrl+B already does — see
  // the keydown handler above); clicking it from anywhere else selects
  // the Files panel and makes sure the sidebar is actually expanded to
  // show it, rather than leaving it collapsed from an earlier
  // Ctrl+B/manual collapse. Also un-collapses the whole editor pane, same
  // reasoning as openEditorPanel above.
  function toggleFilesPanel() {
    const alreadyShowingFiles = editorPanel === 'files' && !editorSidebarCollapsed && !editorPaneCollapsed
    if (alreadyShowingFiles) {
      setEditorSidebarCollapsed(true)
      return
    }
    setEditorPanel('files')
    setEditorSidebarCollapsed(false)
    setEditorPaneCollapsed(false)
  }

  // Collapses the whole Editor pane (file tree/tabs, not the terminal —
  // that's its own separate pane now, see usePaneSlots/toggleTerminalPane
  // below). This toggle (a view-rail button — see its render below) is
  // the only way back in, same as handleCloseChat's X button is the only
  // way back into chat.
  function toggleEditorPaneCollapsed() {
    setEditorPaneCollapsed((collapsed) => !collapsed)
  }

  // Same idea for the Terminal pane — also bound to Ctrl+J (see the
  // keydown handler above).
  function toggleTerminalPane() {
    setTerminalPaneCollapsed((collapsed) => !collapsed)
  }

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  function refreshConversations() {
    return fetchConversations().then((list) => {
      setConversations(list)
      return list
    })
  }

  function openConversation(id) {
    setConversationId(id)
    setMessages([])
    setMessagesLoading(true)
    setInput('')
    setChatCollapsed(false)
    fetchMessages(id).then((msgs) => {
      // Guard against out-of-order responses: if the user switched to a
      // different conversation again before this fetch resolved, don't
      // stomp on the newer selection with stale data.
      setConversationId((current) => {
        if (String(current) === String(id)) {
          // The backend's pending_writes column (see store.Message) is
          // the same shape a live SSE pending_write event carries, minus
          // the camelCase key ChatPanel/PendingWriteCard read — mapped
          // here rather than renaming it anywhere else, so a reloaded
          // conversation's diff cards render from the exact same prop
          // shape a fresh one built during this session would.
          setMessages(msgs.map((m) => ({ ...m, pendingWrites: m.pending_writes ?? [] })))
          setMessagesLoading(false)
        }
        return current
      })
    })
  }

  function startNewChat() {
    setConversationId(null)
    setMessages([])
    setMessagesLoading(false)
    setInput('')
    // Re-expand if it was collapsed — clicking "+ New chat" (this
    // function's other caller) clearly means the user wants to see the
    // chat pane again, same as opening an existing conversation (see
    // openConversation).
    setChatCollapsed(false)
  }

  // Wraps startNewChat with also collapsing the chat pane itself down to
  // a slim strip — used only by ChatPanel's header X, freeing up width
  // for the editor pane once there's no active conversation to show.
  // Deliberately calls setChatCollapsed(true) after startNewChat's own
  // setChatCollapsed(false), so this specific path ends up collapsed
  // (last write wins) while every other caller of startNewChat expands.
  function handleCloseChat() {
    startNewChat()
    setChatCollapsed(true)
  }

  function stopStreaming() {
    abortControllerRef.current?.abort()
  }

  // Drags the vertical divider between the left column (top-left +
  // bottom-left slots) and the right (full-height) slot — see
  // usePaneGridSizes. Delta-based off the cursor's starting position, same
  // reasoning as the row-divider handler below.
  function handleColumnDividerDragStart(e) {
    e.preventDefault()
    const startX = e.clientX
    const startWidth = paneGridSizes.leftWidth

    // Belt-and-suspenders alongside preventDefault: without this, a fast
    // drag can still start a text-selection drag across the rest of the
    // page (the mousedown target is a thin divider, easy to graze rather
    // than hit squarely) — force it off for the duration of the drag, then
    // restore whatever the page's own default was.
    const previousUserSelect = document.body.style.userSelect
    document.body.style.userSelect = 'none'

    function handleMove(moveEvent) {
      const delta = moveEvent.clientX - startX
      setLeftWidth(Math.max(220, startWidth + delta))
    }
    function handleUp() {
      window.removeEventListener('mousemove', handleMove)
      window.removeEventListener('mouseup', handleUp)
      document.body.style.userSelect = previousUserSelect
    }
    window.addEventListener('mousemove', handleMove)
    window.addEventListener('mouseup', handleUp)
  }

  // Drags the horizontal divider between the top-left and bottom-left
  // slots, within the left column — see usePaneGridSizes.
  function handleRowDividerDragStart(e) {
    e.preventDefault()
    const startY = e.clientY
    const startHeight = paneGridSizes.topHeight

    const previousUserSelect = document.body.style.userSelect
    document.body.style.userSelect = 'none'

    function handleMove(moveEvent) {
      const delta = moveEvent.clientY - startY
      setTopHeight(Math.max(160, startHeight + delta))
    }
    function handleUp() {
      window.removeEventListener('mousemove', handleMove)
      window.removeEventListener('mouseup', handleUp)
      document.body.style.userSelect = previousUserSelect
    }
    window.addEventListener('mousemove', handleMove)
    window.addEventListener('mouseup', handleUp)
  }

  // No confirm modal for chat deletion — chats are low-stakes and easy to
  // regenerate/re-derive compared to overwriting a file on disk, so this
  // deletes immediately on click rather than routing through
  // ConfirmDeleteModal the way skill/file-write/quit do.
  async function handleDeleteConversation(id) {
    if (id == null) return
    setConversationError('')
    try {
      const res = await deleteConversation(id)
      if (!res.ok) throw new Error(await res.text())
      if (String(conversationId) === String(id)) startNewChat()
      const list = await refreshConversations()
      if (String(conversationId) === String(id) && list.length > 0) openConversation(list[0].id)
    } catch (err) {
      setConversationError(`Couldn't delete this conversation: ${err.message}`)
    }
  }

  function refreshDocuments() {
    fetchDocuments().then(setDocuments)
  }

  function refreshSkills() {
    fetchSkills().then(setSkills)
  }

  // maxPendingImages/maxImageBytes bound what the composer will accept —
  // a vision request with many/huge images costs real latency and (for
  // cloud providers) real money per token, and there's no resizing/
  // compression step here, so the cap has to be conservative enough that
  // even several full-resolution screenshots stay reasonable.
  const maxPendingImages = 4
  const maxImageBytes = 8 * 1024 * 1024

  const visionSupported = !!models.find((m) => m.name === model)?.supports_vision

  function addPendingImage(file) {
    if (!file.type.startsWith('image/')) return
    if (file.size > maxImageBytes) {
      setComposerImageError(`"${file.name || 'pasted image'}" is too large to attach (max 8 MB).`)
      return
    }
    setComposerImageError('')
    const reader = new FileReader()
    reader.onload = () => {
      setPendingImages((prev) => {
        if (prev.length >= maxPendingImages) return prev
        return [...prev, { dataUri: reader.result, name: file.name || 'pasted-image' }]
      })
    }
    reader.readAsDataURL(file)
  }

  function removePendingImage(index) {
    setPendingImages((prev) => prev.filter((_, i) => i !== index))
  }

  // Wired to the composer's onPaste — Ctrl+V with an image on the
  // clipboard (a screenshot, a copied image from a browser/file explorer)
  // attaches it instead of the browser trying to paste it as text (which
  // it can't, so nothing would happen otherwise). Text pastes are left
  // completely alone: only clipboard items whose type starts with
  // "image/" are intercepted, so a normal text paste never even reaches
  // this branch, let alone gets preventDefault'd — and never rejected for
  // vision support, since a plain-text paste is always valid regardless
  // of which model is selected.
  //
  // A model that doesn't support vision (per visionSupported, derived
  // from llm.SupportsVisionForModel via GET /api/models) rejects the
  // image outright rather than silently attaching it and letting the
  // request fail (or be silently dropped) deep inside whichever
  // provider's API receives it — see llm.Message's Images field and each
  // provider's request translation for what would otherwise happen.
  function handleComposerPaste(e) {
    const items = Array.from(e.clipboardData?.items || [])
    const imageItems = items.filter((item) => item.type.startsWith('image/'))
    if (imageItems.length === 0) return
    e.preventDefault()
    if (!visionSupported) {
      setComposerImageError(`"${model || 'This model'}" doesn't support images.`)
      return
    }
    for (const item of imageItems) {
      const file = item.getAsFile()
      if (file) addPendingImage(file)
    }
  }

  async function sendMessage(e) {
    e.preventDefault()
    const text = input.trim()
    if ((!text && pendingImages.length === 0) || streaming) return

    const images = pendingImages.map((img) => img.dataUri)
    setInput('')
    setPendingImages([])
    setComposerImageError('')
    setStreaming(true)
    setMessages((prev) => [...prev, { role: 'user', content: text, images }, { role: 'assistant', content: '', sources: [], reasoning: '', toolCalls: [], pendingWrites: [], buildChecks: [], usage: null }])

    const controller = new AbortController()
    abortControllerRef.current = controller

    try {
      await streamChat(
        { message: text, model, skillId, conversationId, thinkLevel, images, activeFile: activeEditorFile },
        {
          onConversation: (id) => {
            setConversationId(id)
            refreshConversations()
          },
          onSources: (sources) => {
            setMessages((prev) => {
              const next = [...prev]
              next[next.length - 1] = { ...next[next.length - 1], sources }
              return next
            })
          },
          onToken: (token) => {
            setMessages((prev) => {
              const next = [...prev]
              next[next.length - 1] = {
                ...next[next.length - 1],
                content: next[next.length - 1].content + token,
              }
              return next
            })
          },
          onReasoning: (reasoning) => {
            setMessages((prev) => {
              const next = [...prev]
              next[next.length - 1] = {
                ...next[next.length - 1],
                reasoning: (next[next.length - 1].reasoning || '') + reasoning,
              }
              return next
            })
          },
          onUsage: (usage) => {
            setMessages((prev) => {
              const next = [...prev]
              next[next.length - 1] = { ...next[next.length - 1], usage }
              return next
            })
          },
          onToolCall: (call) => {
            setMessages((prev) => {
              const next = [...prev]
              const last = next[next.length - 1]
              next[next.length - 1] = {
                ...last,
                toolCalls: [...(last.toolCalls || []), call],
              }
              return next
            })
          },
          onPendingWrite: (write) => {
            setMessages((prev) => {
              const next = [...prev]
              const last = next[next.length - 1]
              next[next.length - 1] = {
                ...last,
                pendingWrites: [...(last.pendingWrites || []), { ...write, status: 'pending' }],
              }
              return next
            })
          },
          onBuildCheck: (check) => {
            setMessages((prev) => {
              const next = [...prev]
              const last = next[next.length - 1]
              next[next.length - 1] = {
                ...last,
                buildChecks: [...(last.buildChecks || []), check],
              }
              return next
            })
          },
          onError: (message) => {
            setMessages((prev) => {
              const next = [...prev]
              next[next.length - 1] = {
                ...next[next.length - 1],
                content: `⚠️ The model backend couldn't complete this response.\n\n${message}`,
                isError: true,
              }
              return next
            })
          },
        },
        controller.signal
      )
    } catch (err) {
      // A deliberate stop click aborts the fetch, which surfaces here as
      // an AbortError — that's the expected/successful outcome of
      // stopStreaming, not a failure worth showing as an error bubble.
      if (err.name !== 'AbortError') {
        setMessages((prev) => [...prev, { role: 'assistant', content: `⚠️ ${err.message}`, isError: true }])
      }
    } finally {
      abortControllerRef.current = null
      setStreaming(false)
      refreshConversations()
    }
  }

  async function indexContent(filename, content) {
    setDocStatus(`Indexing ${filename}…`)
    try {
      const res = await indexDocument(filename, content)
      if (!res.ok) throw new Error(await res.text())
      const data = await res.json()
      setDocStatus(`Indexed ${data.chunks} chunk(s) from ${filename}.`)
      refreshDocuments()
    } catch (err) {
      setDocStatus(`Couldn't index ${filename}: ${err.message}`)
    }
  }

  // Returns an outcome tag rather than only setting docStatus, so callers
  // driving a batch (indexFileList) can tell a real failure apart from a
  // benign skip and aggregate counts/errors across the whole batch instead
  // of only ever knowing about the very last file.
  async function indexFile(file, label) {
    setDocStatus(`Uploading ${label}…`)
    try {
      const res = await uploadFile(file, label)
      if (!res.ok) {
        const message = await res.text()
        // Empty/whitespace-only files are common and harmless in a folder
        // upload (codegen stubs, blank __init__.py, etc.) — not a real
        // failure worth interrupting the batch for, so skip quietly rather
        // than surfacing "Error indexing ...".
        if (res.status === 400 && message.includes('no extractable text')) {
          setDocStatus(`Skipped ${label}: empty file.`)
          return { status: 'skipped' }
        }
        throw new Error(message)
      }
      const data = await res.json()
      setDocStatus(`Indexed ${data.chunks} chunk(s) from ${label}.`)
      refreshDocuments()
      return { status: 'indexed' }
    } catch (err) {
      setDocStatus(`Couldn't index ${label}: ${err.message}`)
      return { status: 'error', message: err.message }
    }
  }

  async function uploadDocument(e) {
    e.preventDefault()
    if (!docText.trim()) return
    await indexContent('pasted-text.txt', docText)
    setDocText('')
  }

  // Shared by both the flat file picker and the folder picker: skips
  // unsupported extensions and indexes everything else one at a time
  // (sequential, so the status line stays readable and we don't flood
  // the embedding backend with concurrent requests). Drives uploadProgress
  // for the progress bar and accumulates real failures into uploadErrors —
  // computed up front (rather than filtered inline in the loop) so the
  // progress bar's denominator is "files actually attempted," not raw
  // file count, which would stall visually while skipping a big excluded
  // directory like node_modules.
  async function indexFileList(files, { relativeLabel } = {}) {
    const candidates = []
    for (const file of files) {
      const label = relativeLabel ? file.webkitRelativePath || file.name : file.name
      // Only a folder upload has real directory segments to check —
      // webkitRelativePath is empty for the flat file picker, so
      // isInExcludedDir would never match there anyway, but relativeLabel
      // makes the intent explicit rather than relying on that being empty.
      if (relativeLabel && isInExcludedDir(label)) {
        continue
      }
      // Applies to the flat picker too, not just folder uploads — someone
      // multi-selecting files by hand is just as unlikely to want a
      // lockfile semantically indexed as someone uploading a whole folder.
      if (isExcludedFilename(file.name)) {
        continue
      }
      if (!UPLOAD_FILE_PATTERN.test(file.name)) {
        continue
      }
      candidates.push({ file, label })
    }

    if (candidates.length === 0) return

    setUploadErrors([])
    setUploadProgress({ total: candidates.length, completed: 0, label: candidates[0].label })

    for (let i = 0; i < candidates.length; i++) {
      const { file, label } = candidates[i]
      setUploadProgress({ total: candidates.length, completed: i, label })
      const outcome = await indexFile(file, label)
      if (outcome?.status === 'error') {
        setUploadErrors((prev) => [...prev, { label, message: outcome.message }])
      }
    }

    setUploadProgress(null)
  }

  async function handleFilePicked(e) {
    const files = Array.from(e.target.files ?? [])
    e.target.value = '' // allow re-selecting the same file later
    await indexFileList(files)
  }

  async function handleFolderPicked(e) {
    const files = Array.from(e.target.files ?? [])
    e.target.value = ''
    await indexFileList(files, { relativeLabel: true })
  }

  async function handleClearKnowledgeBase() {
    try {
      const res = await clearKnowledgeBase()
      if (!res.ok) throw new Error(await res.text())
      setDocStatus('Knowledge base cleared.')
      refreshDocuments()
    } catch (err) {
      setDocStatus(`Couldn't clear the knowledge base: ${err.message}`)
      throw err
    }
  }

  async function handleClearConversations() {
    const res = await clearConversations()
    if (!res.ok) {
      setConversationError(`Couldn't clear conversations: ${await res.text()}`)
      throw new Error('clear conversations failed')
    }
    await refreshConversations()
    startNewChat()
  }

  async function handleSaveRagSettings(next) {
    const res = await saveRagSettings(next)
    if (!res.ok) throw new Error(await res.text())
    const saved = await res.json()
    setRagSettings(saved)
  }

  async function handleSaveFileAccessSettings(next) {
    const res = await saveFileAccessSettings(next)
    if (!res.ok) throw new Error(await res.text())
    const saved = await res.json()
    setFileAccessSettings(saved)
    return saved
  }

  // On success, re-fetches the model list — a newly-configured provider's
  // models should show up in the picker immediately, without waiting for
  // some other unrelated refresh to happen to run first.
  async function handleSaveCloudProviderSettings(next) {
    const res = await saveCloudProviderSettings(next)
    if (!res.ok) throw new Error(await res.text())
    const saved = await res.json()
    setCloudProviderSettings(saved)
    fetchModels().then(setModels)
    return saved
  }

  async function handleSaveTerminalSettings(next) {
    const res = await saveTerminalSettings(next)
    if (!res.ok) throw new Error(await res.text())
    const saved = await res.json()
    setTerminalSettings(saved)
    return saved
  }

  async function createSkill(e) {
    e.preventDefault()
    if (!skillName.trim() || !skillPrompt.trim()) return
    setSkillError('')
    try {
      const res = await apiCreateSkill(skillName, skillPrompt)
      if (!res.ok) throw new Error(await res.text())
      setSkillName('')
      setSkillPrompt('')
      setSkillFormOpen(false)
      refreshSkills()
    } catch (err) {
      // Leaves the form open with the user's input intact, but now says
      // why instead of just doing nothing — same phrasing family as the
      // delete-skill error a few lines below.
      setSkillError(`Couldn't create this skill: ${err.message}`)
    }
  }

  async function confirmQuit() {
    setQuitting(true)
    try {
      await quitServer()
    } catch {
      // The server closing its own connection to respond can itself look
      // like a fetch error — that's still a successful quit, not a failure.
    }
    // window.close() is a no-op on tabs the user navigated to directly
    // (as opposed to ones opened via script) — most browsers silently
    // ignore it. Fall back to an in-page "stopped" state so the tab
    // doesn't sit there looking alive against a server that's gone.
    window.close()
    setQuitConfirmOpen(false)
    setServerStopped(true)
  }

  async function handleScreenshot() {
    setCapturingScreenshot(true)
    try {
      const blob = await captureScreenshot()
      setScreenshotBlob(blob)
    } catch (err) {
      // A native alert rather than an inline status line: this button
      // lives in the icon-only view-rail with no room for status text
      // nearby, and the failure needs to be visible regardless of which
      // tab (Chat/Editor) is currently active — same reasoning EditorView
      // uses window.confirm() for its own dialogs rather than inventing
      // a toast system for one-off cases.
      alert(`Couldn't capture a screenshot: ${err.message}`)
    } finally {
      setCapturingScreenshot(false)
    }
  }

  function handleOpenSettings(subSection) {
    if (subSection) setSettingsExpandRequest(subSection)
    setSettingsOpen(true)
  }

  // completionModel is '' for "use the backend's default chat model"
  // (see store.EditorSettings' doc comment) — saved optimistically so the
  // dropdown reflects the pick immediately rather than waiting on a
  // round-trip, matching how setModel itself is a plain synchronous
  // setter with no save-confirmation step.
  async function handleSetCompletionModel(completionModel) {
    setEditorSettings((prev) => ({ ...prev, completion_model: completionModel }))
    try {
      await saveEditorSettings({ completion_model: completionModel })
    } catch (err) {
      console.error('Failed to save completion model setting:', err)
    }
  }

  async function confirmDeleteSkill() {
    const id = skillToDelete
    setSkillToDelete(null)
    if (id == null) return
    setSkillError('')
    try {
      const res = await deleteSkillById(id)
      if (!res.ok) throw new Error(await res.text())
      if (String(skillId) === String(id)) setSkillId('')
      refreshSkills()
    } catch (err) {
      setSkillError(`Couldn't delete this skill: ${err.message}`)
    }
  }

  function updatePendingWrite(id, patch) {
    setMessages((prev) =>
      prev.map((m) =>
        m.pendingWrites?.some((w) => w.id === id)
          ? { ...m, pendingWrites: m.pendingWrites.map((w) => (w.id === id ? { ...w, ...patch } : w)) }
          : m
      )
    )
  }

  // Approving a write applies immediately, no confirm modal — the diff is
  // already shown in the PendingWriteCard before Approve is clicked, so
  // the review step already happened; a second confirmation on top of
  // that was just friction, not real protection.
  function requestApproveWrite(write) {
    runApproveWrite(write.id)
  }

  async function runApproveWrite(id) {
    updatePendingWrite(id, { status: 'applying' })
    try {
      const res = await approveWrite(id)
      if (!res.ok) throw new Error(await res.text())
      updatePendingWrite(id, { status: 'approved' })
    } catch (err) {
      updatePendingWrite(id, { status: 'error', error: err.message })
    }
  }

  async function handleRejectWrite(id) {
    updatePendingWrite(id, { status: 'applying' })
    try {
      const res = await rejectWrite(id)
      if (!res.ok) throw new Error(await res.text())
      updatePendingWrite(id, { status: 'rejected' })
    } catch (err) {
      updatePendingWrite(id, { status: 'error', error: err.message })
    }
  }

  if (serverStopped) {
    return (
      <div className="app">
        <div className="stopped-state">
          <p className="stopped-title">fastllm has stopped.</p>
          <p className="stopped-hint">You can close this window, or relaunch it from the Desktop shortcut.</p>
        </div>
      </div>
    )
  }

  // The Editor/Terminal/Chat grid: a fixed shape — top-left and
  // bottom-left stacked in a left column, one full-height slot on the
  // right (see usePaneSlots/usePaneGridSizes) — with each of the three
  // panes independently collapsible (editorPaneCollapsed/
  // terminalPaneCollapsed/chatCollapsed). Dragging a pane's title bar onto
  // another slot swaps their assignments (DraggableSection, same mechanism
  // the right sidebar's sections use) — the grid's shape itself never
  // changes, only which pane sits where. Collapsing the only pane on one
  // side of a divider removes that divider and lets its sibling fill the
  // freed space.
  function renderPanes() {
    // Editor isn't in here — unlike Terminal/Chat, it doesn't render its
    // content inline into whichever slot it's assigned. Its sidebar and
    // main-content portal into DOM targets this function hands out below
    // (editorMainContainer always; editorSidebarContainer either inside
    // Editor's own slot or spanning the shared left column — see the
    // editorSharesLeftColumn branch), so the always-mounted <EditorView/>
    // near the bottom of this component's return can keep running
    // regardless of where those targets currently point.
    const nonEditorContent = {
      terminal: (
        <TerminalPane
          theme={theme}
          folderRoot={fileAccessSettings?.root}
          terminalEnabled={terminalSettings.enabled}
          terminalTabs={terminalTabs}
          activeTerminalTab={activeTerminalTab}
          onSelectTab={setActiveTerminalTab}
          onAddTab={addTerminalTab}
          onCloseTab={closeTerminalTab}
        />
      ),
      chat: (
        <ChatPanel
          messages={messages}
          messagesLoading={messagesLoading}
          conversationId={conversationId}
          conversationTitle={conversations.find((c) => String(c.id) === String(conversationId))?.title}
          onCloseChat={handleCloseChat}
          onNewChat={startNewChat}
          bottomRef={bottomRef}
          input={input}
          onInputChange={setInput}
          streaming={streaming}
          onSendMessage={sendMessage}
          onStop={stopStreaming}
          userDisplayName={settings?.username}
          onRequestApproveWrite={requestApproveWrite}
          onRejectWrite={handleRejectWrite}
          pendingImages={pendingImages}
          composerImageError={composerImageError}
          onComposerPaste={handleComposerPaste}
          onRemovePendingImage={removePendingImage}
          visionSupported={visionSupported}
          activeEditorFile={activeEditorFile}
        />
      ),
    }
    const collapsedByKey = { editor: editorPaneCollapsed, terminal: terminalPaneCollapsed, chat: chatCollapsed }

    const topKey = paneSlots['top-left']
    const bottomKey = paneSlots['bottom-left']
    const rightKey = paneSlots['right']
    const topVisible = !collapsedByKey[topKey]
    const bottomVisible = !collapsedByKey[bottomKey]
    const rightVisible = !collapsedByKey[rightKey]
    const leftVisible = topVisible || bottomVisible

    // Editor's file sidebar spans the whole left column — next to both
    // its own tabs AND Terminal (or whatever's sharing the column with
    // it) — only when it actually HAS a visible column-mate there,
    // mirroring a conventional IDE's file explorer. Editor alone in a
    // slot (its mate collapsed, or Editor dragged into the full-height
    // right slot) just renders sidebar+main together inline, same as
    // before.
    const editorSharesLeftColumn = (topKey === 'editor' && bottomVisible) || (bottomKey === 'editor' && topVisible)

    const editorMainPortal = <div className="portal-target" ref={setEditorMainContainer} />

    function slotPane(key, flex) {
      let content = nonEditorContent[key]
      if (key === 'editor') {
        // Sharing the column: just the main-content portal — the sidebar
        // portal lives outside this DraggableSection entirely (see the
        // editorSharesLeftColumn branch below). Alone in its own slot:
        // sidebar+main need a row wrapper here, since DraggableSection's
        // own content area is a column (title bar stacked over content) —
        // .editor-view used to be that row; now it's inlined, since
        // there's nothing left to put the sidebar's OTHER placement in.
        content = editorSharesLeftColumn ? (
          editorMainPortal
        ) : (
          <div className="editor-inline-row">
            <div className="editor-sidebar-portal" ref={setEditorSidebarContainer} />
            {editorMainPortal}
          </div>
        )
      }
      return (
        <DraggableSection
          key={key}
          sectionKey={key}
          index={key}
          onReorder={swapPanes}
          className="is-pane-column"
          style={{ flex, minWidth: 220, minHeight: 160 }}
          label={PANE_LABELS[key]}
        >
          {content}
        </DraggableSection>
      )
    }

    const leftColumnNodes = []
    if (topVisible) {
      leftColumnNodes.push(slotPane(topKey, bottomVisible ? `0 1 ${paneGridSizes.topHeight}px` : '1 1 0%'))
    }
    if (topVisible && bottomVisible) {
      leftColumnNodes.push(
        <div key="row-divider" className="split-divider split-divider-row" onMouseDown={handleRowDividerDragStart} />
      )
    }
    if (bottomVisible) {
      leftColumnNodes.push(slotPane(bottomKey, '1 1 0%'))
    }

    const rows = []
    if (leftVisible) {
      const stack = (
        <div
          key={editorSharesLeftColumn ? undefined : 'left-column'}
          className="pane-column"
          style={{ flex: editorSharesLeftColumn || !rightVisible ? '1 1 0%' : `0 1 ${paneGridSizes.leftWidth}px`, minWidth: 220 }}
        >
          {leftColumnNodes}
        </div>
      )
      rows.push(
        editorSharesLeftColumn ? (
          <div
            key="left-column"
            className="editor-sidebar-row"
            style={{ flex: rightVisible ? `0 1 ${paneGridSizes.leftWidth}px` : '1 1 0%', minWidth: 220 }}
          >
            <div className="editor-sidebar-portal" ref={setEditorSidebarContainer} />
            {stack}
          </div>
        ) : (
          stack
        )
      )
    }
    if (leftVisible && rightVisible) {
      rows.push(<div key="column-divider" className="split-divider" onMouseDown={handleColumnDividerDragStart} />)
    }
    if (rightVisible) {
      rows.push(slotPane(rightKey, '1 1 0%'))
    }

    return <div className="split-container">{rows}</div>
  }

  return (
    <div className="app">
      {isWails() && (
        <TopBar
          onOpenFolder={() => setOpenFolderSignal((n) => (n ?? 0) + 1)}
          onQuit={() => setQuitConfirmOpen(true)}
          onAbout={() => handleOpenSettings('about')}
        />
      )}
      {offline && !offlineDismissed && (
        <div className="offline-banner">
          <span className="offline-banner-dot" aria-hidden="true" />
          <span className="offline-banner-text">Can't reach the fastllm backend — showing the last data that loaded.</span>
          <button type="button" className="offline-banner-close" title="Dismiss" onClick={() => setOfflineDismissed(true)}>
            ✕
          </button>
        </div>
      )}
      <div className="app-body">
      <nav className="view-rail">
        <button
          type="button"
          className={editorPaneCollapsed ? '' : 'is-toggled'}
          title={editorPaneCollapsed ? 'Show editor panel' : 'Hide editor panel'}
          onClick={toggleEditorPaneCollapsed}
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <rect x="3" y="4" width="18" height="16" rx="2" />
            <path d="M10 4v16" />
            {!editorPaneCollapsed && <rect x="3" y="4" width="7" height="16" rx="1" fill="currentColor" stroke="none" opacity="0.35" />}
          </svg>
        </button>
        <button
          type="button"
          className={editorPanel === 'files' && !editorSidebarCollapsed ? 'is-active' : ''}
          title="Files"
          onClick={toggleFilesPanel}
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M3 7.5A1.5 1.5 0 0 1 4.5 6h4l1.7 2H19.5A1.5 1.5 0 0 1 21 9.5v8A1.5 1.5 0 0 1 19.5 19h-15A1.5 1.5 0 0 1 3 17.5v-10Z" />
          </svg>
        </button>
        <button
          type="button"
          className={editorPanel === 'search' ? 'is-active' : ''}
          title="Search"
          onClick={() => openEditorPanel('search')}
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <circle cx="11" cy="11" r="7" />
            <path d="m20 20-3.5-3.5" />
          </svg>
        </button>
        <button
          type="button"
          className={`view-rail-git ${editorPanel === 'git' ? 'is-active' : ''}`}
          title="Git"
          onClick={() => openEditorPanel('git')}
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <circle cx="6" cy="6" r="2.5" />
            <circle cx="6" cy="18" r="2.5" />
            <circle cx="18" cy="12" r="2.5" />
            <path d="M6 8.5v7M8 6h4a4 4 0 0 1 4 4v0" />
          </svg>
          {gitChangeCount > 0 && <span className="view-rail-badge">{gitChangeCount}</span>}
        </button>

        <div className="view-rail-spacer" />

        {isWails() && (
          <button
            type="button"
            title="Screenshot"
            disabled={capturingScreenshot}
            // Modals (Settings, etc.) close themselves on any mousedown
            // outside their own DOM node — this button lives outside every
            // modal, so a plain click would close whatever modal is open
            // (mousedown fires, and closes it) before the click handler
            // that captures the screenshot even runs, defeating the whole
            // point of screenshotting a modal. Capturing here on
            // mousedown's capture phase, with stopPropagation, keeps this
            // button's press from ever reaching the modals' own
            // document-level "click outside" listeners, so the modal is
            // still open (and still painted) when captureScreenshot's
            // native PrintWindow call runs.
            onMouseDownCapture={(e) => {
              e.stopPropagation()
              handleScreenshot()
            }}
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="M4 8h3l1.5-2h7L17 8h3a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V9a1 1 0 0 1 1-1Z" />
              <circle cx="12" cy="13.5" r="3.5" />
            </svg>
          </button>
        )}
        <button
          type="button"
          id="view-rail-tweak-bar"
          className={tweakBarOpen ? 'is-active' : ''}
          title="Tweak bar (dev)"
          onClick={() => setTweakBarOpen((v) => !v)}
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M4 6h10M17 6h3M4 12h4M11 12h9M4 18h13M20 18h0" />
            <circle cx="14" cy="6" r="2" fill="currentColor" stroke="none" />
            <circle cx="7" cy="12" r="2" fill="currentColor" stroke="none" />
            <circle cx="17" cy="18" r="2" fill="currentColor" stroke="none" />
          </svg>
        </button>
        <button type="button" title="Settings" onClick={() => handleOpenSettings()}>
          ⚙
        </button>
        {!isWails() && (
          <button type="button" title="Quit fastllm" onClick={() => setQuitConfirmOpen(true)}>
            ⏻
          </button>
        )}
      </nav>

      <div className="main-row">
      {renderPanes()}

      <aside className={`sidebar ${sidebarCollapsed ? 'is-collapsed' : ''}`}>
        <button
          type="button"
          className="sidebar-collapse-toggle"
          title={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}
          onClick={() => setSidebarCollapsed((c) => !c)}
        >
          {sidebarCollapsed ? '«' : '»'}
        </button>

        {sidebarCollapsed
          ? sectionOrder.map((key) => (
              <button
                key={key}
                type="button"
                className="sidebar-icon-btn"
                title={SECTION_LABELS[key]}
                onClick={() => setSidebarCollapsed(false)}
              >
                <SectionIcon name={key} />
              </button>
            ))
          : sectionOrder.map((key, index) => {
              const section = {
                conversations: (
                  <ConversationList
                    conversations={conversations}
                    conversationId={conversationId}
                    onNewChat={startNewChat}
                    onOpen={openConversation}
                    onRequestDelete={handleDeleteConversation}
                    error={conversationError}
                  />
                ),
                model: (
              <ModelPicker
                models={models}
                model={model}
                onChange={setModel}
                fileAccessSettings={fileAccessSettings}
                onOpenSettings={() => handleOpenSettings('llmBackend')}
                completionModel={editorSettings.completion_model}
                onCompletionModelChange={handleSetCompletionModel}
              />
            ),
                skills: (
                  <SkillPanel
                    skills={skills}
                    skillId={skillId}
                    onSkillIdChange={setSkillId}
                    onRequestDeleteSkill={setSkillToDelete}
                    skillFormOpen={skillFormOpen}
                    onOpenForm={() => setSkillFormOpen(true)}
                    onCloseForm={() => setSkillFormOpen(false)}
                    skillName={skillName}
                    onSkillNameChange={setSkillName}
                    skillPrompt={skillPrompt}
                    onSkillPromptChange={setSkillPrompt}
                    onCreateSkill={createSkill}
                    error={skillError}
                  />
                ),
                knowledge: (
                  <KnowledgeBasePanel
                    docText={docText}
                    onDocTextChange={setDocText}
                    onUploadDocument={uploadDocument}
                    fileInputRef={fileInputRef}
                    onFilePicked={handleFilePicked}
                    folderInputRef={folderInputRef}
                    onFolderPicked={handleFolderPicked}
                    docStatus={docStatus}
                    documents={documents}
                    uploadProgress={uploadProgress}
                    uploadErrors={uploadErrors}
                    onDismissUploadErrors={() => setUploadErrors([])}
                  />
                ),
              }[key]

              return (
                <DraggableSection key={key} sectionKey={key} index={index} onReorder={moveSection}>
                  {section}
                </DraggableSection>
              )
            })}
      </aside>
      </div>
      </div>

      {skillToDelete != null && (
        <ConfirmDeleteModal
          heading="Delete skill?"
          description={`This deletes "${skills.find((s) => s.id === skillToDelete)?.name ?? 'this skill'}". This can't be undone.`}
          confirmLabel="Delete"
          onCancel={() => setSkillToDelete(null)}
          onConfirm={confirmDeleteSkill}
        />
      )}

      {quitConfirmOpen && (
        <ConfirmDeleteModal
          heading="Quit fastllm?"
          description={
            isWails()
              ? 'This closes the app.'
              : 'This stops the local server. Any open browser tabs will stop working until you relaunch it from the Desktop shortcut.'
          }
          confirmLabel={quitting ? 'Quitting…' : 'Quit'}
          onCancel={() => setQuitConfirmOpen(false)}
          onConfirm={confirmQuit}
        />
      )}

      {screenshotBlob && (
        <ScreenshotPreviewModal blob={screenshotBlob} onClose={() => setScreenshotBlob(null)} />
      )}

      {settingsOpen && (
        <SettingsPanel
          theme={theme}
          onThemeChange={setTheme}
          fontFamily={fontFamily}
          onFontFamilyChange={setFontFamily}
          fontScale={fontScale}
          onFontScaleChange={setFontScale}
          settings={settings}
          ragSettings={ragSettings}
          onSaveRagSettings={handleSaveRagSettings}
          fileAccessSettings={fileAccessSettings}
          onSaveFileAccessSettings={handleSaveFileAccessSettings}
          terminalSettings={terminalSettings}
          onSaveTerminalSettings={handleSaveTerminalSettings}
          cloudProviderSettings={cloudProviderSettings}
          onSaveCloudProviderSettings={handleSaveCloudProviderSettings}
          thinkLevel={thinkLevel}
          onThinkLevelChange={setThinkLevel}
          onClearKnowledgeBase={handleClearKnowledgeBase}
          onClearConversations={handleClearConversations}
          expandSection={settingsExpandRequest}
          onClose={() => setSettingsOpen(false)}
        />
      )}

      {/* Always mounted, regardless of where renderPanes() currently points
          its portal targets — see EditorView's own doc comment on why. */}
      <EditorView
        fileAccessSettings={fileAccessSettings}
        onFileAccessSettingsChange={setFileAccessSettings}
        theme={theme}
        terminalEnabled={terminalSettings.enabled}
        openFolderSignal={openFolderSignal}
        onOpenSettings={handleOpenSettings}
        panel={editorPanel}
        onPanelChange={setEditorPanel}
        onGitChangeCountChange={setGitChangeCount}
        onOpenPathChange={setActiveEditorFile}
        sidebarCollapsed={editorSidebarCollapsed}
        onSidebarCollapsedChange={setEditorSidebarCollapsed}
        sidebarContainer={editorSidebarContainer}
        mainContainer={editorMainContainer}
      />

      <TweakBar open={tweakBarOpen} onToggle={() => setTweakBarOpen(true)} />

      <span className="app-version" title={__BUILD_TIME__}>
        v{__APP_VERSION__}
      </span>
    </div>
  )
}
