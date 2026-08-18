// Cloud model names carry a "provider:" prefix (see internal/llm.Router)
// so the backend can tell which API a picked model routes to — the
// prefix is what StreamChat dispatches on, not just a display label, so
// it has to stay in the <option>'s value. Splitting it out here is purely
// about how the dropdown reads: a flat "anthropic:claude-sonnet-5" next
// to a bare "llama3.1" looks like an error, whereas grouping under an
// "Anthropic (Claude)" optgroup with the bare model name reads the way a
// user would expect a provider picker to.
const PROVIDER_GROUPS = [
  { prefix: 'anthropic:', label: 'Anthropic (Claude)' },
  { prefix: 'openai:', label: 'OpenAI (ChatGPT)' },
  { prefix: 'gemini:', label: 'Google (Gemini)' },
]

function groupModels(models) {
  const local = []
  const cloud = PROVIDER_GROUPS.map((g) => ({ ...g, models: [] }))
  for (const m of models) {
    const group = cloud.find((g) => m.name.startsWith(g.prefix))
    if (group) group.models.push(m)
    else local.push(m)
  }
  return { local, cloud: cloud.filter((g) => g.models.length > 0) }
}

export default function ModelPicker({ models, model, onChange, fileAccessSettings, onOpenSettings }) {
  const readOn = !!fileAccessSettings?.read_enabled
  const writeOn = !!fileAccessSettings?.write_enabled
  const selected = models.find((m) => m.name === model)
  const modelSupportsFileTools = selected ? !!selected.supports_file_tools : true
  const { local, cloud } = groupModels(models)

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
        {local.map((m) => (
          <option key={m.name} value={m.name}>
            {m.name}
          </option>
        ))}
        {cloud.map((group) => (
          <optgroup key={group.prefix} label={group.label}>
            {group.models.map((m) => (
              <option key={m.name} value={m.name}>
                {m.name.slice(group.prefix.length)}
              </option>
            ))}
          </optgroup>
        ))}
      </select>

      <button type="button" className="file-access-status" onClick={onOpenSettings}>
        <span className={`file-access-dot ${readOn && modelSupportsFileTools ? 'is-on' : 'is-off'}`} />
        {statusLabel}
      </button>
    </section>
  )
}
