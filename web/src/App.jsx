import { useEffect, useMemo, useRef, useState } from 'react'
import './App.css'
import ConversationList from './components/ConversationList'
import ModelPicker from './components/ModelPicker'
import ChatPanel from './components/ChatPanel'
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
  streamHarnessRun,
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

function BoltIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true" {...props}>
      <polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2" />
    </svg>
  )
}

function SettingsIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1Z" />
    </svg>
  )
}

function PowerIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <path d="M18.36 6.64a9 9 0 1 1-12.73 0M12 2v10" />
    </svg>
  )
}

function CloseIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <line x1="18" y1="6" x2="6" y2="18" />
      <line x1="6" y1="6" x2="18" y2="18" />
    </svg>
  )
}

export default function App() {
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

  useEffect(() => {
    function handleKeyDown(e) {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setCommandPaletteOpen((prev) => !prev)
      } else if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.key.toLowerCase() === 'p') {
        e.preventDefault()
        setCommandPaletteOpen((prev) => !prev)
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [])

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

  async function sendMessage(e, mode = 'chat', agentConfig = {}) {
    e.preventDefault()
    const trimmed = input.trim()
    if (!trimmed && pendingImages.length === 0) return
    if (streaming) return

    if (mode === 'agent') {
      const userMessage = {
        role: 'user',
        content: trimmed,
        mode: 'agent',
      }
      setMessages((prev) => [...prev, userMessage])
      setInput('')
      setStreaming(true)

      const assistantPlaceholder = {
        role: 'assistant',
        content: '',
        isAgent: true,
        turns: [],
        currentTurn: { turn: 1, toolCalls: [] },
        finalResult: null,
      }
      setMessages((prev) => [...prev, assistantPlaceholder])

      const ac = new AbortController()
      streamAbortRef.current = ac

      let activeTurn = { turn: 1, toolCalls: [] }

      const priorTurns = messages.slice(-8).map((m) => ({
        role: m.role,
        content: m.content || '',
      }))

      const payload = {
        task: trimmed,
        conversation_id: conversationId || undefined,
        working_dir: agentConfig?.workingDir?.trim() || fileAccessSettings?.root || undefined,
        model: model || undefined,
        max_turns: Number(agentConfig?.maxTurns) || 20,
        allow_commands: agentConfig?.allowCommands ?? true,
        initial_messages: priorTurns,
      }

      try {
        await streamHarnessRun(
          payload,
          (ev) => {
            if (ev.type === 'conversation' && ev.conversation_id) {
              if (!conversationIdRef.current) {
                setConversationId(ev.conversation_id)
                refreshConversations()
              }
            } else if (ev.type === 'turn_start') {
              activeTurn = { turn: ev.turn, toolCalls: [] }
              setMessages((prev) => {
                const last = prev[prev.length - 1]
                if (!last || last.role !== 'assistant') return prev
                return [...prev.slice(0, -1), { ...last, currentTurn: { ...activeTurn } }]
              })
            } else if (ev.type === 'tool_call') {
              if (ev.tool_call) {
                activeTurn.toolCalls = [...activeTurn.toolCalls, { ...ev.tool_call, pending: true }]
                setMessages((prev) => {
                  const last = prev[prev.length - 1]
                  if (!last || last.role !== 'assistant') return prev
                  return [...prev.slice(0, -1), { ...last, currentTurn: { ...activeTurn } }]
                })
              }
            } else if (ev.type === 'tool_result') {
              if (ev.tool_call) {
                activeTurn.toolCalls = activeTurn.toolCalls.map((tc) =>
                  tc.id === ev.tool_call.id ? { ...ev.tool_call, pending: false } : tc
                )
                setMessages((prev) => {
                  const last = prev[prev.length - 1]
                  if (!last || last.role !== 'assistant') return prev
                  return [...prev.slice(0, -1), { ...last, currentTurn: { ...activeTurn } }]
                })
              }
            } else if (ev.type === 'turn_complete') {
              if (ev.metrics) activeTurn.metrics = ev.metrics
              if (ev.response) activeTurn.response = ev.response
              const completedTurn = { ...activeTurn }
              setMessages((prev) => {
                const last = prev[prev.length - 1]
                if (!last || last.role !== 'assistant') return prev
                return [
                  ...prev.slice(0, -1),
                  {
                    ...last,
                    turns: [...(last.turns || []), completedTurn],
                    currentTurn: null,
                  },
                ]
              })
            } else if (ev.type === 'task_finished') {
              setMessages((prev) => {
                const last = prev[prev.length - 1]
                if (!last || last.role !== 'assistant') return prev
                return [
                  ...prev.slice(0, -1),
                  {
                    ...last,
                    content: ev.result?.final_response || (ev.result?.success ? 'Task completed successfully.' : ''),
                    finalResult: ev.result,
                    error: ev.error,
                    currentTurn: null,
                  },
                ]
              })
            }
          },
          (err) => {
            setMessages((prev) => {
              const last = prev[prev.length - 1]
              if (!last || last.role !== 'assistant') return prev
              return [
                ...prev.slice(0, -1),
                {
                  ...last,
                  error: err.message,
                  isError: true,
                  currentTurn: null,
                },
              ]
            })
          },
          ac.signal
        )
      } catch (err) {
        if (err.name !== 'AbortError') {
          setMessages((prev) => {
            const last = prev[prev.length - 1]
            if (!last || last.role !== 'assistant') return prev
            return [
              ...prev.slice(0, -1),
              { ...last, error: err.message, isError: true, currentTurn: null },
            ]
          })
        }
      } finally {
        setStreaming(false)
        streamAbortRef.current = null
        refreshConversations()
      }
      return
    }

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
          <button type="button" onClick={() => setOfflineDismissed(true)}>
            <CloseIcon className="inline-icon" />
          </button>
        </div>
      )}

      <TopBar
        onOpenFolder={() => setOpenFolderSignal((s) => s + 1)}
        onQuit={() => setQuitConfirmOpen(true)}
      />

      <div className="app-body">
        <nav className="header-nav">
          <div className="header-brand">
            <BoltIcon className="brand-logo" />
            <span className="brand-title">fastllm</span>
          </div>

          <div className="header-actions">
            <button
              type="button"
              className="btn-settings-icon"
              title="Settings"
              onClick={() => handleOpenSettings(null)}
            >
              <SettingsIcon className="inline-icon" />
            </button>
            <button
              type="button"
              className="btn-quit-icon"
              title="Quit fastllm"
              onClick={() => setQuitConfirmOpen(true)}
            >
              <PowerIcon className="inline-icon" />
            </button>
          </div>
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

      {commandPaletteOpen && (
        <CommandPalette
          open={commandPaletteOpen}
          onClose={() => setCommandPaletteOpen(false)}
          commands={commands}
        />
      )}

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
