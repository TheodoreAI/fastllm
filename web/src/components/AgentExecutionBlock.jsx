import { useState } from 'react'
import MessageContent from './MessageContent'

function CheckIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <polyline points="20 6 9 17 4 12" />
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

function ChevronRightIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <polyline points="9 18 15 12 9 6" />
    </svg>
  )
}

function ChevronDownIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <polyline points="6 9 12 15 18 9" />
    </svg>
  )
}

export default function AgentExecutionBlock({ turns = [], currentTurn = null, finalResult = null, error = null }) {
  const [open, setOpen] = useState(true)

  const allTurns = [...turns]
  if (currentTurn) {
    allTurns.push({ ...currentTurn, active: true })
  }

  const isExecuting = !finalResult && !error && (currentTurn != null || turns.length > 0)
  const totalToolCalls = turns.reduce((acc, t) => acc + (t.toolCalls?.length || 0), 0) + (currentTurn?.toolCalls?.length || 0)
  const totalCost = finalResult?.metrics?.total_cost || turns.reduce((acc, t) => acc + (t.metrics?.estimated_cost || 0), 0)
  const durationSec = finalResult ? (finalResult.duration_ms / 1000).toFixed(1) : null

  return (
    <div className={`agent-exec-card ${isExecuting && !finalResult ? 'is-running' : finalResult?.success ? 'is-success' : error ? 'is-error' : ''}`}>
      <div className="agent-exec-header" onClick={() => setOpen(!open)}>
        <div className="agent-exec-status">
          {isExecuting && !finalResult ? (
            <span className="agent-status-running">
              <span className="agent-spinner" /> Thinking & Executing…
            </span>
          ) : finalResult?.success ? (
            <span className="agent-status-success">
              <CheckIcon className="inline-icon" /> Task Completed
            </span>
          ) : error || (finalResult && !finalResult.success) ? (
            <span className="agent-status-fail">
              <XCircleIcon className="inline-icon" /> Task Failed
            </span>
          ) : (
            <span className="agent-status-done">Agent Execution</span>
          )}
        </div>

        <div className="agent-exec-summary">
          <span>{allTurns.length} turn{allTurns.length === 1 ? '' : 's'}</span>
          {totalToolCalls > 0 && <span> · {totalToolCalls} tool call{totalToolCalls === 1 ? '' : 's'}</span>}
          {durationSec && <span> · {durationSec}s</span>}
          {totalCost > 0 && <span> · ${totalCost.toFixed(4)}</span>}
        </div>

        <span className="agent-collapse-arrow">
          {open ? <ChevronDownIcon className="inline-icon" /> : <ChevronRightIcon className="inline-icon" />}
        </span>
      </div>

      {open && (
        <div className="agent-exec-body">
          {allTurns.map((t, idx) => (
            <TurnCard key={t.turn || idx} turn={t} active={t.active} />
          ))}

          {error && (
            <div className="agent-exec-error-banner">
              <strong>Error:</strong> {error}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function TurnCard({ turn, active = false }) {
  const [collapsed, setCollapsed] = useState(false)

  return (
    <div className={`harness-turn-card ${active ? 'is-active' : ''}`}>
      <div className="harness-turn-header" onClick={() => setCollapsed(!collapsed)}>
        <span className="harness-turn-badge">Turn {turn.turn}</span>
        {active && <span className="harness-turn-running">Thinking & Executing…</span>}
        {turn.metrics && (
          <span className="harness-turn-metrics">
            {turn.metrics.prompt_tokens} in / {turn.metrics.completion_tokens} out ·{' '}
            {turn.metrics.tokens_per_second?.toFixed(1)} tok/s ·{' '}
            {(turn.metrics.duration / 1e9).toFixed(2)}s
            {turn.metrics.estimated_cost > 0 && ` · $${turn.metrics.estimated_cost.toFixed(4)}`}
          </span>
        )}
        <span className="harness-collapse-indicator">
          {collapsed ? <ChevronRightIcon className="inline-icon" /> : <ChevronDownIcon className="inline-icon" />}
        </span>
      </div>

      {!collapsed && (
        <div className="harness-turn-content">
          {turn.response && !turn.toolCalls?.length && (
            <div className="harness-turn-response">
              <MessageContent content={turn.response} />
            </div>
          )}

          {turn.toolCalls && turn.toolCalls.length > 0 && (
            <div className="harness-tool-calls">
              {turn.toolCalls.map((tc, idx) => (
                <ToolCallItem key={tc.id || idx} toolCall={tc} />
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function ToolCallItem({ toolCall }) {
  const [open, setOpen] = useState(false)

  let summary = ''
  try {
    const args = JSON.parse(toolCall.arguments || '{}')
    if (args.path) summary += args.path
    if (args.command) summary += args.command
    if (args.pattern) summary += `pattern: ${args.pattern}`
  } catch {
    summary = toolCall.arguments || ''
  }

  return (
    <div className="harness-tool-item">
      <div className="harness-tool-line" onClick={() => setOpen(!open)}>
        <span className="harness-tool-name">{toolCall.name}</span>
        <span className="harness-tool-args">{summary}</span>
        {toolCall.pending ? (
          <span className="harness-tool-spin">executing…</span>
        ) : (
          <span className="harness-tool-done">
            <CheckIcon className="inline-icon" />
          </span>
        )}
      </div>

      {open && (
        <div className="harness-tool-details">
          {toolCall.arguments && (
            <div className="harness-tool-block">
              <span className="harness-subhead">Arguments:</span>
              <pre>{toolCall.arguments}</pre>
            </div>
          )}
          {toolCall.result && (
            <div className="harness-tool-block">
              <span className="harness-subhead">Result:</span>
              <pre>{toolCall.result}</pre>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
