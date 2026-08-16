import { useEffect, useRef, useState } from 'react'
import './App.css'
import ConversationList from './components/ConversationList'
import ModelPicker from './components/ModelPicker'
import SkillPanel from './components/SkillPanel'
import KnowledgeBasePanel from './components/KnowledgeBasePanel'
import ChatPanel from './components/ChatPanel'
import DeleteConversationModal from './components/DeleteConversationModal'
import {
  fetchConversations,
  fetchMessages,
  deleteConversation,
  fetchDocuments,
  fetchSkills,
  fetchModels,
  createSkill as apiCreateSkill,
  deleteSkillById,
  indexDocument,
  streamChat,
} from './api'

// Text files we accept for direct upload — anything else likely needs
// server-side extraction (e.g. PDFs) which isn't wired up yet.
const TEXT_FILE_PATTERN = /\.(txt|md|markdown|mdx|json|ya?ml|csv|tsv|log|go|js|jsx|ts|tsx|py|rb|java|c|cc|cpp|h|hpp|rs|sh|sql|html|css|xml)$/i

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
  const bottomRef = useRef(null)
  const fileInputRef = useRef(null)

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
    setDocStatus('Uploading…')
    try {
      const res = await indexDocument(filename, content)
      if (!res.ok) throw new Error(await res.text())
      const data = await res.json()
      setDocStatus(`Indexed ${data.chunks} chunk(s) from ${filename}.`)
      refreshDocuments()
    } catch (err) {
      setDocStatus(`Error: ${err.message}`)
    }
  }

  async function uploadDocument(e) {
    e.preventDefault()
    if (!docText.trim()) return
    await indexContent('pasted-text.txt', docText)
    setDocText('')
  }

  async function handleFilePicked(e) {
    const files = Array.from(e.target.files ?? [])
    e.target.value = '' // allow re-selecting the same file later

    for (const file of files) {
      if (!TEXT_FILE_PATTERN.test(file.name)) {
        setDocStatus(`Skipped ${file.name}: unsupported file type.`)
        continue
      }
      const text = await file.text()
      await indexContent(file.name, text)
    }
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
    </div>
  )
}
