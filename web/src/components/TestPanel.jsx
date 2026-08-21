import { useState } from 'react'
import { runEditorTests } from '../api'

// The Test sub-panel of the editor sidebar: a "Run Tests" button and the
// pass/fail result of the last run (see internal/chat.EditorRunTests —
// "go test ./..." against the real, already-saved project files). Go
// only, matching internal/buildcheck's current scope; a project with no
// go.mod gets a clear error instead of a generic failure.
export default function TestPanel() {
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState(null) // { passed, output } | null
  const [error, setError] = useState('')

  async function handleRun() {
    setRunning(true)
    setError('')
    setResult(null)
    try {
      const res = await runEditorTests()
      if (!res.ok) {
        const body = await res.text().catch(() => res.statusText)
        throw new Error(body || `request failed with ${res.status}`)
      }
      const data = await res.json()
      setResult(data)
    } catch (err) {
      setError(err.message)
    } finally {
      setRunning(false)
    }
  }

  return (
    <div className="editor-panel-body editor-test-panel">
      <button type="button" className="editor-test-run" onClick={handleRun} disabled={running}>
        {running ? 'Running tests…' : 'Run Tests'}
      </button>
      {error && <p className="editor-error">{error}</p>}
      {result && (
        <div className={`editor-test-result ${result.passed ? 'is-ok' : 'is-error'}`}>
          <div className="editor-test-result-status">{result.passed ? 'Tests passed' : 'Tests failed'}</div>
          <pre className="editor-test-output">{result.output || '(no output)'}</pre>
        </div>
      )}
      {!running && !result && !error && <p className="editor-hint">Runs "go test ./..." against your saved project files.</p>}
    </div>
  )
}
