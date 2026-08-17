import { useMemo, useState } from 'react'

// Builds a nested {dirs, files} tree from the flat list of relative
// paths the backend returns (see EditorTree/walkFiles in
// internal/chat/editor.go) — the API only ever hands back a flat file
// list, so the folder structure is inferred client-side by splitting on
// '/'. Folders are sorted before files, both alphabetically, matching
// VS Code's default explorer ordering.
function buildTree(paths) {
  const root = { dirs: new Map(), files: [] }
  for (const path of paths) {
    const parts = path.split('/')
    let node = root
    for (let i = 0; i < parts.length - 1; i++) {
      const name = parts[i]
      if (!node.dirs.has(name)) {
        node.dirs.set(name, { dirs: new Map(), files: [] })
      }
      node = node.dirs.get(name)
    }
    node.files.push({ name: parts[parts.length - 1], path })
  }
  return root
}

const CHEVRON = (
  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M9 6l6 6-6 6" />
  </svg>
)

const FOLDER_ICON = (open) => (
  <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    {open ? (
      <path d="M3 7.5A1.5 1.5 0 0 1 4.5 6h4l1.7 2H19.5A1.5 1.5 0 0 1 21 9.5v8A1.5 1.5 0 0 1 19.5 19h-15A1.5 1.5 0 0 1 3 17.5v-10Z" />
    ) : (
      <path d="M3 6.5A1.5 1.5 0 0 1 4.5 5h4l1.7 2H19.5A1.5 1.5 0 0 1 21 8.5v9A1.5 1.5 0 0 1 19.5 19h-15A1.5 1.5 0 0 1 3 17.5v-11Z" />
    )}
  </svg>
)

const FILE_ICON = (
  <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M6.5 3.5h7L18.5 8.5v12a1 1 0 0 1-1 1h-11a1 1 0 0 1-1-1v-16a1 1 0 0 1 1-1Z" />
    <path d="M13.5 3.5V8.5h5" />
  </svg>
)

function DirRow({ name, node, depth, openPath, expanded, onToggle, onOpenFile }) {
  const dirPath = node.__path
  const isOpen = expanded.has(dirPath)
  return (
    <>
      <li>
        <button
          type="button"
          className="tree-row tree-dir"
          style={{ paddingLeft: 8 + depth * 14 }}
          onClick={() => onToggle(dirPath)}
          title={dirPath}
        >
          <span className={`tree-chevron ${isOpen ? 'is-open' : ''}`}>{CHEVRON}</span>
          <span className="tree-icon">{FOLDER_ICON(isOpen)}</span>
          <span className="tree-label">{name}</span>
        </button>
      </li>
      {isOpen && (
        <TreeChildren
          node={node}
          depth={depth + 1}
          openPath={openPath}
          expanded={expanded}
          onToggle={onToggle}
          onOpenFile={onOpenFile}
        />
      )}
    </>
  )
}

function TreeChildren({ node, depth, openPath, expanded, onToggle, onOpenFile }) {
  const dirNames = [...node.dirs.keys()].sort((a, b) => a.localeCompare(b))
  const files = [...node.files].sort((a, b) => a.name.localeCompare(b.name))
  return (
    <>
      {dirNames.map((name) => (
        <DirRow
          key={node.dirs.get(name).__path}
          name={name}
          node={node.dirs.get(name)}
          depth={depth}
          openPath={openPath}
          expanded={expanded}
          onToggle={onToggle}
          onOpenFile={onOpenFile}
        />
      ))}
      {files.map((file) => (
        <li key={file.path}>
          <button
            type="button"
            className={`tree-row tree-file ${openPath === file.path ? 'is-active' : ''}`}
            style={{ paddingLeft: 8 + depth * 14 + 18 }}
            onClick={() => onOpenFile(file.path)}
            title={file.path}
          >
            <span className="tree-icon">{FILE_ICON}</span>
            <span className="tree-label">{file.name}</span>
          </button>
        </li>
      ))}
    </>
  )
}

// Stamps each directory node with its own full path (as __path) so
// DirRow/TreeChildren can key and track expansion by path without
// threading an accumulator through every recursive call.
function stampPaths(node, prefix) {
  for (const [name, child] of node.dirs) {
    child.__path = prefix ? `${prefix}/${name}` : name
    stampPaths(child, child.__path)
  }
}

// Renders paths as a VS Code-style explorer tree: folders before files,
// both alphabetical, with chevron expand/collapse and indentation by
// depth. Expansion state lives here (keyed by folder path) rather than
// in the parent, since it's pure presentation state the rest of
// EditorView doesn't need to know about.
export default function FileTree({ paths, openPath, onOpenFile }) {
  const [expanded, setExpanded] = useState(() => new Set())

  const root = useMemo(() => {
    const tree = buildTree(paths)
    stampPaths(tree, '')
    return tree
  }, [paths])

  function toggle(path) {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  return (
    <ul className="editor-file-list tree-list">
      <TreeChildren node={root} depth={0} openPath={openPath} expanded={expanded} onToggle={toggle} onOpenFile={onOpenFile} />
    </ul>
  )
}
