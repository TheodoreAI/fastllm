import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-editor-panel'
const VALID_PANELS = new Set(['files', 'search', 'git'])

function initialPanel() {
  const stored = localStorage.getItem(STORAGE_KEY)
  return VALID_PANELS.has(stored) ? stored : 'files'
}

// Manages which editor sidebar panel (file tree / search / git) is
// showing, persisted to localStorage — mirrors useTheme's pattern.
export function useEditorPanel() {
  const [panel, setPanel] = useState(initialPanel)

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, panel)
  }, [panel])

  return [panel, setPanel]
}
