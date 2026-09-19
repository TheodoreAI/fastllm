import { useEffect, useMemo, useRef, useState } from 'react'
import './App.css'
import ConversationList from './components/ConversationList'
import ModelPicker from './components/ModelPicker'
import SkillPanel from './components/SkillPanel'
import KnowledgeBasePanel from './components/KnowledgeBasePanel'
import ChatPanel from './components/ChatPanel'
import ConfirmDeleteModal from './components/ConfirmDeleteModal'
import CommandPalette from './components/CommandPalette'
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
import { useConnectionStatus } from './useConnectionStatus'
import { useModel } from './useModel'
import { useSkillId } from './useSkillId'
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
  fetchCloudProviderSettings,
  saveCloudProviderSettings,
  clearKnowledgeBase,
  clearConversations,
  createSkill as apiCreateSkill,
  deleteSkillById,
  indexDocument,
  uploadFile,
  streamChat,
  subscribeLiveChat,
  setLiveTarget,
  quitServer,
  isWails,
  captureScreenshot,
  approveWrite,
  rejectWrite,
} from './api'
import { buildCommands } from './commands'

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
  '.yarn',
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
  'target',
  'vendor',
  '.dart_tool',
  '.pub-cache',
  '.stack-work',
  'dist-newstyle',
  '.idea',
  '.vs',
  'obj',
  '.react-router',
  '.svelte-kit',
  '.vercel',
  '.netlify',
  '.wrangler',
])

function isInExcludedDir(relativePath) {
  const segments = relativePath.split(/[/\\]/)
  return segments.some((segment) => UPLOAD_EXCLUDED_DIRS.has(segment))
}

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
  'pubspec.lock',
  'stack.yaml.lock',
  'cabal.project.freeze',
])

function isExcludedFilename(filename) {
  return UPLOAD_EXCLUDED_FILENAMES.has(filename)
}

const DEFAULT_SECTION_ORDER = ['conversations', 'model', 'skills', 'knowledge']

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
  const [pendingImages, setPendingImages] = useState([])
  const [composerImageError, setComposerImageError] = useState('')
  const [streaming, setStreaming] = useState(false)
  const [docText, setDocText] = useState('')
  const [docStatus, setDocStatus] = useState('')
  const [uploadProgress, setUploadProgress] = useState(null)
  const [uploadErrors, setUploadErrors] = useState([])
  const [documents, setDocuments] = useState([])
  const [models, setModels] = useState([])
  const [model, setModel] = useModel()

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
  const conversationIdRef = useRef(null)
  const [conversationError, setConversationError] = useState('')
  const [settings, setSettings] = useState(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [settingsExpandRequest, setSettingsExpandRequest] = useState(null)
  const [quitConfirmOpen, setQuitConfirmOpen] = useState(false)
  const [quitting, setQuitting] = useState(false)
  const [screenshotBlob, setScreenshotBlob] = useState(null)
  const [capturingScreenshot, setCapturingScreenshot] = useState(false)
  const [serverStopped, setServerStopped] = useState(false)
  const [ragSettings, setRagSettings] = useState(null)
  const [fileAccessSettings, setFileAccessSettings] = useState({ root: '', read_enabled: false, write_enabled: false })
  const [cloudProviderSettings, setCloudProviderSettings] = useState({ anthropic_configured: false, openai_configured: false, gemini_configured: false })
  const [thinkLevel, setThinkLevel] = useState('medium')
  const [theme, setTheme] = useTheme()
  const offline = useConnectionStatus()
  const [offlineDismissed, setOfflineDismissed] = useState(false)

  useEffect(() => {
    if (!offline) setOfflineDismissed(false)
  }, [offline])

  const [fontFamily, setFontFamily] = useFontFamily()
  const [fontScale, setFontScale] = useFontScale()
  const [sectionOrder, moveSection] = useSectionOrder(DEFAULT_SECTION_ORDER)
  const [sidebarCollapsed, setSidebarCollapsed] = useSidebarCollapsed()
  const [tweakBarOpen, setTweakBarOpen] = useState(false)
  const [paletteMode, setPaletteMode] = useState(null)

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
    fetchFileAccessSettings().then((s) => setFileAccessSettings(s ?? { root: '', read_enabled: false, write_enabled: false }))
    fetchCloudProviderSettings().then((s) =>
      setCloudProviderSettings(s ?? { anthropic_configured: false, openai_configured: false, gemini_configured: false }),
    )
  }, [setModel])

  const commands = useMemo(
    () =>
      buildCommands({
        setSidebarCollapsed,
        setOpenFolderSignal: () => handleOpenSettings('fileAccess'),
        setQuitConfirmOpen,
        isWails,
      }),
    [setSidebarCollapsed]
  )

  useEffect(() => {
    function handleKeyDown(e) {
      if (!(e.ctrlKey || e.metaKey)) return
      const key = e.key.toLowerCase()
      if (key === 'p' && e.shiftKey) {
        e.preventDefault()
        setPaletteMode('commands')
        return
      }
      const cmd = commands.find((c) => c.key === key && c.shift === e.shiftKey)
      if (cmd) {
        e.preventDefault()
        cmd.run()
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [commands])

  useEffect(() => {
    conversationIdRef.current = conversationId
    if (conversationId) setLiveTarget(conversationId)
  }, [conversationId])

  useEffect(() => {
    const controller = new AbortController()
    let reconnectTimer = null

    function connect() {
      subscribeLiveChat(
        {
          onEvent: (event) => {
            const currentId = conversationIdRef.current
            if (String(event.conversation_id) !== String(currentId)) return

            if (event.type === 'token') {
              setMessages((prev) => {
                if (prev.length === 0) return prev
                const last = prev[prev.length - 1]
                if (last.role !== 'assistant') return prev
                const next = [...prev]
                next[next.length - 1] = { ...last, content: last.content + event.token }
                return next
              })
            } else if (event.type === 'user_message') {
              setMessages((prev) => [
                ...prev,
                { role: 'user', content: event.content, source: 'terminal' },
                { role: 'assistant', content: '', sources: [], reasoning: '', toolCalls: [], pendingWrites: [], buildChecks: [], testChecks: [], usage: null },
              ])
            } else if (event.type === 'reasoning') {
              setMessages((prev) => {
                if (prev.length === 0) return prev
                const last = prev[prev.length - 1]
                if (last.role !== 'assistant') return prev
                const next = [...prev]
                next[next.length - 1] = { ...last, reasoning: (last.reasoning || '') + event.reasoning }
                return next
              })
            } else if (event.type === 'done') {
              refreshConversations()
            }
          },
        },
        controller.signal
      ).catch((err) => {
        if (controller.signal.aborted) return
        console.error('live chat subscription dropped, retrying in 2s:', err)
        reconnectTimer = setTimeout(connect, 2000)
      })
    }

    connect()

    return () => {
      controller.abort()
      if (reconnectTimer) clearTimeout(reconnectTimer)
    }
  }, [])

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
    fetchMessages(id).then((msgs) => {
      setConversationId((current) => {
        if (String(current) === String(id)) {
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
  }

  function handleCloseChat() {
    startNewChat()
  }

  function stopStreaming() {
    abortControllerRef.current?.abort()
  }

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
    setMessages((prev) => [
      ...prev,
      { role: 'user', content: text, images },
      { role: 'assistant', content: '', sources: [], reasoning: '', toolCalls: [], pendingWrites: [], buildChecks: [], testChecks: [], usage: null },
    ])

    const controller = new AbortController()
    abortControllerRef.current = controller

    try {
      await streamChat(
        { message: text, model, skillId, conversationId, thinkLevel, images },
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
                toolCalls: [...(last.toolCalls ?? []), call],
              }
              return next
            })
          },
          onPendingWrite: (write) => {
            setMessages((prev) => {
              const next = [...prev]
              const last = next[next.length - 1]
              const existing = last.pendingWrites ?? []
              if (existing.some((w) => w.id === write.id)) return prev
              next[next.length - 1] = {
                ...last,
                pendingWrites: [...existing, { ...write, status: 'pending' }],
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
                buildChecks: [...(last.buildChecks ?? []), check],
              }
              return next
            })
          },
          onTestCheck: (check) => {
            setMessages((prev) => {
              const next = [...prev]
              const last = next[next.length - 1]
              next[next.length - 1] = {
                ...last,
                testChecks: [...(last.testChecks ?? []), check],
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

  async function indexFile(file, label) {
    setDocStatus(`Uploading ${label}…`)
    try {
      const res = await uploadFile(file, label)
      if (!res.ok) {
        const message = await res.text()
        if (res.status === 400 && message.includes('no extractable text')) {
          setDocStatus(`Skipped ${label}: empty file.`)
          return { status: 'skipped' }
        }
        throw new Error(message)
      }
      const data = await res.json()
      setDocStatus(`Indexed ${data.chunks} chunk(s) from ${label}.`)
      refreshDocuments()
      return { status: 'indexed', chunks: data.chunks }
    } catch (err) {
      setDocStatus(`Couldn't upload ${label}: ${err.message}`)
      return { status: 'error', message: err.message }
    }
  }

  async function uploadDocument(e) {
    e.preventDefault()
    if (!docText.trim()) return
    const filename = `snippet-${Date.now()}.txt`
    await indexContent(filename, docText)
    setDocText('')
  }

  async function indexFileList(files, { relativeLabel = false } = {}) {
    const candidates = []
    for (const file of files) {
      const relPath = file.webkitRelativePath || file.name
      if (isInExcludedDir(relPath)) continue
      if (isExcludedFilename(file.name)) continue
      if (!UPLOAD_FILE_PATTERN.test(file.name)) continue
      candidates.push({ file, label: relativeLabel && file.webkitRelativePath ? file.webkitRelativePath : file.name })
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
    e.target.value = ''
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

  async function handleSaveCloudProviderSettings(next) {
    const res = await saveCloudProviderSettings(next)
    if (!res.ok) throw new Error(await res.text())
    const saved = await res.json()
    setCloudProviderSettings(saved)
    fetchModels().then(setModels)
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
      setSkillError(`Couldn't create this skill: ${err.message}`)
    }
  }

  async function confirmQuit() {
    setQuitting(true)
    try {
      await quitServer()
    } catch {
      // ignore
    }
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
      alert(`Couldn't capture a screenshot: ${err.message}`)
    } finally {
      setCapturingScreenshot(false)
    }
  }

  function handleOpenSettings(subSection) {
    if (subSection) setSettingsExpandRequest(subSection)
    setSettingsOpen(true)
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

  return (
    <div className="app">
      {isWails() && (
        <TopBar
          onOpenFolder={() => handleOpenSettings('fileAccess')}
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
          {isWails() && (
            <button
              type="button"
              title="Screenshot"
              disabled={capturingScreenshot}
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
          <div className="view-rail-spacer" />
          <button type="button" className="view-rail-settings" title="Settings" onClick={() => handleOpenSettings()}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <circle cx="12" cy="12" r="3" />
              <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1Z" />
            </svg>
          </button>
          {!isWails() && (
            <button type="button" title="Quit fastllm" onClick={() => setQuitConfirmOpen(true)}>
              ⏻
            </button>
          )}
        </nav>

        <div className="main-row">
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
          />

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

      {paletteMode && (
        <CommandPalette
          mode={paletteMode}
          files={[]}
          commands={commands}
          onOpenFile={() => {}}
          onClose={() => setPaletteMode(null)}
        />
      )}

      <TweakBar open={tweakBarOpen} onToggle={() => setTweakBarOpen(true)} />

      <span className="app-version" title={__BUILD_TIME__}>
        v{__APP_VERSION__}
      </span>
    </div>
  )
}
