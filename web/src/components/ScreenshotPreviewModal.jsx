import { useEffect, useState } from 'react'
import { useEscapeKey } from '../useEscapeKey'

function timestampForFilename() {
  return new Date().toISOString().replace(/[:.]/g, '-')
}

// Shows a captured screenshot Blob with Copy-to-clipboard and Save-as-file
// actions. Structurally mirrors ConfirmDeleteModal.jsx (modal-overlay +
// modal, click-outside/Esc to close) — the "screenshot-preview-modal"
// class layered on top of .modal follows the same pattern SettingsModal
// uses to widen the default 360px modal for content that needs more room.
export default function ScreenshotPreviewModal({ blob, onClose }) {
  const [imageUrl, setImageUrl] = useState(null)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    const url = URL.createObjectURL(blob)
    setImageUrl(url)
    return () => URL.revokeObjectURL(url)
  }, [blob])

  useEscapeKey(onClose)

  async function handleCopy() {
    try {
      await navigator.clipboard.write([new ClipboardItem({ 'image/png': blob })])
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      // Same tolerance as MessageContent's code-block copy button —
      // clipboard access can fail on permissions/context; nothing else to
      // recover into beyond leaving the button unflipped.
    }
  }

  function handleSave() {
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `fastllm-screenshot-${timestampForFilename()}.png`
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal screenshot-preview-modal" onClick={(e) => e.stopPropagation()}>
        <h3>Screenshot</h3>
        {imageUrl && (
          <div className="screenshot-preview-frame">
            <img src={imageUrl} alt="Captured screenshot" />
          </div>
        )}
        <div className="modal-actions">
          <button type="button" className="btn-secondary" onClick={onClose}>
            Close
          </button>
          <button type="button" className="btn-secondary" onClick={handleSave}>
            Save
          </button>
          <button type="button" className="btn-primary" onClick={handleCopy}>
            {copied ? 'Copied' : 'Copy'}
          </button>
        </div>
      </div>
    </div>
  )
}
