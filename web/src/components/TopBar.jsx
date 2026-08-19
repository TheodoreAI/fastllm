import { useEffect, useRef, useState } from 'react'
import { useEscapeKey } from '../useEscapeKey'

// One File/Edit/Help-style menu: a button that opens a small flat list of
// actions below it. Deliberately not GroupedDropdown (that's for a value
// picker with a persistent selection) — these are one-shot commands with
// no "current value" to show on the trigger.
function TopBarMenu({ label, items, openMenu, onOpenMenu }) {
  const open = openMenu === label
  const rootRef = useRef(null)

  useEscapeKey(() => {
    if (open) onOpenMenu(null)
  })

  useEffect(() => {
    if (!open) return
    function handlePointerDown(e) {
      if (rootRef.current && !rootRef.current.contains(e.target)) onOpenMenu(null)
    }
    document.addEventListener('mousedown', handlePointerDown)
    return () => document.removeEventListener('mousedown', handlePointerDown)
  }, [open, onOpenMenu])

  function runItem(item) {
    onOpenMenu(null)
    item.onSelect()
  }

  return (
    <div className="top-bar-menu" ref={rootRef}>
      <button
        type="button"
        className={`top-bar-menu-trigger ${open ? 'is-open' : ''}`}
        onClick={() => onOpenMenu(open ? null : label)}
        onMouseEnter={() => {
          // Once one top-bar menu is open, hovering a sibling should swap
          // straight to it (standard menu-bar behavior) instead of
          // requiring another click.
          if (openMenu && openMenu !== label) onOpenMenu(label)
        }}
        aria-haspopup="menu"
        aria-expanded={open}
      >
        {label}
      </button>
      {open && (
        <ul className="top-bar-menu-list" role="menu">
          {items.map((item, i) =>
            item.separator ? (
              <li key={`sep-${i}`} className="top-bar-menu-separator" role="separator" />
            ) : (
              <li key={item.label} role="none">
                <button
                  type="button"
                  role="menuitem"
                  className="top-bar-menu-item"
                  onClick={() => runItem(item)}
                >
                  <span>{item.label}</span>
                  {item.shortcut && <span className="top-bar-menu-shortcut">{item.shortcut}</span>}
                </button>
              </li>
            )
          )}
        </ul>
      )}
    </div>
  )
}

// Replaces cmd/desktop's native Windows menu bar with a plain in-app
// component. The native menu was the one part of the app that couldn't
// be themed: Windows renders it via undocumented uxtheme.dll ordinals
// that don't resolve on some Windows builds (confirmed via a diagnostic
// probe earlier in this app's development — not a fastllm or Wails bug,
// a genuine platform limitation on affected builds), so it stayed stuck
// in light/white chrome regardless of the app's own theme. This is just
// normal HTML/CSS, so it follows the theme like everything else.
//
// Desktop-only (see App.jsx's isWails() guard around where this is
// rendered) — cmd/server's browser tab has no native menu bar to
// replace, and "Exit" has no meaning for a browser tab.
export default function TopBar({ onOpenFolder, onQuit, onAbout }) {
  const [openMenu, setOpenMenu] = useState(null)

  function runEditCommand(command) {
    document.execCommand(command)
  }

  const fileItems = [
    { label: 'Open Folder…', shortcut: 'Ctrl+O', onSelect: onOpenFolder },
    { separator: true },
    { label: 'Exit', shortcut: 'Ctrl+Q', onSelect: onQuit },
  ]

  const editItems = [
    { label: 'Undo', shortcut: 'Ctrl+Z', onSelect: () => runEditCommand('undo') },
    { label: 'Redo', shortcut: 'Ctrl+Y', onSelect: () => runEditCommand('redo') },
    { separator: true },
    { label: 'Cut', shortcut: 'Ctrl+X', onSelect: () => runEditCommand('cut') },
    { label: 'Copy', shortcut: 'Ctrl+C', onSelect: () => runEditCommand('copy') },
    { label: 'Paste', shortcut: 'Ctrl+V', onSelect: () => runEditCommand('paste') },
    { separator: true },
    { label: 'Select All', shortcut: 'Ctrl+A', onSelect: () => runEditCommand('selectAll') },
  ]

  const helpItems = [{ label: 'About fastllm', onSelect: onAbout }]

  return (
    <div className="top-bar">
      <TopBarMenu label="File" items={fileItems} openMenu={openMenu} onOpenMenu={setOpenMenu} />
      <TopBarMenu label="Edit" items={editItems} openMenu={openMenu} onOpenMenu={setOpenMenu} />
      <TopBarMenu label="Help" items={helpItems} openMenu={openMenu} onOpenMenu={setOpenMenu} />
    </div>
  )
}
