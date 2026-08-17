export default function ModelPicker({ models, model, onChange, fileAccessSettings, onOpenSettings }) {
  const readOn = !!fileAccessSettings?.read_enabled
  const writeOn = !!fileAccessSettings?.write_enabled

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
        <span className={`file-access-dot ${readOn ? 'is-on' : 'is-off'}`} />
        {readOn ? (writeOn ? 'File read + write on' : 'File read only') : 'File access off'}
      </button>
    </section>
  )
}
