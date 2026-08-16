import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-sidebar-order'

function initialOrder(defaultOrder) {
  const stored = localStorage.getItem(STORAGE_KEY)
  if (!stored) return defaultOrder
  try {
    const parsed = JSON.parse(stored)
    if (!Array.isArray(parsed)) return defaultOrder
    // Reconcile against defaultOrder so a code change adding/removing a
    // section doesn't leave a stale/missing key stuck in localStorage.
    const known = parsed.filter((key) => defaultOrder.includes(key))
    const missing = defaultOrder.filter((key) => !known.includes(key))
    return [...known, ...missing]
  } catch {
    return defaultOrder
  }
}

// Manages a persisted, user-reorderable list of section keys (sidebar
// panels), backed by localStorage like useTheme.
export function useSectionOrder(defaultOrder) {
  const [order, setOrder] = useState(() => initialOrder(defaultOrder))

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(order))
  }, [order])

  function moveSection(key, toIndex) {
    setOrder((prev) => {
      const fromIndex = prev.indexOf(key)
      if (fromIndex === -1 || fromIndex === toIndex) return prev
      const next = [...prev]
      next.splice(fromIndex, 1)
      next.splice(toIndex, 0, key)
      return next
    })
  }

  return [order, moveSection]
}
