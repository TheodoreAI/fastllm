import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-theme'

function systemPrefersLight() {
  return window.matchMedia?.('(prefers-color-scheme: light)').matches ?? false
}

function initialTheme() {
  const stored = localStorage.getItem(STORAGE_KEY)
  if (stored === 'light' || stored === 'dark') return stored
  return systemPrefersLight() ? 'light' : 'dark'
}

// Manages the app's light/dark theme, persisted to localStorage and
// applied via a data-theme attribute on the root element (see App.css).
export function useTheme() {
  const [theme, setTheme] = useState(initialTheme)

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
    localStorage.setItem(STORAGE_KEY, theme)
  }, [theme])

  return [theme, setTheme]
}
