import MessageContent from './MessageContent'
import PendingWriteCard from './PendingWriteCard'

export default function ChatPanel({
  messages,
  messagesLoading,
  bottomRef,
  input,
  onInputChange,
  streaming,
  onSendMessage,
  userDisplayName,
  onApproveWrite,
  onRejectWrite,
}) {
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

      <form className="composer" onSubmit={onSendMessage}>
        <input
          value={input}
          onChange={(e) => onInputChange(e.target.value)}
          placeholder="Ask something…"
          disabled={streaming}
        />
        <button type="submit" className="btn-primary" disabled={streaming || !input.trim()}>
          {streaming ? 'Sending…' : 'Send'}
        </button>
      </form>
    </main>
  )
}
