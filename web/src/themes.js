// The single source of truth for which themes exist, their display
// labels, and which "family" (light/dark) each belongs to. The family
// matters beyond cosmetics: light-family themes need the light-mode
// syntax-highlighting overrides in App.css (data-theme-family='light'
// selectors), since code blocks always render with highlight.js's fixed
// dark palette regardless of app theme, and that palette only reads
// against a dark backdrop.
export const THEMES = [
  { id: 'dark', label: 'Dark', family: 'dark' },
  { id: 'light', label: 'Light', family: 'light' },
  { id: 'high-contrast', label: 'High Contrast', family: 'dark' },
  { id: 'sepia', label: 'Sepia', family: 'light' },
  { id: 'midnight', label: 'Midnight Blue', family: 'dark' },
  { id: 'ubuntu', label: 'Ubuntu', family: 'dark' },
  { id: 'solarized', label: 'Solarized', family: 'dark' },
  { id: 'nord', label: 'Nord', family: 'dark' },
  { id: 'big-sur', label: 'Big Sur', family: 'light' },
  { id: 'catppuccin', label: 'Catppuccin', family: 'dark' },
  { id: 'dracula', label: 'Dracula', family: 'dark' },
  { id: 'gruvbox', label: 'Gruvbox', family: 'dark' },
  { id: 'tokyo-night', label: 'Tokyo Night', family: 'dark' },
  { id: 'one-dark', label: 'One Dark', family: 'dark' },
  { id: 'monokai', label: 'Monokai', family: 'dark' },
]

const THEME_IDS = THEMES.map((t) => t.id)

export function isValidTheme(id) {
  return THEME_IDS.includes(id)
}

export function familyOf(id) {
  return THEMES.find((t) => t.id === id)?.family ?? 'dark'
}
