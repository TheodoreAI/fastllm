// The Search sub-panel of the editor sidebar: cross-file text search and
// its results list. State (query/results) stays in EditorView since
// nothing here needs it to persist across a remount — but pulled out for
// the same readability reasons as GitPanel.
export default function SearchPanel({ query, onQueryChange, onSubmit, searching, searchResults, onOpenFile }) {
  return (
    <div className="editor-panel-body">
      <form onSubmit={onSubmit} className="editor-search-form">
        <input
          value={query}
          onChange={(e) => onQueryChange(e.target.value)}
          placeholder="Search all files…"
        />
        <button type="submit" disabled={searching || !query.trim()}>
          {searching ? 'Searching…' : 'Go'}
        </button>
      </form>
      <ul className="editor-search-results">
        {searchResults.map((m, i) => (
          <li key={i}>
            <button type="button" onClick={() => onOpenFile(m.path)}>
              <span className="editor-search-path">{m.path}:{m.line}</span>
              <span className="editor-search-text">{m.text.trim()}</span>
            </button>
          </li>
        ))}
        {searchResults.length === 0 && query && !searching && (
          <li className="editor-hint">No matches.</li>
        )}
      </ul>
    </div>
  )
}
