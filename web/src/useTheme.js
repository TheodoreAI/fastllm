import { useEffect, useState } from 'react'
import { isWails } from './api'

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
    // Wails' native menu bar (File → …) is drawn by Windows itself, not by
    // this page's CSS — it stays on the OS's default light chrome unless
    // explicitly told to follow dark mode, which is why it looked like a
    // stray white bar above an otherwise fully dark window. Syncing it
    // here means the toggle in Settings also flips the native chrome, not
    // just the in-page colors.
    if (isWails() && window.runtime) {
      if (theme === 'dark') {
        window.runtime.WindowSetDarkTheme()
      } else {
        window.runtime.WindowSetLightTheme()
      }
    }
  }, [theme])

  return [theme, setTheme]
}
