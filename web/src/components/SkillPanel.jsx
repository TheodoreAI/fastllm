export default function SkillPanel({
  skills,
  skillId,
  onSkillIdChange,
  onDeleteSkill,
  skillFormOpen,
  onOpenForm,
  onCloseForm,
  skillName,
  onSkillNameChange,
  skillPrompt,
  onSkillPromptChange,
  onCreateSkill,
}) {
  return (
    <section className="panel">
      <h2>Skill</h2>
      <div className="skill-select-row">
        <select
          className="model-select"
          value={skillId}
          onChange={(e) => onSkillIdChange(e.target.value)}
        >
          <option value="">General assistant</option>
          {skills.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </select>
        {skillId && (
          <button
            type="button"
            className="btn-icon"
            title="Delete this skill"
            onClick={() => onDeleteSkill(skillId)}
          >
            ×
          </button>
        )}
      </div>

      {skillFormOpen ? (
        <form onSubmit={onCreateSkill} className="skill-form">
          <input
            className="skill-name-input"
            placeholder="Skill name (e.g. Code Reviewer)"
            value={skillName}
            onChange={(e) => onSkillNameChange(e.target.value)}
          />
          <textarea
            placeholder="System prompt for this skill…"
            value={skillPrompt}
            onChange={(e) => onSkillPromptChange(e.target.value)}
            rows={5}
          />
          <div className="skill-form-actions">
            <button type="submit" className="btn-primary">Save skill</button>
            <button type="button" className="btn-secondary" onClick={onCloseForm}>
              Cancel
            </button>
          </div>
        </form>
      ) : (
        <button type="button" className="btn-secondary" onClick={onOpenForm}>
          + New skill
        </button>
      )}
    </section>
  )
}
