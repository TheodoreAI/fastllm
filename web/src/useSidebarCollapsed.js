import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-sidebar-collapsed'

function initialCollapsed() {
  return localStorage.getItem(STORAGE_KEY) === '1'
}

// Manages whether the sidebar is collapsed to an icon rail, persisted to
// localStorage — mirrors useTheme's pattern.
export function useSidebarCollapsed() {
  const [collapsed, setCollapsed] = useState(initialCollapsed)

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, collapsed ? '1' : '0')
  }, [collapsed])

  return [collapsed, setCollapsed]
}
