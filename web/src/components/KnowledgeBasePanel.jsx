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
        {documents.map((d) => (
          <li key={d.id} className="doc-item">
            <span className="doc-name">{d.filename}</span>
            <span className="doc-count">{d.chunk_count} chunk{d.chunk_count === 1 ? '' : 's'}</span>
          </li>
        ))}
      </ul>
    </section>
  )
}
