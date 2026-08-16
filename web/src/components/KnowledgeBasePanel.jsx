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

      {docStatus && <p className="status">{docStatus}</p>}

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
