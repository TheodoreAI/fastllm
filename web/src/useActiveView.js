import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-active-view'
const VALID_VIEWS = new Set(['chat', 'editor', 'split'])

function initialView() {
  const stored = localStorage.getItem(STORAGE_KEY)
  return VALID_VIEWS.has(stored) ? stored : 'chat'
}

// Manages which top-level view (chat / editor / split) is showing,
// persisted to localStorage — mirrors useTheme's pattern, so reopening
// the app returns to whatever layout the user left it in instead of
// always resetting to Chat.
export function useActiveView() {
  const [view, setView] = useState(initialView)

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, view)
  }, [view])

  return [view, setView]
}
