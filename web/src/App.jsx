import { useEffect, useRef, useState } from 'react'
import './App.css'
import ConversationList from './components/ConversationList'
import ModelPicker from './components/ModelPicker'
import SkillPanel from './components/SkillPanel'
import KnowledgeBasePanel from './components/KnowledgeBasePanel'
import ChatPanel from './components/ChatPanel'
import ConfirmDeleteModal from './components/ConfirmDeleteModal'
import SettingsModal from './components/SettingsModal'
import DraggableSection from './components/DraggableSection'
import { useTheme } from './useTheme'
import { useFontFamily } from './useFontFamily'
import { useFontScale } from './useFontScale'
import { useSectionOrder } from './useSectionOrder'
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
  clearKnowledgeBase,
  clearConversations,
  createSkill as apiCreateSkill,
  deleteSkillById,
  indexDocument,
  uploadFile,
  streamChat,
  quitServer,
} from './api'

// Files we accept for upload: plain-text-like formats (indexed as-is,
// client never needs to read their bytes) plus PDF (extracted server-side).
const UPLOAD_FILE_PATTERN = /\.(txt|md|markdown|mdx|json|ya?ml|csv|tsv|log|go|js|jsx|ts|tsx|py|rb|java|c|cc|cpp|h|hpp|rs|sh|sql|html|css|xml|pdf)$/i

const DEFAULT_SECTION_ORDER = ['conversations', 'model', 'skills', 'knowledge']

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
  const [quitConfirmOpen, setQuitConfirmOpen] = useState(false)
  const [quitting, setQuitting] = useState(false)
  const [serverStopped, setServerStopped] = useState(false)
  const [ragSettings, setRagSettings] = useState(null)
  const [theme, setTheme] = useTheme()
  const [fontFamily, setFontFamily] = useFontFamily()
  const [fontScale, setFontScale] = useFontScale()
  const [sectionOrder, moveSection] = useSectionOrder(DEFAULT_SECTION_ORDER)
  const bottomRef = useRef(null)
  const fileInputRef = useRef(null)
  const folderInputRef = useRef(null)

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
    setMessages((prev) => [...prev, { role: 'user', content: text }, { role: 'assistant', content: '', sources: [], reasoning: '', toolCalls: [] }])

    try {
      await streamChat(
        { message: text, model, skillId, conversationId },
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
        }
      )
    } catch (err) {
      setMessages((prev) => [...prev, { role: 'assistant', content: `⚠️ ${err.message}`, isError: true }])
    } finally {
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

  if (serverStopped) {
    return (
      <div className="app">
        <div className="titlebar">
          <div className="traffic-lights">
            <span className="dot red" />
            <span className="dot yellow" />
            <span className="dot green" />
          </div>
          <span className="titlebar-title">fastllm</span>
        </div>
        <div className="stopped-state">
          <p className="stopped-title">fastllm has stopped.</p>
          <p className="stopped-hint">You can close this tab, or relaunch it from the Desktop shortcut.</p>
        </div>
      </div>
    )
  }

  return (
    <div className="app">
      <div className="titlebar">
        <div className="traffic-lights">
          <span className="dot red" />
          <span className="dot yellow" />
          <span className="dot green" />
        </div>
        <span className="titlebar-title">fastllm</span>
        <div className="titlebar-actions">
          <button
            type="button"
            className="titlebar-btn"
            title="Settings"
            onClick={() => setSettingsOpen(true)}
          >
            ⚙
          </button>
          <button
            type="button"
            className="titlebar-btn"
            title="Quit fastllm"
            onClick={() => setQuitConfirmOpen(true)}
          >
            ⏻
          </button>
        </div>
      </div>

      <div className="body">
        <aside className="sidebar">
          {sectionOrder.map((key, index) => {
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
              model: <ModelPicker models={models} model={model} onChange={setModel} />,
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

        <ChatPanel
          messages={messages}
          messagesLoading={messagesLoading}
          bottomRef={bottomRef}
          input={input}
          onInputChange={setInput}
          streaming={streaming}
          onSendMessage={sendMessage}
          userDisplayName={settings?.username}
        />
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

      {settingsOpen && (
        <SettingsModal
          theme={theme}
          onThemeChange={setTheme}
          fontFamily={fontFamily}
          onFontFamilyChange={setFontFamily}
          fontScale={fontScale}
          onFontScaleChange={setFontScale}
          settings={settings}
          ragSettings={ragSettings}
          onSaveRagSettings={handleSaveRagSettings}
          onClearKnowledgeBase={handleClearKnowledgeBase}
          onClearConversations={handleClearConversations}
          onClose={() => setSettingsOpen(false)}
        />
      )}
    </div>
  )
}
