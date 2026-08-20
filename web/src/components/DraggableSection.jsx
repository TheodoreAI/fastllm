import { useState } from 'react'

// Wraps a sidebar panel with a drag handle so its position among sibling
// DraggableSections can be reordered. Drag state lives here (not in
// useSectionOrder) since it's purely transient UI feedback per drag.
//
// `label`, when passed, switches the handle from a hover-revealed icon
// absolutely positioned over the corner of `children` (the sidebar's
// compact rows, where nothing else lives in that corner) to a real,
// always-flowed title bar rendered above `children` instead — needed for
// wrapping a full pane (Editor/Terminal/Chat — see App.jsx's
// usePaneSlots) whose own header already has real buttons living in that
// same corner (Chat's +/✕, Terminal's tab-add), which the overlay would
// otherwise sit on top of and block.
//
// `onCollapse`, when passed alongside `label`, renders a small ✕ button
// in that title bar, just left of the drag handle — the only way to
// collapse a pane whose own inner header has no close button of its own
// (Terminal; Chat and Editor already have one — see ChatPanel's
// chat-header-close and App.jsx's view-rail toggle).
export default function DraggableSection({ sectionKey, index, onReorder, highlighted, className, style, label, onCollapse, children }) {
  const [dragging, setDragging] = useState(false)
  const [dragOver, setDragOver] = useState(false)

  const handle = (
    <div
      className="drag-handle"
      draggable
      title="Drag to reorder"
      onDragStart={(e) => {
        e.dataTransfer.setData('text/plain', sectionKey)
        e.dataTransfer.effectAllowed = 'move'
        setDragging(true)
      }}
      onDragEnd={() => setDragging(false)}
    >
      <span className="drag-handle-dots">⠿</span>
    </div>
  )

  return (
    <div
      className={`draggable-section ${dragging ? 'is-dragging' : ''} ${dragOver ? 'is-drag-over' : ''} ${highlighted ? 'is-highlighted' : ''} ${className ?? ''}`}
      style={style}
      data-section={sectionKey}
      onDragOver={(e) => {
        e.preventDefault()
        setDragOver(true)
      }}
      onDragLeave={() => setDragOver(false)}
      onDrop={(e) => {
        e.preventDefault()
        setDragOver(false)
        const fromKey = e.dataTransfer.getData('text/plain')
        if (fromKey && fromKey !== sectionKey) onReorder(fromKey, index)
      }}
    >
      {label ? (
        <div className="draggable-section-titlebar">
          <span className="draggable-section-title">{label}</span>
          <div className="draggable-section-titlebar-actions">
            {onCollapse && (
              <button
                type="button"
                className="draggable-section-collapse"
                title={`Collapse ${label}`}
                onClick={onCollapse}
              >
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  <circle cx="12" cy="12" r="9" />
                  <path d="M9 9l6 6M15 9l-6 6" />
                </svg>
              </button>
            )}
            {handle}
          </div>
        </div>
      ) : (
        handle
      )}
      {children}
    </div>
  )
}
