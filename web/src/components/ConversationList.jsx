export default function ConversationList({
  conversations,
  conversationId,
  onNewChat,
  onOpen,
  onRequestDelete,
  error,
}) {
  return (
    <section className="panel">
      <h2>Conversations</h2>
      <button type="button" className="btn-secondary" onClick={onNewChat}>
        + New chat
      </button>

      {error && <p className="status status-error">{error}</p>}

      <ul className="conversation-list">
        {conversations.length === 0 && (
          <li className="doc-empty">No conversations yet</li>
        )}
        {conversations.map((c) => (
          <li
            key={c.id}
            className={`conversation-item ${String(c.id) === String(conversationId) ? 'active' : ''}`}
          >
            <button
              type="button"
              className="conversation-title"
              onClick={() => onOpen(c.id)}
              title={c.title}
            >
              {c.title}
            </button>
            <button
              type="button"
              className="btn-icon"
              title="Delete this conversation"
              onClick={() => onRequestDelete(c.id)}
            >
              ×
            </button>
          </li>
        ))}
      </ul>
    </section>
  )
}
