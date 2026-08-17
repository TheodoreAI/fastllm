import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-terminal-panel-height'
const DEFAULT_HEIGHT = 200 // px

function initialHeight() {
  const stored = Number(localStorage.getItem(STORAGE_KEY))
  return Number.isFinite(stored) && stored > 0 ? stored : DEFAULT_HEIGHT
}

// Manages the bottom terminal panel's height in pixels, persisted to
// localStorage — mirrors useSplitWidth's pattern. Stored as pixels rather
// than a fraction since a terminal's usefulness is tied to a roughly
// constant number of visible rows, not a proportion of the window.
export function useTerminalPanelHeight() {
  const [height, setHeight] = useState(initialHeight)

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, String(height))
  }, [height])

  return [height, setHeight]
}
