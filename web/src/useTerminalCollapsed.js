import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-terminal-collapsed'

function initialCollapsed() {
  return localStorage.getItem(STORAGE_KEY) === '1'
}

// Manages whether the bottom terminal panel is collapsed, persisted to
// localStorage — mirrors useSidebarCollapsed's pattern.
export function useTerminalCollapsed() {
  const [collapsed, setCollapsed] = useState(initialCollapsed)

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, collapsed ? '1' : '0')
  }, [collapsed])

  return [collapsed, setCollapsed]
}
