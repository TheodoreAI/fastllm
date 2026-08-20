import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-editor-pane-collapsed'

function initialCollapsed() {
  return localStorage.getItem(STORAGE_KEY) === '1'
}

// Manages whether the whole editor pane (Files/Search/Git sidebar, open
// tabs, terminal — the entire column, not just its own internal
// sidebar/terminal sub-panels) is collapsed, persisted to localStorage —
// mirrors useEditorSidebarCollapsed's pattern, kept separate since the
// two collapse independently.
export function useEditorPaneCollapsed() {
  const [collapsed, setCollapsed] = useState(initialCollapsed)

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, collapsed ? '1' : '0')
  }, [collapsed])

  return [collapsed, setCollapsed]
}
