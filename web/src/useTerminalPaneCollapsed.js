import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-terminal-pane-collapsed'

function initialCollapsed() {
  return localStorage.getItem(STORAGE_KEY) === '1'
}

// Manages whether the Terminal pane (now a full top-level column — see
// usePaneSlots — rather than the old bottom-docked strip) is hidden from
// the Editor/Terminal/Chat row entirely, persisted to localStorage —
// mirrors useEditorPaneCollapsed's pattern.
export function useTerminalPaneCollapsed() {
  const [collapsed, setCollapsed] = useState(initialCollapsed)

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, collapsed ? '1' : '0')
  }, [collapsed])

  return [collapsed, setCollapsed]
}
