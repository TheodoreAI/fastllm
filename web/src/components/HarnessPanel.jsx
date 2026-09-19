import { useEffect, useRef, useState } from 'react'
import MessageContent from './MessageContent'
import { streamHarnessRun } from '../api'

function PlayIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true" {...props}>
      <polygon points="6 3 20 12 6 21 6 3" />
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

function CheckIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <polyline points="20 6 9 17 4 12" />
    </svg>
  )
}

export default function HarnessPanel({
  models,
  selectedModel,
  onSelectModel,
  defaultWorkingDir = '',
}) {
  const [task, setTask] = useState('')
  const [workingDir, setWorkingDir] = useState(defaultWorkingDir)
  const [maxTurns, setMaxTurns] = useState(20)
  const [allowCommands, setAllowCommands] = useState(true)
  const [isRunning, setIsRunning] = useState(false)
  const [turns, setTurns] = useState([])
  const [currentTurn, setCurrentTurn] = useState(null)
  const [finalResult, setFinalResult] = useState(null)
  const [runError, setRunError] = useState(null)

  const abortControllerRef = useRef(null)
  const bottomRef = useRef(null)

  useEffect(() => {
    if (defaultWorkingDir && !workingDir) {
      setWorkingDir(defaultWorkingDir)
    }
  }, [defaultWorkingDir, workingDir])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [turns, currentTurn, finalResult])

  function handleStart() {
    if (!task.trim() || isRunning) return

    setIsRunning(true)
    setTurns([])
    setCurrentTurn(null)
    setFinalResult(null)
    setRunError(null)

    const controller = new AbortController()
    abortControllerRef.current = controller

    const payload = {
      task: task.trim(),
      working_dir: workingDir.trim() || undefined,
      model: selectedModel || undefined,
      max_turns: Number(maxTurns) || 20,
      allow_commands: allowCommands,
    }

    let activeTurn = { turn: 1, toolCalls: [] }

    streamHarnessRun(
      payload,
      (ev) => {
        if (ev.type === 'turn_start') {
          activeTurn = { turn: ev.turn, toolCalls: [] }
          setCurrentTurn({ ...activeTurn })
        } else if (ev.type === 'tool_call') {
          if (ev.tool_call) {
            activeTurn.toolCalls = [...activeTurn.toolCalls, { ...ev.tool_call, pending: true }]
            setCurrentTurn({ ...activeTurn })
          }
        } else if (ev.type === 'tool_result') {
          if (ev.tool_call) {
            activeTurn.toolCalls = activeTurn.toolCalls.map((tc) =>
              tc.id === ev.tool_call.id ? { ...ev.tool_call, pending: false } : tc
            )
            setCurrentTurn({ ...activeTurn })
          }
        } else if (ev.type === 'turn_complete') {
          if (ev.metrics) {
            activeTurn.metrics = ev.metrics
          }
          if (ev.response) {
            activeTurn.response = ev.response
          }
          setTurns((prev) => [...prev, { ...activeTurn }])
          setCurrentTurn(null)
        } else if (ev.type === 'task_finished') {
          setFinalResult(ev.result)
          if (ev.error) {
            setRunError(ev.error)
          }
          setIsRunning(false)
        }
      },
      (err) => {
        setRunError(err.message)
        setIsRunning(false)
      },
      controller.signal
    ).finally(() => {
      setIsRunning(false)
    })
  }

  function handleStop() {
    abortControllerRef.current?.abort()
    setIsRunning(false)
  }

  return (
    <div className="harness-panel">
      <div className="harness-header">
        <div className="harness-config-row">
          <div className="harness-field">
            <label htmlFor="harness-workdir">Working Directory</label>
            <input
              id="harness-workdir"
              type="text"
              placeholder="e.g. C:\Users\mateo\project (or empty for root)"
              value={workingDir}
              onChange={(e) => setWorkingDir(e.target.value)}
              disabled={isRunning}
            />
          </div>

          <div className="harness-field">
            <label htmlFor="harness-model">Model</label>
            <select
              id="harness-model"
              value={selectedModel}
              onChange={(e) => onSelectModel?.(e.target.value)}
              disabled={isRunning}
            >
              {models.map((m) => (
                <option key={m.id || m.name} value={m.name || m.id}>
                  {m.name || m.id}
                </option>
              ))}
            </select>
          </div>

          <div className="harness-field-sm">
            <label htmlFor="harness-turns">Max Turns</label>
            <input
              id="harness-turns"
              type="number"
              min="1"
              max="100"
              value={maxTurns}
              onChange={(e) => setMaxTurns(e.target.value)}
              disabled={isRunning}
            />
          </div>

          <div className="harness-field-checkbox">
            <label>
              <input
                type="checkbox"
                checked={allowCommands}
                onChange={(e) => setAllowCommands(e.target.checked)}
                disabled={isRunning}
              />
              Allow Shell Commands
            </label>
          </div>
        </div>

        <div className="harness-composer">
          <textarea
            className="harness-task-input"
            rows="3"
            placeholder="Describe an autonomous task (e.g. 'Inspect server.go, add an endpoint for /status, and test it')..."
            value={task}
            onChange={(e) => setTask(e.target.value)}
            disabled={isRunning}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
                e.preventDefault()
                handleStart()
              }
            }}
          />
          <div className="harness-actions">
            {!isRunning ? (
              <button
                type="button"
                className="harness-run-btn"
                onClick={handleStart}
                disabled={!task.trim()}
              >
                <PlayIcon className="inline-icon" /> Run Agent Task
              </button>
            ) : (
              <button
                type="button"
                className="harness-stop-btn"
                onClick={handleStop}
              >
                <StopIcon className="inline-icon" /> Stop Execution
              </button>
            )}
          </div>
        </div>
      </div>

      <div className="harness-feed">
        {turns.length === 0 && !currentTurn && !isRunning && !finalResult && (
          <div className="harness-empty-state">
            <h3>Autonomous Coding Agent</h3>
            <p>
              Give the agent a goal or coding instruction. It will inspect files, make targeted edits,
              patch diffs, run verification tests, and report results automatically.
            </p>
          </div>
        )}

        {turns.map((t) => (
          <TurnCard key={t.turn} turn={t} />
        ))}

        {currentTurn && (
          <TurnCard turn={currentTurn} active />
        )}

        {finalResult && (
          <div className={`harness-final-result ${finalResult.success ? 'is-success' : 'is-failure'}`}>
            <div className="harness-final-header">
              <span className="harness-status-pill">
                {finalResult.success ? '✓ Task Complete' : '✕ Task Failed'}
              </span>
              <span className="harness-final-meta">
                {finalResult.turns} turns · {(finalResult.duration_ms / 1000).toFixed(2)}s
                {finalResult.metrics?.total_cost > 0 && ` · $${finalResult.metrics.total_cost.toFixed(4)}`}
              </span>
            </div>
            {finalResult.final_response && (
              <div className="harness-final-body">
                <MessageContent content={finalResult.final_response} />
              </div>
            )}
            {finalResult.error && (
              <div className="harness-error-banner">
                <strong>Error:</strong> {finalResult.error}
              </div>
            )}
          </div>
        )}

        {runError && !finalResult && (
          <div className="harness-error-banner">
            <strong>Execution Error:</strong> {runError}
          </div>
        )}

        <div ref={bottomRef} />
      </div>
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
        <span className="harness-collapse-indicator">{collapsed ? '▶' : '▼'}</span>
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
          <span className="harness-tool-done">✓</span>
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
