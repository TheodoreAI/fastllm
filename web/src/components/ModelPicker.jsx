export default function ModelPicker({ models, model, onChange, fileAccessSettings, onOpenSettings }) {
  const readOn = !!fileAccessSettings?.read_enabled
  const writeOn = !!fileAccessSettings?.write_enabled
  const selected = models.find((m) => m.name === model)
  const modelSupportsFileTools = selected ? !!selected.supports_file_tools : true

  let statusLabel = 'File access off'
  if (readOn && !modelSupportsFileTools) {
    statusLabel = 'Not supported by this model'
  } else if (readOn) {
    statusLabel = writeOn ? 'File read + write on' : 'File read only'
  }

  return (
    <section className="panel">
      <h2>Model</h2>
      <select
        className="model-select"
        value={model}
        onChange={(e) => onChange(e.target.value)}
        disabled={models.length === 0}
      >
        {models.length === 0 && <option>Default</option>}
        {models.map((m) => (
          <option key={m.name} value={m.name}>
            {m.name}
          </option>
        ))}
      </select>

      <button type="button" className="file-access-status" onClick={onOpenSettings}>
        <span className={`file-access-dot ${readOn && modelSupportsFileTools ? 'is-on' : 'is-off'}`} />
        {statusLabel}
      </button>
    </section>
  )
}
