import { useEffect, useMemo, useRef, useState } from 'react'

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

const RENAME_ICON = (
  <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7.5 18.5 3 20l1.5-4.5Z" />
  </svg>
)

const DELETE_ICON = (
  <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M5 7h14" />
    <path d="M9 7V5a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2" />
    <path d="M7 7l1 13a1 1 0 0 0 1 1h6a1 1 0 0 0 1-1l1-13" />
  </svg>
)

const NEW_FILE_ICON = (
  <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M6.5 3.5h7L18.5 8.5v12a1 1 0 0 1-1 1h-11a1 1 0 0 1-1-1v-16a1 1 0 0 1 1-1Z" />
    <path d="M13.5 3.5V8.5h5" />
    <path d="M12 12v5M9.5 14.5h5" />
  </svg>
)

// RowActions renders the small rename/delete icon buttons revealed on
// row hover — stopPropagation keeps a click from also triggering the
// row's own onClick (open file / toggle folder).
function RowActions({ onRename, onDelete }) {
  return (
    <span className="tree-row-actions">
      <button
        type="button"
        className="tree-row-action"
        title="Rename"
        onClick={(e) => {
          e.stopPropagation()
          onRename()
        }}
      >
        {RENAME_ICON}
      </button>
      {onDelete && (
        <button
          type="button"
          className="tree-row-action tree-row-action-danger"
          title="Delete"
          onClick={(e) => {
            e.stopPropagation()
            onDelete()
          }}
        >
          {DELETE_ICON}
        </button>
      )}
    </span>
  )
}

// RenameInput replaces a row's label with an editable text field —
// shared by both file and directory rows so Enter/Escape/blur behave
// identically everywhere renaming can happen.
function RenameInput({ initialValue, onSubmit, onCancel }) {
  const [value, setValue] = useState(initialValue)
  const ref = useRef(null)

  useEffect(() => {
    ref.current?.focus()
    ref.current?.select()
  }, [])

  return (
    <input
      ref={ref}
      className="tree-rename-input"
      value={value}
      onClick={(e) => e.stopPropagation()}
      onChange={(e) => setValue(e.target.value)}
      onKeyDown={(e) => {
        if (e.key === 'Enter') {
          e.preventDefault()
          const trimmed = value.trim()
          if (trimmed && trimmed !== initialValue) onSubmit(trimmed)
          else onCancel()
        } else if (e.key === 'Escape') {
          onCancel()
        }
      }}
      onBlur={onCancel}
    />
  )
}

function DirRow({ name, node, depth, openPath, expanded, onToggle, onOpenFile, canWrite, renamingPath, onRequestRename, onSubmitRename, onCancelRename, onContextMenu }) {
  const dirPath = node.__path
  const isOpen = expanded.has(dirPath)
  const isRenaming = renamingPath === dirPath
  return (
    <>
      <li>
        <div
          className="tree-row tree-dir"
          style={{ paddingLeft: 8 + depth * 14 }}
          onClick={() => !isRenaming && onToggle(dirPath)}
          onContextMenu={(e) => onContextMenu(e, { type: 'dir', path: dirPath, name })}
          title={dirPath}
        >
          <span className={`tree-chevron ${isOpen ? 'is-open' : ''}`}>{CHEVRON}</span>
          <span className="tree-icon">{FOLDER_ICON(isOpen)}</span>
          {isRenaming ? (
            <RenameInput initialValue={name} onSubmit={(next) => onSubmitRename(dirPath, next)} onCancel={onCancelRename} />
          ) : (
            <>
              <span className="tree-label">{name}</span>
              {canWrite && <RowActions onRename={() => onRequestRename(dirPath)} />}
            </>
          )}
        </div>
      </li>
      {isOpen && (
        <TreeChildren
          node={node}
          depth={depth + 1}
          openPath={openPath}
          expanded={expanded}
          onToggle={onToggle}
          onOpenFile={onOpenFile}
          canWrite={canWrite}
          renamingPath={renamingPath}
          onRequestRename={onRequestRename}
          onSubmitRename={onSubmitRename}
          onCancelRename={onCancelRename}
          onContextMenu={onContextMenu}
        />
      )}
    </>
  )
}

function TreeChildren({ node, depth, openPath, expanded, onToggle, onOpenFile, canWrite, renamingPath, onRequestRename, onSubmitRename, onCancelRename, onContextMenu, onDeleteFile }) {
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
          canWrite={canWrite}
          renamingPath={renamingPath}
          onRequestRename={onRequestRename}
          onSubmitRename={onSubmitRename}
          onCancelRename={onCancelRename}
          onContextMenu={onContextMenu}
        />
      ))}
      {files.map((file) => {
        const isRenaming = renamingPath === file.path
        return (
          <li key={file.path}>
            <div
              className={`tree-row tree-file ${openPath === file.path ? 'is-active' : ''}`}
              style={{ paddingLeft: 8 + depth * 14 + 18 }}
              onClick={() => !isRenaming && onOpenFile(file.path)}
              onContextMenu={(e) => onContextMenu(e, { type: 'file', path: file.path, name: file.name })}
              title={file.path}
            >
              <span className="tree-icon">{FILE_ICON}</span>
              {isRenaming ? (
                <RenameInput initialValue={file.name} onSubmit={(next) => onSubmitRename(file.path, next)} onCancel={onCancelRename} />
              ) : (
                <>
                  <span className="tree-label">{file.name}</span>
                  {canWrite && (
                    <RowActions onRename={() => onRequestRename(file.path)} onDelete={() => onDeleteFile(file.path)} />
                  )}
                </>
              )}
            </div>
          </li>
        )
      })}
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
// EditorView doesn't need to know about. Right-click opens a context
// menu (New File/New Folder/Rename/Delete); rename and create-file are
// resolved here, delete and the actual create are delegated up since
// they need confirmation UI / API calls that live in EditorView.
export default function FileTree({ paths, openPath, onOpenFile, canWrite, onCreateFile, onDeleteFile, onRenameFile }) {
  const [expanded, setExpanded] = useState(() => new Set())
  const [renamingPath, setRenamingPath] = useState(null)
  const [menu, setMenu] = useState(null) // { x, y, type: 'file'|'dir'|'root', path, name }
  const [creating, setCreating] = useState(null) // { dirPath } while a new-file input is showing

  const root = useMemo(() => {
    const tree = buildTree(paths)
    stampPaths(tree, '')
    return tree
  }, [paths])

  useEffect(() => {
    if (!menu) return
    function close() {
      setMenu(null)
    }
    window.addEventListener('click', close)
    window.addEventListener('scroll', close, true)
    return () => {
      window.removeEventListener('click', close)
      window.removeEventListener('scroll', close, true)
    }
  }, [menu])

  function toggle(path) {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  function openContextMenu(e, target) {
    if (!canWrite) return
    e.preventDefault()
    e.stopPropagation()
    setMenu({ x: e.clientX, y: e.clientY, ...target })
  }

  function openRootContextMenu(e) {
    if (!canWrite || e.target !== e.currentTarget) return
    e.preventDefault()
    setMenu({ x: e.clientX, y: e.clientY, type: 'root', path: '', name: '' })
  }

  function startCreate(dirPath) {
    if (dirPath) setExpanded((prev) => new Set(prev).add(dirPath))
    setCreating({ dirPath })
    setMenu(null)
  }

  function submitCreate(name) {
    if (!creating) return
    const path = creating.dirPath ? `${creating.dirPath}/${name}` : name
    setCreating(null)
    onCreateFile(path)
  }

  return (
    <div className="tree-container" onContextMenu={openRootContextMenu}>
      {canWrite && (
        <div className="tree-toolbar">
          <button type="button" className="tree-toolbar-btn" title="New File" onClick={() => startCreate('')}>
            {NEW_FILE_ICON}
            <span>New File</span>
          </button>
        </div>
      )}
      <ul className="editor-file-list tree-list">
        <TreeChildren
          node={root}
          depth={0}
          openPath={openPath}
          expanded={expanded}
          onToggle={toggle}
          onOpenFile={onOpenFile}
          canWrite={canWrite}
          renamingPath={renamingPath}
          onRequestRename={setRenamingPath}
          onSubmitRename={(oldPath, newName) => {
            setRenamingPath(null)
            const dir = oldPath.includes('/') ? oldPath.slice(0, oldPath.lastIndexOf('/')) : ''
            onRenameFile(oldPath, dir ? `${dir}/${newName}` : newName)
          }}
          onCancelRename={() => setRenamingPath(null)}
          onContextMenu={openContextMenu}
          onDeleteFile={onDeleteFile}
        />
        {creating && (
          <li>
            <div className="tree-row tree-file" style={{ paddingLeft: 8 + (creating.dirPath ? 14 : 0) + 18 }}>
              <span className="tree-icon">{FILE_ICON}</span>
              <RenameInput initialValue="" onSubmit={submitCreate} onCancel={() => setCreating(null)} />
            </div>
          </li>
        )}
      </ul>

      {menu && (
        <ul className="tree-context-menu" style={{ top: menu.y, left: menu.x }}>
          {menu.type !== 'file' && (
            <li>
              <button type="button" onClick={() => startCreate(menu.type === 'dir' ? menu.path : '')}>
                New File
              </button>
            </li>
          )}
          {menu.type === 'file' && (
            <li>
              <button
                type="button"
                onClick={() => {
                  setRenamingPath(menu.path)
                  setMenu(null)
                }}
              >
                Rename
              </button>
            </li>
          )}
          {menu.type === 'dir' && (
            <li>
              <button
                type="button"
                onClick={() => {
                  setRenamingPath(menu.path)
                  setMenu(null)
                }}
              >
                Rename
              </button>
            </li>
          )}
          {menu.type === 'file' && (
            <li>
              <button
                type="button"
                className="tree-context-danger"
                onClick={() => {
                  setMenu(null)
                  onDeleteFile(menu.path)
                }}
              >
                Delete
              </button>
            </li>
          )}
        </ul>
      )}
    </div>
  )
}
