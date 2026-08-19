import { useState } from 'react'

// Wraps a sidebar panel with a drag handle so its position among sibling
// DraggableSections can be reordered. Drag state lives here (not in
// useSectionOrder) since it's purely transient UI feedback per drag.
export default function DraggableSection({ sectionKey, index, onReorder, highlighted, children }) {
  const [dragging, setDragging] = useState(false)
  const [dragOver, setDragOver] = useState(false)

  return (
    <div
      className={`draggable-section ${dragging ? 'is-dragging' : ''} ${dragOver ? 'is-drag-over' : ''} ${highlighted ? 'is-highlighted' : ''}`}
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
      {children}
    </div>
  )
}
