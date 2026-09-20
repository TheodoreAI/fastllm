import { useEffect, useState } from 'react'
import { isValidTheme, familyOf } from './themes'

const STORAGE_KEY = 'fastllm-theme'

function systemPrefersLight() {
  return window.matchMedia?.('(prefers-color-scheme: light)').matches ?? false
}

function initialTheme() {
  const stored = localStorage.getItem(STORAGE_KEY)
  if (isValidTheme(stored)) return stored
  return systemPrefersLight() ? 'light' : 'dark'
}

// Manages the app's theme (see themes.js for the full list), persisted to
// localStorage and applied via data-theme (the specific theme) and
// data-theme-family (light/dark, for the syntax-highlighting overrides
// and any other rule that only cares about light-vs-dark rather than the
// exact palette) attributes on the root element — see App.css.
export function useTheme() {
  const [theme, setTheme] = useState(initialTheme)

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
    document.documentElement.setAttribute('data-theme-family', familyOf(theme))
    localStorage.setItem(STORAGE_KEY, theme)
  }, [theme])

  return [theme, setTheme]
}
