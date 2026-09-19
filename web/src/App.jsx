import { useEffect, useMemo, useRef, useState } from 'react'
import './App.css'
import ConversationList from './components/ConversationList'
import ModelPicker from './components/ModelPicker'
import ChatPanel from './components/ChatPanel'
import HarnessPanel from './components/HarnessPanel'
import ConfirmDeleteModal from './components/ConfirmDeleteModal'
import CommandPalette from './components/CommandPalette'
import SettingsPanel from './components/SettingsPanel'
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
import {
  fetchConversations,
  fetchMessages,
  deleteConversation,
  fetchModels,
  fetchSettings,
  fetchFileAccessSettings,
  saveFileAccessSettings,
  fetchCloudProviderSettings,
  saveCloudProviderSettings,
  clearConversations,
  streamChat,
  subscribeLiveChat,
  setLiveTarget,
  quitServer,
} from './api'
import { buildCommands } from './commands'

const DEFAULT_SECTION_ORDER = ['conversations', 'model']

const SECTION_LABELS = {
  conversations: 'Conversations',
  model: 'Model',
}

export default function App() {
  const [activeTab, setActiveTab] = useState('chat') // 'chat' | 'harness'
  const [messages, setMessages] = useState([])
  const [messagesLoading, setMessagesLoading] = useState(false)
  const [input, setInput] = useState('')
  const [pendingImages, setPendingImages] = useState([])
  const [composerImageError, setComposerImageError] = useState('')
  const [streaming, setStreaming] = useState(false)
  const [models, setModels] = useState([])
  const [model, setModel] = useModel()

  useEffect(() => {
    setComposerImageError('')
  }, [model])

  const [conversations, setConversations] = useState([])
  const [conversationId, setConversationId] = useState(null)
  const conversationIdRef = useRef(null)
  const [conversationError, setConversationError] = useState('')
  const [settings, setSettings] = useState(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [settingsExpandRequest, setSettingsExpandRequest] = useState(null)
  const [quitConfirmOpen, setQuitConfirmOpen] = useState(false)
  const [quitting, setQuitting] = useState(false)
  const [serverStopped, setServerStopped] = useState(false)
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
  const [sidebarCollapsed, setSidebarCollapsed] = useSidebarCollapsed()
  const [sectionOrder, moveSection] = useSectionOrder(DEFAULT_SECTION_ORDER)
  const [commandPaletteOpen, setCommandPaletteOpen] = useState(false)
  const [openFolderSignal, setOpenFolderSignal] = useState(0)

  const streamAbortRef = useRef(null)
  const bottomRef = useRef(null)

  useEffect(() => {
    conversationIdRef.current = conversationId
    setLiveTarget(conversationId).catch(() => {})
  }, [conversationId])

  useEffect(() => {
    refreshConversations()
    refreshModels()
    fetchSettings().then(setSettings)
    refreshFileAccessSettings()
    refreshCloudProviderSettings()
  }, [])

  useEffect(() => {
    const ac = new AbortController()
    subscribeLiveChat(
      {
        onEvent: (event) => {
          if (event.type === 'message' && event.message) {
            setMessages((prev) => {
              if (prev.some((m) => m.id === event.message.id)) return prev
              return [...prev, event.message]
            })
          }
        },
      },
      ac.signal
    ).catch(() => {})
    return () => ac.abort()
  }, [])

  function refreshConversations() {
    fetchConversations().then(setConversations)
  }

  function refreshModels() {
    fetchModels().then((data) => {
      setModels(data)
      if (data && data.length > 0 && !model) {
        setModel(data[0].name || data[0].id)
      }
    })
  }

  function refreshFileAccessSettings() {
    fetchFileAccessSettings().then((s) => {
      if (s) setFileAccessSettings(s)
    })
  }

  function refreshCloudProviderSettings() {
    fetchCloudProviderSettings().then((s) => {
      if (s) setCloudProviderSettings(s)
    })
  }

  const maxPendingImages = 4
  const maxImageBytes = 8 * 1024 * 1024
  const visionSupported = !!models.find((m) => m.name === model)?.supports_vision

  function handleComposerPaste(e) {
    if (!visionSupported) return
    const items = e.clipboardData?.items
    if (!items) return
    for (const item of items) {
      if (item.type.startsWith('image/')) {
        const file = item.getAsFile()
        if (!file) continue
        e.preventDefault()
        if (pendingImages.length >= maxPendingImages) {
          setComposerImageError(`Can't attach more than ${maxPendingImages} images.`)
          return
        }
        if (file.size > maxImageBytes) {
          setComposerImageError(`Image exceeds ${maxImageBytes / 1024 / 1024}MB limit.`)
          return
        }
        setComposerImageError('')
        const reader = new FileReader()
        reader.onload = () => {
          setPendingImages((prev) => [...prev, { name: file.name || 'Pasted image', dataUri: reader.result }])
        }
        reader.readAsDataURL(file)
        return
      }
    }
  }

  function removePendingImage(index) {
    setPendingImages((prev) => prev.filter((_, i) => i !== index))
  }

  function openConversation(id) {
    if (streaming) return
    setConversationId(id)
    setMessagesLoading(true)
    fetchMessages(id)
      .then((msgs) => {
        setMessages(msgs)
        setMessagesLoading(false)
      })
      .catch((err) => {
        setConversationError(`Failed to load messages: ${err.message}`)
        setMessagesLoading(false)
      })
  }

  function startNewChat() {
    if (streaming) return
    setConversationId(null)
    setMessages([])
    setInput('')
    setPendingImages([])
    setActiveTab('chat')
  }

  function handleCloseChat() {
    if (streaming) return
    startNewChat()
  }

  async function handleDeleteConversation(id) {
    if (streaming) return
    try {
      await deleteConversation(id)
      if (conversationId === id) {
        startNewChat()
      }
      refreshConversations()
    } catch (err) {
      setConversationError(`Failed to delete conversation: ${err.message}`)
    }
  }

  async function handleClearConversations() {
    await clearConversations()
    startNewChat()
    refreshConversations()
  }

  function stopStreaming() {
    if (streamAbortRef.current) {
      streamAbortRef.current.abort()
      streamAbortRef.current = null
    }
    setStreaming(false)
  }

  async function sendMessage(e) {
    e.preventDefault()
    const trimmed = input.trim()
    if (!trimmed && pendingImages.length === 0) return
    if (streaming) return

    const userMessage = {
      role: 'user',
      content: trimmed,
      images: pendingImages.map((img) => img.dataUri),
    }

    setMessages((prev) => [...prev, userMessage])
    setInput('')
    const imagesToSend = [...pendingImages]
    setPendingImages([])
    setStreaming(true)

    const assistantPlaceholder = {
      role: 'assistant',
      content: '',
      reasoning: '',
      toolCalls: [],
    }
    setMessages((prev) => [...prev, assistantPlaceholder])

    const ac = new AbortController()
    streamAbortRef.current = ac

    try {
      await streamChat(
        trimmed,
        model,
        conversationId,
        thinkLevel,
        {
          onConversation: (id) => {
            if (!conversationId) {
              setConversationId(id)
              refreshConversations()
            }
          },
          onToken: (token) => {
            setMessages((prev) => {
              const last = prev[prev.length - 1]
              if (!last || last.role !== 'assistant') return prev
              return [...prev.slice(0, -1), { ...last, content: last.content + token }]
            })
          },
          onReasoning: (delta) => {
            setMessages((prev) => {
              const last = prev[prev.length - 1]
              if (!last || last.role !== 'assistant') return prev
              return [...prev.slice(0, -1), { ...last, reasoning: (last.reasoning || '') + delta }]
            })
          },
          onToolCall: (tc) => {
            setMessages((prev) => {
              const last = prev[prev.length - 1]
              if (!last || last.role !== 'assistant') return prev
              return [...prev.slice(0, -1), { ...last, toolCalls: [...(last.toolCalls || []), tc] }]
            })
          },
          onUsage: (u) => {
            setMessages((prev) => {
              const last = prev[prev.length - 1]
              if (!last || last.role !== 'assistant') return prev
              return [...prev.slice(0, -1), { ...last, usage: u }]
            })
          },
          onError: (err) => {
            setMessages((prev) => {
              const last = prev[prev.length - 1]
              if (!last || last.role !== 'assistant') return prev
              return [...prev.slice(0, -1), { ...last, content: last.content + `\n\n[Error: ${err}]`, isError: true }]
            })
          },
        },
        ac.signal,
        imagesToSend.map((i) => i.dataUri)
      )
    } catch (err) {
      if (err.name !== 'AbortError') {
        setMessages((prev) => [
          ...prev,
          { role: 'assistant', content: `Request failed: ${err.message}`, isError: true },
        ])
      }
    } finally {
      setStreaming(false)
      streamAbortRef.current = null
      refreshConversations()
    }
  }

  function handleOpenSettings(expandTarget) {
    setSettingsExpandRequest(expandTarget)
    setSettingsOpen(true)
  }

  async function confirmQuit() {
    setQuitting(true)
    try {
      await quitServer()
      setServerStopped(true)
    } catch {
      setServerStopped(true)
    }
  }

  const commands = useMemo(
    () =>
      buildCommands({
        setSidebarCollapsed,
        setOpenFolderSignal,
        setQuitConfirmOpen,
        isWails: () => false,
      }),
    [setSidebarCollapsed]
  )

  if (serverStopped) {
    return (
      <div className="server-stopped-screen">
        <div className="server-stopped-box">
          <h2>fastllm has stopped</h2>
          <p>The local server was shut down. You can safely close this browser window or tab.</p>
        </div>
      </div>
    )
  }

  return (
    <div className="app-shell">
      {offline && !offlineDismissed && (
        <div className="offline-banner" role="alert">
          <span>Backend unreachable — check if fastllm server is running.</span>
          <button type="button" onClick={() => setOfflineDismissed(true)}>✕</button>
        </div>
      )}

      <TopBar
        onOpenFolder={() => setOpenFolderSignal((s) => s + 1)}
        onQuit={() => setQuitConfirmOpen(true)}
      />

      <div className="app-body">
        <nav className="header-nav">
          <div className="header-brand">
            <span className="brand-logo">⚡</span>
            <span className="brand-title">fastllm</span>
          </div>

          <div className="view-mode-tabs">
            <button
              type="button"
              className={`view-mode-tab ${activeTab === 'chat' ? 'is-active' : ''}`}
              onClick={() => setActiveTab('chat')}
            >
              💬 Chat
            </button>
            <button
              type="button"
              className={`view-mode-tab ${activeTab === 'harness' ? 'is-active' : ''}`}
              onClick={() => setActiveTab('harness')}
            >
              ⚡ Autonomous Agent
            </button>
          </div>

          <div className="header-actions">
            <button
              type="button"
              className="btn-settings-icon"
              title="Settings"
              onClick={() => handleOpenSettings(null)}
            >
              ⚙
            </button>
            <button
              type="button"
              className="btn-quit-icon"
              title="Quit fastllm"
              onClick={() => setQuitConfirmOpen(true)}
            >
              ⏻
            </button>
          </div>
        </nav>

        <div className="main-row">
          {activeTab === 'harness' ? (
            <HarnessPanel
              models={models}
              selectedModel={model}
              onSelectModel={setModel}
              defaultWorkingDir={fileAccessSettings?.root || ''}
            />
          ) : (
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
              pendingImages={pendingImages}
              composerImageError={composerImageError}
              onComposerPaste={handleComposerPaste}
              onRemovePendingImage={removePendingImage}
              visionSupported={visionSupported}
            />
          )}

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

      {quitConfirmOpen && (
        <ConfirmDeleteModal
          heading="Quit fastllm?"
          description="This stops the local server. Any open browser tabs will stop working until you relaunch it."
          confirmLabel={quitting ? 'Quitting…' : 'Quit'}
          onCancel={() => setQuitConfirmOpen(false)}
          onConfirm={confirmQuit}
        />
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
          fileAccessSettings={fileAccessSettings}
          onSaveFileAccessSettings={saveFileAccessSettings}
          cloudProviderSettings={cloudProviderSettings}
          onSaveCloudProviderSettings={saveCloudProviderSettings}
          thinkLevel={thinkLevel}
          onThinkLevelChange={setThinkLevel}
          onClearConversations={handleClearConversations}
          expandSection={settingsExpandRequest}
          onClose={() => {
            setSettingsOpen(false)
            setSettingsExpandRequest(null)
            refreshModels()
            refreshFileAccessSettings()
            refreshCloudProviderSettings()
          }}
        />
      )}

      <CommandPalette
        open={commandPaletteOpen}
        onClose={() => setCommandPaletteOpen(false)}
        commands={commands}
      />

      <TweakBar
        theme={theme}
        onThemeChange={setTheme}
        fontFamily={fontFamily}
        onFontFamilyChange={setFontFamily}
        fontScale={fontScale}
        onFontScaleChange={setFontScale}
      />
    </div>
  )
}
