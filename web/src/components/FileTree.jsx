import { useEffect, useMemo, useRef, useState } from 'react'
import { CHEVRON, FOLDER_ICON, FILE_ICON, RENAME_ICON, DELETE_ICON, NEW_FILE_ICON, NEW_FOLDER_ICON } from './treeIcons'
import TreeContextMenu from './TreeContextMenu'

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

// The drag payload is a JSON-encoded string on a custom MIME type — using
// a custom type (rather than 'text/plain') keeps this tree from reacting
// to drags originating outside it (e.g. dragging text from elsewhere in
// the page).
const DND_MIME = 'application/x-fastllm-file-path'

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

function DirRow({ name, node, depth, openPath, expanded, onToggle, onOpenFile, canWrite, renamingPath, onRequestRename, onSubmitRename, onCancelRename, onContextMenu, onDeleteFolder, dropTarget, onDragStartFile, onDragOverTarget, onDragLeaveTarget, onDropTarget }) {
  const dirPath = node.__path
  const isOpen = expanded.has(dirPath)
  const isRenaming = renamingPath === dirPath
  return (
    <>
      <li>
        <div
          className={`tree-row tree-dir ${dropTarget === dirPath ? 'is-drop-target' : ''}`}
          style={{ paddingLeft: 8 + depth * 14 }}
          onClick={() => !isRenaming && onToggle(dirPath)}
          onContextMenu={(e) => onContextMenu(e, { type: 'dir', path: dirPath, name })}
          draggable={canWrite && !isRenaming}
          onDragStart={(e) => onDragStartFile(e, dirPath)}
          onDragOver={(e) => onDragOverTarget(e, dirPath)}
          onDragLeave={onDragLeaveTarget}
          onDrop={(e) => onDropTarget(e, dirPath)}
          title={dirPath}
        >
          <span className={`tree-chevron ${isOpen ? 'is-open' : ''}`}>{CHEVRON}</span>
          <span className="tree-icon">{FOLDER_ICON(isOpen)}</span>
          {isRenaming ? (
            <RenameInput initialValue={name} onSubmit={(next) => onSubmitRename(dirPath, next)} onCancel={onCancelRename} />
          ) : (
            <>
              <span className="tree-label">{name}</span>
              {canWrite && (
                <RowActions onRename={() => onRequestRename(dirPath)} onDelete={() => onDeleteFolder(dirPath)} />
              )}
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
          onDeleteFolder={onDeleteFolder}
          dropTarget={dropTarget}
          onDragStartFile={onDragStartFile}
          onDragOverTarget={onDragOverTarget}
          onDragLeaveTarget={onDragLeaveTarget}
          onDropTarget={onDropTarget}
        />
      )}
    </>
  )
}

function TreeChildren({ node, depth, openPath, expanded, onToggle, onOpenFile, canWrite, renamingPath, onRequestRename, onSubmitRename, onCancelRename, onContextMenu, onDeleteFile, onDeleteFolder, dropTarget, onDragStartFile, onDragOverTarget, onDragLeaveTarget, onDropTarget }) {
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
          onDeleteFolder={onDeleteFolder}
          dropTarget={dropTarget}
          onDragStartFile={onDragStartFile}
          onDragOverTarget={onDragOverTarget}
          onDragLeaveTarget={onDragLeaveTarget}
          onDropTarget={onDropTarget}
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
              draggable={canWrite && !isRenaming}
              onDragStart={(e) => onDragStartFile(e, file.path)}
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
export default function FileTree({ paths, openPath, onOpenFile, canWrite, onCreateFile, onDeleteFile, onDeleteFolder, onRenameFile }) {
  const [expanded, setExpanded] = useState(() => new Set())
  const [renamingPath, setRenamingPath] = useState(null)
  const [menu, setMenu] = useState(null) // { x, y, type: 'file'|'dir'|'root', path, name }
  const [creating, setCreating] = useState(null) // { dirPath, folder } while a new-file/-folder input is showing
  const [dropTarget, setDropTarget] = useState(null) // path of the dir row currently dragged over, '' for root
  const draggingPathRef = useRef(null)

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
    setCreating({ dirPath, folder: false })
    setMenu(null)
  }

  function startCreateFolder(dirPath) {
    if (dirPath) setExpanded((prev) => new Set(prev).add(dirPath))
    setCreating({ dirPath, folder: true })
    setMenu(null)
  }

  function submitCreate(name) {
    if (!creating) return
    const isFolder = creating.folder
    const path = creating.dirPath ? `${creating.dirPath}/${name}` : name
    setCreating(null)
    // Folders only exist implicitly via file paths (see buildTree above),
    // so "creating a folder" means creating a placeholder file inside it —
    // the folder then shows up in the tree like any other. Immediately
    // renaming that placeholder is the natural next step, so kick that off
    // rather than leaving a stray README-ish file sitting there unnamed.
    const filePath = isFolder ? `${path}/new-file` : path
    onCreateFile(filePath)
    if (isFolder) {
      setExpanded((prev) => new Set(prev).add(path))
      setRenamingPath(filePath)
    }
  }

  function movePath(fromPath, toDir) {
    if (fromPath === toDir) return
    // Dragging a folder onto itself or one of its own descendants would
    // otherwise silently fail (or worse, orphan files) — block it up
    // front rather than letting the backend's os.Rename error surface as
    // a confusing generic message.
    if (toDir === fromPath || toDir.startsWith(`${fromPath}/`)) return
    const name = fromPath.includes('/') ? fromPath.slice(fromPath.lastIndexOf('/') + 1) : fromPath
    const currentDir = fromPath.includes('/') ? fromPath.slice(0, fromPath.lastIndexOf('/')) : ''
    if (currentDir === toDir) return
    const toPath = toDir ? `${toDir}/${name}` : name
    onRenameFile(fromPath, toPath)
  }

  function isDirPath(path) {
    let node = root
    for (const part of path.split('/')) {
      if (!node.dirs.has(part)) return false
      node = node.dirs.get(part)
    }
    return true
  }

  function handleDragStartFile(e, path) {
    draggingPathRef.current = path
    e.dataTransfer.effectAllowed = 'move'
    e.dataTransfer.setData(DND_MIME, path)
  }

  function handleDragOverTarget(e, dirPath) {
    if (!canWrite || !draggingPathRef.current) return
    e.preventDefault()
    e.dataTransfer.dropEffect = 'move'
    setDropTarget(dirPath)
  }

  function handleDragLeaveTarget() {
    setDropTarget(null)
  }

  function handleDropTarget(e, dirPath) {
    e.preventDefault()
    e.stopPropagation()
    setDropTarget(null)
    const fromPath = draggingPathRef.current ?? e.dataTransfer.getData(DND_MIME)
    draggingPathRef.current = null
    if (!fromPath) return
    const targetDir = isDirPath(dirPath) ? dirPath : dirPath.includes('/') ? dirPath.slice(0, dirPath.lastIndexOf('/')) : ''
    movePath(fromPath, targetDir)
  }

  function handleRootDragOver(e) {
    if (!canWrite || !draggingPathRef.current) return
    e.preventDefault()
    e.dataTransfer.dropEffect = 'move'
    setDropTarget('')
  }

  function handleRootDrop(e) {
    e.preventDefault()
    setDropTarget(null)
    const fromPath = draggingPathRef.current ?? e.dataTransfer.getData(DND_MIME)
    draggingPathRef.current = null
    if (!fromPath) return
    movePath(fromPath, '')
  }

  return (
    <div
      className={`tree-container ${dropTarget === '' ? 'is-drop-target-root' : ''}`}
      onContextMenu={openRootContextMenu}
      onDragOver={handleRootDragOver}
      onDragLeave={(e) => {
        if (e.target === e.currentTarget) setDropTarget(null)
      }}
      onDrop={handleRootDrop}
    >
      {canWrite ? (
        <div className="tree-toolbar">
          <button type="button" className="tree-toolbar-btn" title="New File" onClick={() => startCreate('')}>
            {NEW_FILE_ICON}
            <span>New File</span>
          </button>
          <button type="button" className="tree-toolbar-btn" title="New Folder" onClick={() => startCreateFolder('')}>
            {NEW_FOLDER_ICON}
            <span>New Folder</span>
          </button>
        </div>
      ) : (
        <div className="tree-readonly-hint" title="Enable write access in Settings → File access">
          Read-only — writes disabled in Settings
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
          onDeleteFolder={onDeleteFolder}
          dropTarget={dropTarget}
          onDragStartFile={handleDragStartFile}
          onDragOverTarget={handleDragOverTarget}
          onDragLeaveTarget={handleDragLeaveTarget}
          onDropTarget={handleDropTarget}
        />
        {creating && (
          <li>
            <div className="tree-row tree-file" style={{ paddingLeft: 8 + (creating.dirPath ? 14 : 0) + 18 }}>
              <span className="tree-icon">{creating.folder ? FOLDER_ICON(true) : FILE_ICON}</span>
              <RenameInput
                initialValue=""
                onSubmit={submitCreate}
                onCancel={() => setCreating(null)}
              />
            </div>
          </li>
        )}
      </ul>

      {menu && (
        <TreeContextMenu
          menu={menu}
          onNewFile={startCreate}
          onNewFolder={startCreateFolder}
          onRename={(path) => {
            setRenamingPath(path)
            setMenu(null)
          }}
          onDeleteFile={(path) => {
            setMenu(null)
            onDeleteFile(path)
          }}
          onDeleteFolder={(path) => {
            setMenu(null)
            onDeleteFolder(path)
          }}
        />
      )}
    </div>
  )
}
