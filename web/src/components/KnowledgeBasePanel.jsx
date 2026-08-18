import { useMemo, useState } from 'react'

const CHEVRON = (
  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M9 6l6 6-6 6" />
  </svg>
)

const FOLDER_ICON = (
  <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M3 6.5A1.5 1.5 0 0 1 4.5 5h4l1.7 2H19.5A1.5 1.5 0 0 1 21 8.5v9A1.5 1.5 0 0 1 19.5 19h-15A1.5 1.5 0 0 1 3 17.5v-11Z" />
  </svg>
)

// Groups documents by their immediate parent directory (everything but
// the last '/'-separated segment of filename) so a whole-project folder
// upload collapses into per-folder rows instead of a flat pile of
// same-looking basenames — mirrors FileTree.jsx's chevron/folder-icon
// pattern, but one level deep rather than a full recursive tree, since
// documents don't need nested browsing, just "get this folder's noise
// out of the way." Files with no '/' in their name (pasted text, a flat
// file-picker upload) go in `root` and render ungrouped above the
// folders, same as FileTree treats root-level files.
function groupDocuments(documents) {
  const groups = new Map()
  const root = []
  for (const doc of documents) {
    const idx = doc.filename.lastIndexOf('/')
    if (idx === -1) {
      root.push(doc)
      continue
    }
    const dir = doc.filename.slice(0, idx)
    if (!groups.has(dir)) groups.set(dir, [])
    groups.get(dir).push(doc)
  }
  return { root, groups }
}

function DocRow({ doc, label }) {
  return (
    <li className="doc-item">
      <span className="doc-name" title={doc.filename}>
        {label}
      </span>
      <span className="doc-count">
        {doc.chunk_count} chunk{doc.chunk_count === 1 ? '' : 's'}
      </span>
    </li>
  )
}

function DocGroup({ dir, docs, expanded, onToggle }) {
  const isOpen = expanded.has(dir)
  return (
    <li className="doc-group">
      <div className="tree-row tree-dir doc-group-header" onClick={() => onToggle(dir)} title={dir}>
        <span className={`tree-chevron ${isOpen ? 'is-open' : ''}`}>{CHEVRON}</span>
        <span className="tree-icon">{FOLDER_ICON}</span>
        <span className="tree-label">{dir}</span>
        <span className="doc-count">{docs.length} file{docs.length === 1 ? '' : 's'}</span>
      </div>
      {isOpen && (
        <ul className="doc-group-list">
          {docs.map((d) => (
            <DocRow key={d.id} doc={d} label={d.filename.slice(dir.length + 1)} />
          ))}
        </ul>
      )}
    </li>
  )
}

export default function KnowledgeBasePanel({
  docText,
  onDocTextChange,
  onUploadDocument,
  fileInputRef,
  onFilePicked,
  folderInputRef,
  onFolderPicked,
  docStatus,
  documents,
  uploadProgress,
  uploadErrors,
  onDismissUploadErrors,
}) {
  const [expanded, setExpanded] = useState(() => new Set())

  const { root, groups } = useMemo(() => groupDocuments(documents), [documents])
  const sortedDirs = useMemo(() => [...groups.keys()].sort(), [groups])

  function toggleGroup(dir) {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(dir)) next.delete(dir)
      else next.add(dir)
      return next
    })
  }

  return (
    <section className="panel">
      <h2>Knowledge base</h2>
      <form onSubmit={onUploadDocument}>
        <textarea
          placeholder="Paste text to index for retrieval…"
          value={docText}
          onChange={(e) => onDocTextChange(e.target.value)}
          rows={6}
        />
        <button type="submit" className="btn-primary">Index text</button>
      </form>

      <input
        ref={fileInputRef}
        type="file"
        accept=".txt,.md,.markdown,.mdx,.json,.yaml,.yml,.csv,.tsv,.log,.go,.js,.jsx,.ts,.tsx,.py,.rb,.java,.c,.cc,.cpp,.h,.hpp,.rs,.sh,.sql,.html,.css,.xml,.pdf"
        multiple
        hidden
        onChange={onFilePicked}
      />
      <button type="button" className="btn-secondary" onClick={() => fileInputRef.current?.click()}>
        Upload files…
      </button>

      <input
        ref={folderInputRef}
        type="file"
        hidden
        webkitdirectory=""
        directory=""
        onChange={onFolderPicked}
      />
      <button type="button" className="btn-secondary" onClick={() => folderInputRef.current?.click()}>
        Upload folder…
      </button>

      {uploadProgress && (
        <div className="upload-progress">
          <div className="upload-progress-bar">
            <div
              className="upload-progress-fill"
              style={{ width: `${Math.round((uploadProgress.completed / uploadProgress.total) * 100)}%` }}
            />
          </div>
          <p className="upload-progress-label">
            {uploadProgress.completed} / {uploadProgress.total} — {uploadProgress.label}
          </p>
        </div>
      )}

      {docStatus && <p className="status">{docStatus}</p>}

      {uploadErrors.length > 0 && (
        <div className="upload-errors">
          <div className="upload-errors-header">
            <span>{uploadErrors.length} file{uploadErrors.length === 1 ? '' : 's'} failed to index</span>
            <button type="button" className="upload-errors-dismiss" onClick={onDismissUploadErrors}>
              Dismiss
            </button>
          </div>
          <ul className="upload-errors-list">
            {uploadErrors.map((e, i) => (
              <li key={i}>
                <span className="upload-error-label">{e.label}</span>: {e.message}
              </li>
            ))}
          </ul>
        </div>
      )}

      <ul className="doc-list">
        {documents.length === 0 && <li className="doc-empty">Nothing indexed yet</li>}
        {root.map((d) => (
          <DocRow key={d.id} doc={d} label={d.filename} />
        ))}
        {sortedDirs.map((dir) => (
          <DocGroup key={dir} dir={dir} docs={groups.get(dir)} expanded={expanded} onToggle={toggleGroup} />
        ))}
      </ul>
    </section>
  )
}
