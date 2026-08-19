import { useEffect, useRef } from 'react'
import { useEscapeKey } from '../useEscapeKey'

export default function ConfirmDeleteModal({ heading, description, confirmLabel, onCancel, onConfirm }) {
  useEscapeKey(onCancel)
  const confirmRef = useRef(null)

  // Autofocus the confirm button so Enter confirms immediately — deleting
  // is triggered from the keyboard-reachable sidebar, so requiring a mouse
  // trip to the modal center just to confirm is pure friction.
  useEffect(() => {
    confirmRef.current?.focus()
  }, [])

  return (
    <div className="modal-overlay" onClick={onCancel}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h3>{heading}</h3>
        <p>{description}</p>
        <div className="modal-actions">
          <button type="button" className="btn-secondary" onClick={onCancel}>
            Cancel
          </button>
          <button type="button" className="btn-danger" ref={confirmRef} onClick={onConfirm}>
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}
