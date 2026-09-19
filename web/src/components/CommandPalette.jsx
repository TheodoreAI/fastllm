import { useEffect, useRef, useState } from 'react'
import { useEscapeKey } from '../useEscapeKey'
import { fuzzyFilter } from '../fuzzyMatch'
import { shortcutLabel } from '../commands'

// Shared modal for both Ctrl/Cmd+P fuzzy file-open ("files" mode) and
// Ctrl/Cmd+Shift+P command palette ("commands" mode) — same overlay,
// fuzzy-filtered list, and arrow/Enter/Esc handling either way, just a
// different source list and a different action per selected row. One
// component instead of two near-identical ones is what keeps their feel
// from drifting apart as either grows.
export default function CommandPalette({ open = true, mode, files, commands, onOpenFile, onClose }) {
  if (!open) return null

  const [query, setQuery] = useState('')
  const [highlighted, setHighlighted] = useState(0)
  const inputRef = useRef(null)

  useEscapeKey(onClose)

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  const items =
    mode === 'files'
      ? fuzzyFilter(query, files, (path) => path).map((path) => ({ id: path, label: path }))
      : fuzzyFilter(query, commands, (cmd) => cmd.label).map((cmd) => ({
          id: cmd.id,
          label: cmd.label,
          shortcut: shortcutLabel(cmd),
          run: cmd.run,
        }))

  // The filtered list changes on every keystroke — clamp instead of always
  // resetting to 0, so narrowing a list doesn't fight a user who's already
  // arrowed a couple of rows down.
  useEffect(() => {
    setHighlighted((h) => Math.min(h, Math.max(items.length - 1, 0)))
  }, [items.length])

  function activate(item) {
    if (!item) return
    if (mode === 'files') {
      onOpenFile(item.id)
    } else {
      item.run()
    }
    onClose()
  }

  function handleKeyDown(e) {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setHighlighted((h) => Math.min(h + 1, items.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setHighlighted((h) => Math.max(h - 1, 0))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      activate(items[highlighted])
    }
  }

  return (
    <div className="modal-overlay command-palette-overlay" onClick={onClose}>
      <div className="command-palette" onClick={(e) => e.stopPropagation()}>
        <input
          ref={inputRef}
          className="command-palette-input"
          type="text"
          value={query}
          onChange={(e) => {
            setQuery(e.target.value)
            setHighlighted(0)
          }}
          onKeyDown={handleKeyDown}
          placeholder={mode === 'files' ? 'Go to file…' : 'Type a command…'}
        />
        <div className="command-palette-list">
          {items.length === 0 && <div className="command-palette-empty">No matches.</div>}
          {items.map((item, i) => (
            <div
              key={item.id}
              className={`command-palette-item${i === highlighted ? ' highlighted' : ''}`}
              onMouseEnter={() => setHighlighted(i)}
              onClick={() => activate(item)}
            >
              <span className="command-palette-item-label">{item.label}</span>
              {item.shortcut && <span className="command-palette-item-shortcut">{item.shortcut}</span>}
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
