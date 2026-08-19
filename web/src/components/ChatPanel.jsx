import { useEffect, useRef, useState } from 'react'
import MessageContent from './MessageContent'
import PendingWriteCard from './PendingWriteCard'

// Small inline icon set replacing the emoji ChatPanel used to render
// directly — emoji render inconsistently across platforms/fonts, while
// these follow the stroke-based currentColor style already used for the
// composer-active-file icon, so they inherit the surrounding text color
// and theme automatically.
function FileIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <path d="M6 2.5h8l4 4V21a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V3.5a1 1 0 0 1 1-1Z" />
      <path d="M14 2.5V6a1 1 0 0 0 1 1h3.5" />
    </svg>
  )
}

function CheckCircleIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <circle cx="12" cy="12" r="9" />
      <path d="M8 12.5l2.5 2.5L16 9.5" />
    </svg>
  )
}

function XCircleIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <circle cx="12" cy="12" r="9" />
      <path d="M9 9l6 6M15 9l-6 6" />
    </svg>
  )
}

function ImageIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <rect x="3" y="4" width="18" height="16" rx="2" />
      <circle cx="8.5" cy="9.5" r="1.5" fill="currentColor" stroke="none" />
      <path d="M21 15.5l-5.5-5.5L5 20" />
    </svg>
  )
}

function ImageOffIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <rect x="3" y="4" width="18" height="16" rx="2" />
      <circle cx="8.5" cy="9.5" r="1.5" fill="currentColor" stroke="none" />
      <path d="M21 15.5l-5.5-5.5L5 20" />
      <path d="M2.5 2.5l19 19" />
    </svg>
  )
}

function StopIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true" {...props}>
      <rect x="6" y="6" width="12" height="12" rx="2" />
    </svg>
  )
}

// Used on the composer's token-usage chips — an arrow into the model for
// prompt tokens, an arrow out for completion tokens, mirroring the
// in/out framing already used in the label text next to them.
function ArrowDownIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <path d="M12 4v14" />
      <path d="M6 12l6 6 6-6" />
    </svg>
  )
}

function ArrowUpIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <path d="M12 20V6" />
      <path d="M6 12l6-6 6 6" />
    </svg>
  )
}

// Used on the composer's rate-limit-remaining chip — a half-circle dial,
// evoking a fuel/capacity gauge for "how much headroom is left."
function GaugeIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <path d="M4 18a8 8 0 0 1 16 0" />
      <path d="M12 18l4-6" />
      <circle cx="12" cy="18" r="1.2" fill="currentColor" stroke="none" />
    </svg>
  )
}

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

  // Token usage for the most recent completion — not every provider
  // reports it (see llm.Usage's doc comment), so messages without it are
  // skipped rather than showing a stale count from an earlier turn.
  const lastUsage = [...messages].reverse().find((m) => m.usage)?.usage

  // Running total across every turn in this conversation so far. Summed
  // from each message's own usage rather than tracked as separate state —
  // usage isn't persisted to the DB (same as reasoning/toolCalls), so this
  // naturally covers only what's loaded into `messages` for the current
  // session, same scope as lastUsage above.
  const totalUsage = messages.reduce(
    (acc, m) => (m.usage ? { promptTokens: acc.promptTokens + m.usage.prompt_tokens, completionTokens: acc.completionTokens + m.usage.completion_tokens } : acc),
    { promptTokens: 0, completionTokens: 0 }
  )
  const hasTotalUsage = totalUsage.promptTokens > 0 || totalUsage.completionTokens > 0

  // Rate-limit headroom, if the backend reported one (see llm.RateLimit's
  // doc comment — only confirmed for OpenAI and Anthropic; other
  // providers simply never set rate_limit, and this stays null for them).
  // Tokens are the usual binding constraint, so this prefers the
  // token-remaining percentage over the request-count one; falls back to
  // Anthropic's separate input-token figure when the combined "tokens"
  // pair isn't present, and picks the lower of input/output percentages
  // since either one hitting zero blocks the next request.
  const rl = lastUsage?.rate_limit
  let rateLimitPct = null
  let rateLimitTitle = ''
  if (rl) {
    const pct = (remaining, limit) => (limit > 0 ? Math.round((remaining / limit) * 100) : null)
    if (rl.tokens_limit > 0) {
      rateLimitPct = pct(rl.tokens_remaining, rl.tokens_limit)
      rateLimitTitle = `${rl.tokens_remaining.toLocaleString()} / ${rl.tokens_limit.toLocaleString()} tokens remaining this window`
    } else if (rl.input_tokens_limit > 0 || rl.output_tokens_limit > 0) {
      const inPct = pct(rl.input_tokens_remaining, rl.input_tokens_limit)
      const outPct = pct(rl.output_tokens_remaining, rl.output_tokens_limit)
      rateLimitPct = [inPct, outPct].filter((v) => v != null).sort((a, b) => a - b)[0] ?? null
      rateLimitTitle = `${rl.input_tokens_remaining.toLocaleString()} / ${rl.input_tokens_limit.toLocaleString()} input tokens, ${rl.output_tokens_remaining.toLocaleString()} / ${rl.output_tokens_limit.toLocaleString()} output tokens remaining this window`
    } else if (rl.requests_limit > 0) {
      rateLimitPct = pct(rl.requests_remaining, rl.requests_limit)
      rateLimitTitle = `${rl.requests_remaining.toLocaleString()} / ${rl.requests_limit.toLocaleString()} requests remaining this window`
    }
  }

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
                    <FileIcon className="inline-icon" /> Read <code>{call.path}</code>
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
                      <summary>
                        {check.passed ? <CheckCircleIcon className="inline-icon" /> : <XCircleIcon className="inline-icon" />}
                        {check.passed ? 'Build check passed' : 'Build check failed'}
                      </summary>
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
            {visionSupported ? <ImageIcon className="inline-icon" /> : <ImageOffIcon className="inline-icon" />}
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
              <StopIcon className="inline-icon" /> Stop
            </button>
          ) : (
            <button type="submit" className="btn-primary" disabled={!input.trim() && (!pendingImages || pendingImages.length === 0)}>
              Send
            </button>
          )}
        </div>
        {activeEditorFile && (
          <p className="composer-active-file" title={activeEditorFile}>
            <FileIcon className="inline-icon" />
            {activeEditorFile.split('/').pop()} is available to the model
          </p>
        )}
        {lastUsage && (
          <div className="composer-usage" title={`${lastUsage.total_tokens.toLocaleString()} tokens this turn`}>
            <span className="composer-usage-chip composer-usage-in">
              <ArrowDownIcon className="inline-icon" />
              {lastUsage.prompt_tokens.toLocaleString()}
            </span>
            <span className="composer-usage-chip composer-usage-out">
              <ArrowUpIcon className="inline-icon" />
              {lastUsage.completion_tokens.toLocaleString()}
            </span>
            {rateLimitPct != null && (
              <span
                className={`composer-usage-chip composer-usage-limit ${rateLimitPct <= 10 ? 'is-low' : ''}`}
                title={rateLimitTitle}
              >
                <GaugeIcon className="inline-icon" />
                {rateLimitPct}% left
              </span>
            )}
            {hasTotalUsage && (
              <span className="composer-usage-total">
                session {totalUsage.promptTokens.toLocaleString()} in · {totalUsage.completionTokens.toLocaleString()} out
              </span>
            )}
          </div>
        )}
      </form>
    </main>
  )
}
