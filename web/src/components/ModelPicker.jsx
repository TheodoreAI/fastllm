export default function ModelPicker({ models, model, onChange }) {
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
    </section>
  )
}
