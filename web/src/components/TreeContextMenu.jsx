// The right-click menu FileTree shows for a file/dir/root target — New
// File/New Folder for dir and root targets, Rename/Delete for file and
// dir targets. Purely presentational; FileTree owns the open/close state
// and closes the menu itself on outside click/scroll.
export default function TreeContextMenu({ menu, onNewFile, onNewFolder, onRename, onDeleteFile, onDeleteFolder }) {
  return (
    <ul className="tree-context-menu" style={{ top: menu.y, left: menu.x }}>
      {menu.type !== 'file' && (
        <li>
          <button type="button" onClick={() => onNewFile(menu.type === 'dir' ? menu.path : '')}>
            New File
          </button>
        </li>
      )}
      {menu.type !== 'file' && (
        <li>
          <button type="button" onClick={() => onNewFolder(menu.type === 'dir' ? menu.path : '')}>
            New Folder
          </button>
        </li>
      )}
      {(menu.type === 'file' || menu.type === 'dir') && (
        <li>
          <button type="button" onClick={() => onRename(menu.path)}>
            Rename
          </button>
        </li>
      )}
      {menu.type === 'file' && (
        <li>
          <button type="button" className="tree-context-danger" onClick={() => onDeleteFile(menu.path)}>
            Delete
          </button>
        </li>
      )}
      {menu.type === 'dir' && (
        <li>
          <button type="button" className="tree-context-danger" onClick={() => onDeleteFolder(menu.path)}>
            Delete
          </button>
        </li>
      )}
    </ul>
  )
}
