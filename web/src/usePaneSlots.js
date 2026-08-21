import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-pane-slots'
const SLOTS = ['top-left', 'bottom-left', 'right']

function initialSlots(defaultAssignment) {
  const stored = localStorage.getItem(STORAGE_KEY)
  if (!stored) return defaultAssignment
  try {
    const parsed = JSON.parse(stored)
    if (!parsed || typeof parsed !== 'object') return defaultAssignment
    const assignedPanes = SLOTS.map((s) => parsed[s])
    const defaultPanes = SLOTS.map((s) => defaultAssignment[s])
    // Only trust a stored assignment if it's some permutation of the same
    // pane set the code currently expects — otherwise (a code change
    // added/removed a pane) fall back to the default rather than risk a
    // slot with no pane, or two slots sharing one.
    const isValidPermutation =
      assignedPanes.every((p) => defaultPanes.includes(p)) && new Set(assignedPanes).size === SLOTS.length
    return isValidPermutation ? { ...defaultAssignment, ...parsed } : defaultAssignment
  } catch {
    return defaultAssignment
  }
}

// Manages which pane ('editor' | 'terminal' | 'chat') occupies which of the
// three fixed grid slots — top-left, bottom-left, and right (full height) —
// see App.jsx's renderPanes. Unlike the old linear pane order, the grid's
// shape itself never changes; dragging a pane onto another slot just swaps
// their assignments (see swapPanes below).
export function usePaneSlots(defaultAssignment) {
  const [slots, setSlots] = useState(() => initialSlots(defaultAssignment))

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(slots))
  }, [slots])

  function swapPanes(paneA, paneB) {
    setSlots((prev) => {
      const slotA = SLOTS.find((s) => prev[s] === paneA)
      const slotB = SLOTS.find((s) => prev[s] === paneB)
      if (!slotA || !slotB || slotA === slotB) return prev
      return { ...prev, [slotA]: paneB, [slotB]: paneA }
    })
  }

  return [slots, swapPanes]
}
