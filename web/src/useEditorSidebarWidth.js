import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-editor-sidebar-width'
const DEFAULT_WIDTH = 260 // px, matches .editor-sidebar's old fixed width

function initialWidth() {
  const stored = Number(localStorage.getItem(STORAGE_KEY))
  return Number.isFinite(stored) && stored > 0 ? stored : DEFAULT_WIDTH
}

// Manages the editor's file-tree sidebar (Files/Search/Git) width in
// pixels, persisted to localStorage — mirrors useTerminalPanelHeight's
// pattern (pixels rather than a fraction, since a file tree's usefulness
// is tied to how many characters of a filename fit, not a proportion of
// the window).
export function useEditorSidebarWidth() {
  const [width, setWidth] = useState(initialWidth)

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, String(width))
  }, [width])

  return [width, setWidth]
}
