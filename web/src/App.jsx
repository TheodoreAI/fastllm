import { useEffect, useRef, useState } from 'react'
import './App.css'
import ConversationList from './components/ConversationList'
import ModelPicker from './components/ModelPicker'
import SkillPanel from './components/SkillPanel'
import KnowledgeBasePanel from './components/KnowledgeBasePanel'
import ChatPanel from './components/ChatPanel'
import DeleteConversationModal from './components/DeleteConversationModal'
import SettingsModal from './components/SettingsModal'
import { useTheme } from './useTheme'
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
} from './api'

// Files we accept for upload: plain-text-like formats (indexed as-is,
// client never needs to read their bytes) plus PDF (extracted server-side).
const UPLOAD_FILE_PATTERN = /\.(txt|md|markdown|mdx|json|ya?ml|csv|tsv|log|go|js|jsx|ts|tsx|py|rb|java|c|cc|cpp|h|hpp|rs|sh|sql|html|css|xml|pdf)$/i

export default function App() {
  const [messages, setMessages] = useState([])
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
  const [conversations, setConversations] = useState([])
  const [conversationId, setConversationId] = useState(null)
  const [conversationToDelete, setConversationToDelete] = useState(null)
  const [settings, setSettings] = useState(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [ragSettings, setRagSettings] = useState(null)
  const [theme, setTheme] = useTheme()
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
    fetchMessages(id).then(setMessages)
  }

  function startNewChat() {
    setConversationId(null)
    setMessages([])
  }

  async function confirmDeleteConversation() {
    const id = conversationToDelete
    setConversationToDelete(null)
    if (id == null) return
    try {
      await deleteConversation(id)
      if (String(conversationId) === String(id)) startNewChat()
      const list = await refreshConversations()
      if (String(conversationId) === String(id) && list.length > 0) openConversation(list[0].id)
    } catch {
      // ignore
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
    setMessages((prev) => [...prev, { role: 'user', content: text }, { role: 'assistant', content: '', sources: [] }])

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
        }
      )
    } catch (err) {
      setMessages((prev) => [...prev, { role: 'assistant', content: `Error: ${err.message}` }])
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
      await clearKnowledgeBase()
      setDocStatus('Knowledge base cleared.')
      refreshDocuments()
    } catch (err) {
      setDocStatus(`Error clearing knowledge base: ${err.message}`)
    }
  }

  async function handleClearConversations() {
    await clearConversations()
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

  async function deleteSkill(id) {
    try {
      await deleteSkillById(id)
      if (String(skillId) === String(id)) setSkillId('')
      refreshSkills()
    } catch {
      // ignore
    }
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
        </div>
      </div>

      <div className="body">
        <aside className="sidebar">
          <ConversationList
            conversations={conversations}
            conversationId={conversationId}
            onNewChat={startNewChat}
            onOpen={openConversation}
            onRequestDelete={setConversationToDelete}
          />

          <ModelPicker models={models} model={model} onChange={setModel} />

          <SkillPanel
            skills={skills}
            skillId={skillId}
            onSkillIdChange={setSkillId}
            onDeleteSkill={deleteSkill}
            skillFormOpen={skillFormOpen}
            onOpenForm={() => setSkillFormOpen(true)}
            onCloseForm={() => setSkillFormOpen(false)}
            skillName={skillName}
            onSkillNameChange={setSkillName}
            skillPrompt={skillPrompt}
            onSkillPromptChange={setSkillPrompt}
            onCreateSkill={createSkill}
          />

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
        </aside>

        <ChatPanel
          messages={messages}
          bottomRef={bottomRef}
          input={input}
          onInputChange={setInput}
          streaming={streaming}
          onSendMessage={sendMessage}
        />
      </div>

      {conversationToDelete != null && (
        <DeleteConversationModal
          title={conversations.find((c) => c.id === conversationToDelete)?.title ?? 'this conversation'}
          onCancel={() => setConversationToDelete(null)}
          onConfirm={confirmDeleteConversation}
        />
      )}

      {settingsOpen && (
        <SettingsModal
          theme={theme}
          onThemeChange={setTheme}
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
