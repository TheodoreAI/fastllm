import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-settings-expanded'

function initialExpanded() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw) return JSON.parse(raw)
  } catch {
    // Corrupt/old payload — fall back to the default below.
  }
  return { appearance: true }
}

// Persists which Settings sub-sections are expanded, mirroring
// useSidebarCollapsed's pattern — previously reset to only "Appearance"
// open every time Settings was reopened, so a returning user re-clicked
// through the same chevron on every visit to reach e.g. File access.
export function useSettingsExpanded() {
  const [expanded, setExpanded] = useState(initialExpanded)

  useEffect(() => {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(expanded))
    } catch {
      // Storage full/disabled — expansion state just won't persist this
      // session, not worth failing over.
    }
  }, [expanded])

  return [expanded, setExpanded]
}
