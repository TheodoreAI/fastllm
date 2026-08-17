import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-split-width'
const DEFAULT_WIDTH = 0.5 // chat pane's share of the split, 0-1

function initialWidth() {
  const stored = Number(localStorage.getItem(STORAGE_KEY))
  return Number.isFinite(stored) && stored > 0 && stored < 1 ? stored : DEFAULT_WIDTH
}

// Manages the chat pane's fractional width (0-1) in the Chat|Editor
// split view, persisted to localStorage — mirrors useSidebarCollapsed's
// pattern. Stored as a fraction rather than a pixel width so it stays
// sensible across different window sizes.
export function useSplitWidth() {
  const [width, setWidth] = useState(initialWidth)

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, String(width))
  }, [width])

  return [width, setWidth]
}
