// Simple LCS-based line diff — good enough for reviewing a model-proposed
// file write, no need for a full diff library for this use case.
function diffLines(oldText, newText) {
  const a = oldText.split('\n')
  const b = newText.split('\n')
  const m = a.length
  const n = b.length
  const lcs = Array.from({ length: m + 1 }, () => new Array(n + 1).fill(0))
  for (let i = m - 1; i >= 0; i--) {
    for (let j = n - 1; j >= 0; j--) {
      lcs[i][j] = a[i] === b[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1])
    }
  }
  const rows = []
  let i = 0
  let j = 0
  while (i < m && j < n) {
    if (a[i] === b[j]) {
      rows.push({ type: 'ctx', text: a[i] })
      i++
      j++
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) {
      rows.push({ type: 'del', text: a[i] })
      i++
    } else {
      rows.push({ type: 'add', text: b[j] })
      j++
    }
  }
  while (i < m) rows.push({ type: 'del', text: a[i++] })
  while (j < n) rows.push({ type: 'add', text: b[j++] })
  return rows
}

const STATUS_LABEL = {
  applying: 'Working…',
  approved: 'Written to disk',
  rejected: 'Discarded',
  error: 'Failed',
}

export default function PendingWriteCard({ write, onRequestApprove, onReject }) {
  const rows = diffLines(write.existing_content || '', write.new_content || '')

  return (
    <div className={`pending-write status-${write.status}`}>
      <div className="pending-write-header">
        <span>✏️ {write.file_exists ? 'Edit' : 'Create'} <code>{write.path}</code></span>
        {write.status !== 'pending' && write.status !== 'applying' && (
          <span className="pending-write-status">{STATUS_LABEL[write.status]}</span>
        )}
      </div>
      <div className="pending-write-diff">
        {rows.map((row, i) => (
          <div key={i} className={`diff-row diff-${row.type}`}>
            <span className="diff-marker">{row.type === 'add' ? '+' : row.type === 'del' ? '−' : ' '}</span>
            <span className="diff-text">{row.text || ' '}</span>
          </div>
        ))}
      </div>
      {write.status === 'pending' && (
        <div className="pending-write-actions">
          <button type="button" className="btn-secondary" onClick={() => onReject(write.id)}>
            Reject
          </button>
          <button type="button" className="btn-primary" onClick={() => onRequestApprove(write)}>
            Approve &amp; write
          </button>
        </div>
      )}
      {write.status === 'error' && <p className="status status-error">{write.error}</p>}
    </div>
  )
}
