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
  onApproveWrite,
  onRejectWrite,
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
        {!messagesLoading && messages.map((m, i) => (
          <div key={i} className={`message ${m.role} ${m.isError ? 'is-error' : ''}`}>
            <span className="role">{m.role === 'user' ? userDisplayName || 'user' : m.role}</span>
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
            <MessageContent content={m.content} />
            {m.pendingWrites && m.pendingWrites.length > 0 && (
              <div className="pending-writes">
                {m.pendingWrites.map((w) => (
                  <PendingWriteCard key={w.id} write={w} onApprove={onApproveWrite} onReject={onRejectWrite} />
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
        ))}
        <div ref={bottomRef} />
      </div>

      <form className="composer" onSubmit={handleSubmit}>
        <input
          value={input}
          onChange={(e) => handleComposerChange(e.target.value)}
          onKeyDown={handleComposerKeyDown}
          placeholder="Ask something…"
          disabled={streaming}
        />
        {streaming ? (
          <button type="button" className="btn-primary btn-stop" onClick={onStop}>
            ⏹ Stop
          </button>
        ) : (
          <button type="submit" className="btn-primary" disabled={!input.trim()}>
            Send
          </button>
        )}
      </form>
    </main>
  )
}
