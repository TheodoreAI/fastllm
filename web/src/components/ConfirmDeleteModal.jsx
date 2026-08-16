import { useEscapeKey } from '../useEscapeKey'

export default function ConfirmDeleteModal({ heading, description, confirmLabel, onCancel, onConfirm }) {
  useEscapeKey(onCancel)

  return (
    <div className="modal-overlay" onClick={onCancel}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h3>{heading}</h3>
        <p>{description}</p>
        <div className="modal-actions">
          <button type="button" className="btn-secondary" onClick={onCancel}>
            Cancel
          </button>
          <button type="button" className="btn-danger" onClick={onConfirm}>
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}
