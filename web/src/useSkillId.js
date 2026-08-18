import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-skill-id'

function initialSkillId() {
  return localStorage.getItem(STORAGE_KEY) || ''
}

// Remembers the last-picked skill across reloads, persisted to
// localStorage — mirrors useModel's pattern (see its doc comment). A
// stored ID referencing a since-deleted skill is handled the same way it
// already was before persistence existed: the <select> simply has no
// matching <option> selected, and picking a real skill (or explicitly
// picking "Default") overwrites the stale value — no separate validation
// needed here.
export function useSkillId() {
  const [skillId, setSkillId] = useState(initialSkillId)

  useEffect(() => {
    if (skillId) {
      localStorage.setItem(STORAGE_KEY, skillId)
    } else {
      localStorage.removeItem(STORAGE_KEY)
    }
  }, [skillId])

  return [skillId, setSkillId]
}
