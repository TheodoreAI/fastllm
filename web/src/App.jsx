import { useEffect, useRef, useState } from 'react'
import './App.css'
import ConversationList from './components/ConversationList'
import ModelPicker from './components/ModelPicker'
import SkillPanel from './components/SkillPanel'
import KnowledgeBasePanel from './components/KnowledgeBasePanel'
import ChatPanel from './components/ChatPanel'
import EditorView from './components/EditorView'
import ConfirmDeleteModal from './components/ConfirmDeleteModal'
import SettingsPanel from './components/SettingsPanel'
import ScreenshotPreviewModal from './components/ScreenshotPreviewModal'
import DraggableSection from './components/DraggableSection'
import SectionIcon from './components/SectionIcon'
import { useTheme } from './useTheme'
import { useFontFamily } from './useFontFamily'
import { useFontScale } from './useFontScale'
import { useSectionOrder } from './useSectionOrder'
import { useSidebarCollapsed } from './useSidebarCollapsed'
import { useSplitWidth } from './useSplitWidth'
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
  const [streaming, setStreaming] = useState(false)
  const [docText, setDocText] = useState('')
  const [docStatus, setDocStatus] = useState('')
  const [documents, setDocuments] = useState([])
  const [models, setModels] = useState([])
  const [model, setModel] = useState('')
  const [skills, setSkills] = useState([])
  const [skillId, setSkillId] = useState('')
  const [skillFormOpen, setSkillFormOpen] = useState(false)
  const [skillName, setSkillName] = useState('')
  const [skillPrompt, setSkillPrompt] = useState('')
  const [skillToDelete, setSkillToDelete] = useState(null)
  const [skillError, setSkillError] = useState('')
  const [conversations, setConversations] = useState([])
  const [conversationId, setConversationId] = useState(null)
  const [conversationToDelete, setConversationToDelete] = useState(null)
  const [conversationError, setConversationError] = useState('')
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
  const [thinkLevel, setThinkLevel] = useState('medium')
  const [theme, setTheme] = useTheme()
  const [fontFamily, setFontFamily] = useFontFamily()
  const [fontScale, setFontScale] = useFontScale()
  const [sectionOrder, moveSection] = useSectionOrder(DEFAULT_SECTION_ORDER)
  const [sidebarCollapsed, setSidebarCollapsed] = useSidebarCollapsed()
  const [activeView, setActiveView] = useState('chat')
  const [openFolderSignal, setOpenFolderSignal] = useState(0)
  const [splitWidth, setSplitWidth] = useSplitWidth()
  const splitContainerRef = useRef(null)
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
  }, [])

  // Native File → Open Folder… menu item (see cmd/desktop/main.go) has no
  // direct line to React state, so it emits a Wails runtime event instead;
  // window.runtime only exists in the desktop build, hence the isWails()
  // guard — matches how the rest of this file feature-detects Wails (see
  // api.js's isWails doc comment) rather than importing @wailsjs/runtime.
  // Switches to the Editor tab (where the folder dialog's result — tree,
  // git status/branches — is actually visible) and bumps openFolderSignal,
  // which EditorView watches to re-run its own handleOpenFolder.
  useEffect(() => {
    if (!isWails()) return
    const unsubscribe = window.runtime.EventsOn('menu:open-folder', () => {
      setActiveView((v) => (v === 'chat' ? 'editor' : v))
      setOpenFolderSignal((n) => n + 1)
    })
    return unsubscribe
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
      // Guard against out-of-order responses: if the user switched to a
      // different conversation again before this fetch resolved, don't
      // stomp on the newer selection with stale data.
      setConversationId((current) => {
        if (String(current) === String(id)) {
          setMessages(msgs)
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

  function stopStreaming() {
    abortControllerRef.current?.abort()
  }

  // Drags the Chat|Editor split divider. Tracks mouse position directly
  // against the split container's own bounding box rather than delta
  // movement, so a fast drag can't desync from the cursor. Clamped to
  // 20-80% so neither pane can be dragged down to nothing.
  function handleSplitDragStart(e) {
    e.preventDefault()
    const container = splitContainerRef.current
    if (!container) return

    // Belt-and-suspenders alongside preventDefault: without this, a fast
    // drag can still start a text-selection drag across the rest of the
    // page (the mousedown target is a thin 5px divider, easy to graze
    // rather than hit squarely) — force it off for the duration of the
    // drag, then restore whatever the page's own default was.
    const previousUserSelect = document.body.style.userSelect
    document.body.style.userSelect = 'none'

    function handleMove(moveEvent) {
      const rect = container.getBoundingClientRect()
      const fraction = (moveEvent.clientX - rect.left) / rect.width
      setSplitWidth(Math.min(0.8, Math.max(0.2, fraction)))
    }
    function handleUp() {
      window.removeEventListener('mousemove', handleMove)
      window.removeEventListener('mouseup', handleUp)
      document.body.style.userSelect = previousUserSelect
    }
    window.addEventListener('mousemove', handleMove)
    window.addEventListener('mouseup', handleUp)
  }

  async function confirmDeleteConversation() {
    const id = conversationToDelete
    setConversationToDelete(null)
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

  async function sendMessage(e) {
    e.preventDefault()
    const text = input.trim()
    if (!text || streaming) return

    setInput('')
    setStreaming(true)
    setMessages((prev) => [...prev, { role: 'user', content: text }, { role: 'assistant', content: '', sources: [], reasoning: '', toolCalls: [], pendingWrites: [], buildChecks: [] }])

    const controller = new AbortController()
    abortControllerRef.current = controller

    try {
      await streamChat(
        { message: text, model, skillId, conversationId, thinkLevel },
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
      setDocStatus(`Error indexing ${filename}: ${err.message}`)
    }
  }

  async function indexFile(file, label) {
    setDocStatus(`Uploading ${label}…`)
    try {
      const res = await uploadFile(file)
      if (!res.ok) throw new Error(await res.text())
      const data = await res.json()
      setDocStatus(`Indexed ${data.chunks} chunk(s) from ${label}.`)
      refreshDocuments()
    } catch (err) {
      setDocStatus(`Error indexing ${label}: ${err.message}`)
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
  // the embedding backend with concurrent requests).
  async function indexFileList(files, { relativeLabel } = {}) {
    for (const file of files) {
      if (!UPLOAD_FILE_PATTERN.test(file.name)) {
        setDocStatus(`Skipped ${file.name}: unsupported file type.`)
        continue
      }
      const label = relativeLabel ? file.webkitRelativePath || file.name : file.name
      await indexFile(file, label)
    }
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
      setDocStatus(`Error clearing knowledge base: ${err.message}`)
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
    try {
      const res = await apiCreateSkill(skillName, skillPrompt)
      if (!res.ok) throw new Error(await res.text())
      setSkillName('')
      setSkillPrompt('')
      setSkillFormOpen(false)
      refreshSkills()
    } catch {
      // Best-effort: leave the form open with the user's input intact.
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
    } catch {
      // Nothing to recover into beyond leaving the preview modal unopened
      // — mirrors this codebase's other fetch-failure handling (e.g.
      // confirmQuit above), no separate error UI for a capture failure.
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

  async function handleApproveWrite(id) {
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
      <nav className="view-rail">
        <button
          type="button"
          className={activeView === 'chat' ? 'is-active' : ''}
          title="Chat"
          onClick={() => setActiveView('chat')}
        >
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M4 4h16v12H8l-4 4V4Z" />
          </svg>
        </button>
        <button
          type="button"
          className={activeView === 'editor' ? 'is-active' : ''}
          title="Editor"
          onClick={() => setActiveView('editor')}
        >
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9l-6-6Z" />
            <path d="M14 3v6h6" />
          </svg>
        </button>
        <button
          type="button"
          className={activeView === 'split' ? 'is-active' : ''}
          title="Split"
          onClick={() => setActiveView('split')}
        >
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <rect x="3" y="4" width="18" height="16" rx="2" />
            <path d="M12 4v16" />
          </svg>
        </button>

        <div className="view-rail-spacer" />

        {isWails() && (
          <button
            type="button"
            title="Screenshot"
            disabled={capturingScreenshot}
            onClick={handleScreenshot}
          >
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="M4 8h3l1.5-2h7L17 8h3a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V9a1 1 0 0 1 1-1Z" />
              <circle cx="12" cy="13.5" r="3.5" />
            </svg>
          </button>
        )}
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
      <div className="split-container" ref={splitContainerRef}>
      <div
        className="editor-pane"
        style={{
          display: activeView === 'editor' || activeView === 'split' ? undefined : 'none',
          flex: activeView === 'split' ? `0 0 ${splitWidth * 100}%` : undefined,
        }}
      >
        <EditorView
          fileAccessSettings={fileAccessSettings}
          onFileAccessSettingsChange={setFileAccessSettings}
          theme={theme}
          terminalEnabled={terminalSettings.enabled}
          visible={activeView === 'editor' || activeView === 'split'}
          openFolderSignal={openFolderSignal}
        />
      </div>

      {activeView === 'split' && (
        <div className="split-divider" onMouseDown={handleSplitDragStart} />
      )}

      <div
        className="body"
        style={{ display: activeView === 'chat' || activeView === 'split' ? undefined : 'none' }}
      >
        <ChatPanel
          messages={messages}
          messagesLoading={messagesLoading}
          conversationId={conversationId}
          bottomRef={bottomRef}
          input={input}
          onInputChange={setInput}
          streaming={streaming}
          onSendMessage={sendMessage}
          onStop={stopStreaming}
          userDisplayName={settings?.username}
          onApproveWrite={handleApproveWrite}
          onRejectWrite={handleRejectWrite}
        />
      </div>
      </div>

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
                    onRequestDelete={setConversationToDelete}
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

      {conversationToDelete != null && (
        <ConfirmDeleteModal
          heading="Delete conversation?"
          description={`This deletes "${conversations.find((c) => c.id === conversationToDelete)?.title ?? 'this conversation'}" and all its messages. This can't be undone.`}
          confirmLabel="Delete"
          onCancel={() => setConversationToDelete(null)}
          onConfirm={confirmDeleteConversation}
        />
      )}

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
          description="This stops the local server. Any open browser tabs will stop working until you relaunch it from the Desktop shortcut."
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
          thinkLevel={thinkLevel}
          onThinkLevelChange={setThinkLevel}
          onClearKnowledgeBase={handleClearKnowledgeBase}
          onClearConversations={handleClearConversations}
          expandSection={settingsExpandRequest}
          onClose={() => setSettingsOpen(false)}
        />
      )}
    </div>
  )
}
