import GroupedDropdown from './GroupedDropdown'

// Display labels only — purely presentational, so hardcoding these here
// is fine. What's NOT hardcoded here is the "provider:" name prefix
// itself (e.g. "anthropic:") or which models belong to which provider:
// that comes from each model's `provider` field, set once in
// internal/llm.Router.ListModels (see internal/llm/client.go's Model.
// Provider doc comment) — the actual routing prefix
// (AnthropicPrefix/OpenAIPrefix/GeminiPrefix in router.go) lives in
// exactly one place instead of also being copied into this file.
const PROVIDER_LABELS = {
  anthropic: 'Anthropic (Claude)',
  openai: 'OpenAI (ChatGPT)',
  gemini: 'Google (Gemini)',
  nvidia: 'NVIDIA Build',
  cloudflare: 'Cloudflare Workers AI',
}

function groupModels(models) {
  const local = []
  const cloudByProvider = new Map()
  for (const m of models) {
    if (!m.provider) {
      local.push(m)
      continue
    }
    if (!cloudByProvider.has(m.provider)) cloudByProvider.set(m.provider, [])
    cloudByProvider.get(m.provider).push(m)
  }
  const cloud = [...cloudByProvider.entries()].map(([provider, groupModels]) => ({
    provider,
    label: PROVIDER_LABELS[provider] ?? provider,
    models: groupModels,
  }))
  return { local, cloud }
}

// Shapes {local, cloud} (see groupModels above) into GroupedDropdown's
// generic groups format.
function toDropdownGroups(local, cloud) {
  const groups = []
  if (local.length > 0) {
    groups.push({
      key: 'local',
      label: 'Local models',
      options: local.map((m) => ({ value: m.name, label: m.name })),
    })
  }
  for (const group of cloud) {
    groups.push({
      key: group.provider,
      label: group.label,
      options: group.models.map((m) => ({ value: m.name, label: m.name.slice(m.name.indexOf(':') + 1) })),
    })
  }
  return groups
}

export default function ModelPicker({
  models,
  model,
  onChange,
  fileAccessSettings,
  onOpenSettings,
  completionModel,
  onCompletionModelChange,
}) {
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
      <GroupedDropdown
        groups={toDropdownGroups(local, cloud)}
        value={model}
        onChange={onChange}
        disabled={models.length === 0}
        placeholder={models.length === 0 ? 'Default' : 'Select a model'}
      />

      {local.length > 0 && (
        <label className="model-completion-field">
          <span>Completion model</span>
          <select
            className="model-select"
            value={completionModel || ''}
            onChange={(e) => onCompletionModelChange?.(e.target.value)}
          >
            <option value="">Same as backend default</option>
            {local.map((m) => (
              <option key={m.name} value={m.name}>
                {m.name}
              </option>
            ))}
          </select>
        </label>
      )}

      <button type="button" className="file-access-status" onClick={onOpenSettings}>
        <span className={`file-access-dot ${readOn && modelSupportsFileTools ? 'is-on' : 'is-off'}`} />
        {statusLabel}
      </button>
    </section>
  )
}
