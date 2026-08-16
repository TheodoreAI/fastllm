import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-font-family'

// Curated web-safe stacks — no external font loading, grouped like a
// system font picker. Each value is the actual CSS font-family string
// applied to --font-ui.
export const FONT_OPTIONS = [
  {
    group: 'System',
    choices: [
      { label: 'System default', value: "-apple-system, BlinkMacSystemFont, 'SF Pro Text', 'Segoe UI', Ubuntu, Cantarell, system-ui, sans-serif" },
    ],
  },
  {
    group: 'Sans-serif',
    choices: [
      { label: 'Helvetica', value: "'Helvetica Neue', Helvetica, Arial, 'Liberation Sans', sans-serif" },
      { label: 'Verdana', value: "Verdana, Geneva, 'DejaVu Sans', sans-serif" },
      { label: 'Trebuchet MS', value: "'Trebuchet MS', 'Noto Sans', sans-serif" },
      { label: 'Ubuntu', value: "Ubuntu, Cantarell, Arial, sans-serif" },
    ],
  },
  {
    group: 'Serif',
    choices: [
      { label: 'Georgia', value: "Georgia, 'Noto Serif', 'Liberation Serif', 'Times New Roman', serif" },
      { label: 'Times New Roman', value: "'Times New Roman', Times, 'Liberation Serif', serif" },
      { label: 'Iowan / Palatino', value: "'Iowan Old Style', 'Palatino Linotype', Palatino, 'DejaVu Serif', serif" },
    ],
  },
  {
    group: 'Monospace',
    choices: [
      { label: 'SF Mono', value: "ui-monospace, 'SF Mono', Menlo, 'Cascadia Code', Consolas, 'Ubuntu Mono', 'DejaVu Sans Mono', monospace" },
      { label: 'Courier New', value: "'Courier New', Courier, 'Liberation Mono', monospace" },
    ],
  },
  {
    group: 'Rounded',
    choices: [
      { label: 'Comic Sans MS', value: "'Comic Sans MS', 'Comic Sans', 'Chalkboard SE', cursive" },
    ],
  },
]

const DEFAULT_FONT = FONT_OPTIONS[0].choices[0].value

function initialFont() {
  return localStorage.getItem(STORAGE_KEY) || DEFAULT_FONT
}

// Manages the app's UI font family, persisted to localStorage and applied
// by overriding the --font-ui custom property on the root element — mirrors
// useTheme's pattern.
export function useFontFamily() {
  const [font, setFont] = useState(initialFont)

  useEffect(() => {
    document.documentElement.style.setProperty('--font-ui', font)
    localStorage.setItem(STORAGE_KEY, font)
  }, [font])

  return [font, setFont]
}
