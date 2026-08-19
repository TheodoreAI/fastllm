import { useEffect, useRef, useState } from 'react'
import { useEscapeKey } from '../useEscapeKey'

// Custom dropdown replacing a native <select> wherever the list is
// grouped (<optgroup>-shaped) and can grow long enough to scroll — once
// that happens, a native <select> popup's own scrollbar is OS-rendered
// and narrows/shifts the popup, and nothing in page CSS can reach inside
// that popup to fix it (see ModelPicker's original use case). This
// renders the list as plain markup instead, so it gets this app's own
// scrollbar and layout, with no such ceiling.
//
// groups: [{ key, label, options: [{ value, label }] }] — a group with
// no `label` renders its options with no header row (e.g. a single
// top-level group with nothing else to distinguish it from).
export default function GroupedDropdown({ groups, value, onChange, disabled, placeholder = 'Select…' }) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef(null)
  const listRef = useRef(null)

  const rows = []
  for (const group of groups) {
    if (group.label) rows.push({ kind: 'header', key: `header-${group.key}`, label: group.label })
    for (const opt of group.options) {
      rows.push({ kind: 'option', key: opt.value, value: opt.value, label: opt.label })
    }
  }
  const selectedRow = rows.find((r) => r.kind === 'option' && r.value === value)

  useEscapeKey(() => setOpen(false))

  useEffect(() => {
    if (!open) return
    function handlePointerDown(e) {
      if (rootRef.current && !rootRef.current.contains(e.target)) setOpen(false)
    }
    document.addEventListener('mousedown', handlePointerDown)
    return () => document.removeEventListener('mousedown', handlePointerDown)
  }, [open])

  useEffect(() => {
    if (!open || !listRef.current) return
    const activeEl = listRef.current.querySelector('[data-active="true"]')
    activeEl?.scrollIntoView({ block: 'nearest' })
  }, [open])

  function selectRow(row) {
    onChange(row.value)
    setOpen(false)
  }

  function handleTriggerKeyDown(e) {
    if (disabled) return
    if (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      setOpen(true)
    }
  }

  function handleListKeyDown(e) {
    const optionRows = rows.filter((r) => r.kind === 'option')
    const currentIndex = optionRows.findIndex((r) => r.value === value)
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      const next = optionRows[Math.min(currentIndex + 1, optionRows.length - 1)]
      if (next) onChange(next.value)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      const prev = optionRows[Math.max(currentIndex - 1, 0)]
      if (prev) onChange(prev.value)
    } else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      setOpen(false)
    }
  }

  return (
    <div className="model-dropdown" ref={rootRef}>
      <button
        type="button"
        className="model-dropdown-trigger"
        disabled={disabled}
        onClick={() => setOpen((v) => !v)}
        onKeyDown={handleTriggerKeyDown}
        aria-haspopup="listbox"
        aria-expanded={open}
      >
        <span className="model-dropdown-trigger-label">{selectedRow?.label || placeholder}</span>
        <span className="model-dropdown-caret" aria-hidden="true" />
      </button>
      {open && (
        <ul
          className="model-dropdown-list"
          role="listbox"
          ref={listRef}
          tabIndex={-1}
          onKeyDown={handleListKeyDown}
        >
          {rows.map((row) =>
            row.kind === 'header' ? (
              <li key={row.key} className="model-dropdown-group-label" role="presentation">
                {row.label}
              </li>
            ) : (
              <li
                key={row.key}
                role="option"
                aria-selected={row.value === value}
                data-active={row.value === value}
                className={`model-dropdown-option ${row.value === value ? 'is-selected' : ''}`}
                onClick={() => selectRow(row)}
              >
                {row.label}
              </li>
            )
          )}
        </ul>
      )}
    </div>
  )
}
