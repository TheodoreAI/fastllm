import MessageContent from './MessageContent'

export default function ChatPanel({
  messages,
  messagesLoading,
  bottomRef,
  input,
  onInputChange,
  streaming,
  onSendMessage,
}) {
  return (
    <main className="chat">
      <div className="messages">
        {messagesLoading && <div className="messages-loading">Loading…</div>}
        {!messagesLoading && messages.map((m, i) => (
          <div key={i} className={`message ${m.role} ${m.isError ? 'is-error' : ''}`}>
            <span className="role">{m.role}</span>
            <MessageContent content={m.content} />
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
