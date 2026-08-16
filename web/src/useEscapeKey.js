import { useEffect } from 'react'

// Calls onEscape when the user presses Escape — the standard keyboard
// equivalent to clicking a modal's backdrop to close it.
export function useEscapeKey(onEscape) {
  useEffect(() => {
    function handleKeyDown(e) {
      if (e.key === 'Escape') onEscape()
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [onEscape])
}
