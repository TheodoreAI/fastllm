import { useEffect, useRef, useState } from 'react'
import MessageContent from './MessageContent'
import PendingWriteCard from './PendingWriteCard'

export default function ChatPanel({
  messages,
  messagesLoading,
  conversationId,
  bottomRef,
  input,
  onInputChange,
  streaming,
  onSendMessage,
  onStop,
  userDisplayName,
  onRequestApproveWrite,
  onRejectWrite,
  pendingImages,
  composerImageError,
  onComposerPaste,
  onRemovePendingImage,
  visionSupported,
  activeEditorFile,
}) {
  // Shell-style prompt recall: ArrowUp/ArrowDown step through this
  // conversation's past user messages. historyIndex counts back from the
  // end (0 = not browsing, 1 = most recent prompt, 2 = the one before
  // that, ...). draftBeforeHistory holds whatever was being typed before
  // the user started browsing, restored when they arrow back past the
  // most recent entry.
  const [historyIndex, setHistoryIndex] = useState(0)
  const draftBeforeHistoryRef = useRef('')

  // Switching conversations invalidates any in-progress history browsing —
  // it no longer refers to a draft or a position in the new thread's list.
  useEffect(() => {
    setHistoryIndex(0)
    draftBeforeHistoryRef.current = ''
  }, [conversationId])

  const promptHistory = messages.filter((m) => m.role === 'user').map((m) => m.content)

  function handleComposerKeyDown(e) {
    if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return
    if (promptHistory.length === 0) return

    if (e.key === 'ArrowUp') {
      if (historyIndex >= promptHistory.length) return // already at the oldest prompt
      e.preventDefault()
      if (historyIndex === 0) draftBeforeHistoryRef.current = input
      const nextIndex = historyIndex + 1
      setHistoryIndex(nextIndex)
      onInputChange(promptHistory[promptHistory.length - nextIndex])
    } else {
      if (historyIndex === 0) return // not browsing, let the cursor/nothing happen
      e.preventDefault()
      const nextIndex = historyIndex - 1
      setHistoryIndex(nextIndex)
      onInputChange(nextIndex === 0 ? draftBeforeHistoryRef.current : promptHistory[promptHistory.length - nextIndex])
    }
  }

  function handleComposerChange(value) {
    // Any manual edit exits history-browsing mode so typing doesn't get
    // silently overwritten by a later arrow press.
    if (historyIndex !== 0) setHistoryIndex(0)
    onInputChange(value)
  }

  function handleSubmit(e) {
    setHistoryIndex(0)
    draftBeforeHistoryRef.current = ''
    onSendMessage(e)
  }

  return (
    <main className="chat">
      <div className="messages">
        {messagesLoading && <div className="messages-loading">Loading…</div>}
        {!messagesLoading && messages.map((m, i) => {
          const isLastMessage = i === messages.length - 1
          const isAwaitingResponse =
            streaming &&
            isLastMessage &&
            m.role === 'assistant' &&
            !m.content &&
            !m.reasoning &&
            (!m.toolCalls || m.toolCalls.length === 0)
          return (
          <div key={i} className={`message ${m.role} ${m.isError ? 'is-error' : ''}`}>
            <span className="role">{m.role === 'user' ? userDisplayName || 'user' : m.role}</span>
            {isAwaitingResponse && (
              <div className="typing-indicator" role="status" aria-label="Waiting for model response">
                <span></span><span></span><span></span>
              </div>
            )}
            {m.reasoning && (
              <details className="reasoning" open={!m.content}>
                <summary>{m.content ? 'Thinking' : 'Thinking…'}</summary>
                <p>{m.reasoning}</p>
              </details>
            )}
            {m.toolCalls && m.toolCalls.length > 0 && (
              <ul className="tool-calls">
                {m.toolCalls.map((call, ci) => (
                  <li key={ci} className={call.error ? 'is-error' : ''}>
                    📄 Read <code>{call.path}</code>
                    {call.truncated && ' (truncated)'}
                    {call.error && `: ${call.error}`}
                  </li>
                ))}
              </ul>
            )}
            {m.buildChecks && m.buildChecks.length > 0 && (
              <ul className="tool-calls build-checks">
                {m.buildChecks.map((check, bi) => (
                  <li key={bi} className={check.passed ? 'is-ok' : 'is-error'}>
                    <details>
                      <summary>{check.passed ? '✅ Build check passed' : '❌ Build check failed'}</summary>
                      <pre className="build-check-output">{check.output || '(no output)'}</pre>
                    </details>
                  </li>
                ))}
              </ul>
            )}
            {m.images && m.images.length > 0 && (
              <div className="message-images">
                {m.images.map((src, ii) => (
                  <img key={ii} src={src} alt="Attached" className="message-image" />
                ))}
              </div>
            )}
            <MessageContent content={m.content} />
            {m.pendingWrites && m.pendingWrites.length > 0 && (
              <div className="pending-writes">
                {m.pendingWrites.map((w) => (
                  <PendingWriteCard key={w.id} write={w} onRequestApprove={onRequestApproveWrite} onReject={onRejectWrite} />
                ))}
              </div>
            )}
            {m.sources && m.sources.length > 0 && (
              <details className="sources">
                <summary>{m.sources.length} source{m.sources.length === 1 ? '' : 's'}</summary>
                <ul>
                  {m.sources.map((s, si) => (
                    <li key={si}>{s.content}</li>
                  ))}
                </ul>
              </details>
            )}
          </div>
          )
        })}
        <div ref={bottomRef} />
      </div>

      <form className="composer-form" onSubmit={handleSubmit}>
        {composerImageError && <p className="composer-image-error">{composerImageError}</p>}
        {pendingImages && pendingImages.length > 0 && (
          <div className="composer-image-previews">
            {pendingImages.map((img, i) => (
              <div key={i} className="composer-image-preview">
                <img src={img.dataUri} alt={img.name} />
                <button
                  type="button"
                  className="composer-image-remove"
                  onClick={() => onRemovePendingImage(i)}
                  title="Remove image"
                  aria-label="Remove image"
                >
                  ×
                </button>
              </div>
            ))}
          </div>
        )}
        <div className="composer">
          <span
            className={`composer-vision-flag ${visionSupported ? 'is-supported' : 'is-unsupported'}`}
            title={visionSupported ? 'This model accepts pasted images.' : "This model doesn't accept images — paste is disabled."}
          >
            🖼{visionSupported ? '' : '🚫'}
          </span>
          <input
            value={input}
            onChange={(e) => handleComposerChange(e.target.value)}
            onKeyDown={handleComposerKeyDown}
            onPaste={onComposerPaste}
            placeholder={visionSupported ? 'Ask something… (paste an image to attach it)' : 'Ask something…'}
            disabled={streaming}
          />
          {streaming ? (
            <button type="button" className="btn-primary btn-stop" onClick={onStop}>
              ⏹ Stop
            </button>
          ) : (
            <button type="submit" className="btn-primary" disabled={!input.trim() && (!pendingImages || pendingImages.length === 0)}>
              Send
            </button>
          )}
        </div>
        {activeEditorFile && (
          <p className="composer-active-file" title={activeEditorFile}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" className="composer-active-file-icon">
              <path d="M6 2.5h8l4 4V21a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V3.5a1 1 0 0 1 1-1Z" />
              <path d="M14 2.5V6a1 1 0 0 0 1 1h3.5" />
            </svg>
            {activeEditorFile.split('/').pop()} is available to the model
          </p>
        )}
      </form>
    </main>
  )
}
