import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-editor-sidebar-collapsed'

function initialCollapsed() {
  return localStorage.getItem(STORAGE_KEY) === '1'
}

// Manages whether the editor's file-tree sidebar (Files/Search/Git) is
// collapsed, persisted to localStorage — mirrors useSidebarCollapsed's
// pattern for the chat sidebar, kept separate since the two panels
// collapse independently.
export function useEditorSidebarCollapsed() {
  const [collapsed, setCollapsed] = useState(initialCollapsed)

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, collapsed ? '1' : '0')
  }, [collapsed])

  return [collapsed, setCollapsed]
}
