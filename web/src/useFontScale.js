import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-font-scale'
const DEFAULT_SCALE = 1

export const FONT_SCALE_OPTIONS = [
  { label: 'Small', value: 0.9 },
  { label: 'Medium', value: 1 },
  { label: 'Large', value: 1.15 },
  { label: 'Extra large', value: 1.3 },
]

function initialScale() {
  const stored = Number(localStorage.getItem(STORAGE_KEY))
  return FONT_SCALE_OPTIONS.some((opt) => opt.value === stored) ? stored : DEFAULT_SCALE
}

// Manages the app's UI text-size scale, persisted to localStorage and
// applied by overriding the --font-scale custom property on the root
// element (see App.css: html { font-size: calc(16px * var(--font-scale)) }).
// Mirrors useTheme/useFontFamily's pattern.
export function useFontScale() {
  const [scale, setScale] = useState(initialScale)

  useEffect(() => {
    document.documentElement.style.setProperty('--font-scale', String(scale))
    localStorage.setItem(STORAGE_KEY, String(scale))
  }, [scale])

  return [scale, setScale]
}
