import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-pane-grid-sizes'
const DEFAULT_SIZES = { leftWidth: 480, topHeight: 320, bottomHeight: 320 }

function initialSizes() {
  const stored = localStorage.getItem(STORAGE_KEY)
  if (!stored) return DEFAULT_SIZES
  try {
    const parsed = JSON.parse(stored)
    if (!parsed || typeof parsed !== 'object') return DEFAULT_SIZES
    return { ...DEFAULT_SIZES, ...parsed }
  } catch {
    return DEFAULT_SIZES
  }
}

// The draggable splits in the Editor/Terminal/Chat grid (see
// usePaneSlots): leftWidth is the left column vs. the right (full-height)
// column, and topHeight is the top-left slot vs. the bottom-left slot
// within that left column. bottomHeight is only used when the bottom-left
// slot is alone in the column (its sibling collapsed) — its own fixed
// height, anchored to the bottom, rather than stretching to fill the
// freed space. Sized and persisted per SLOT, independent of which pane
// currently occupies it — dragging a pane into a different slot takes on
// that slot's remembered size, not the size the pane itself used to have.
export function usePaneGridSizes() {
  const [sizes, setSizes] = useState(initialSizes)

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(sizes))
  }, [sizes])

  function setLeftWidth(width) {
    setSizes((prev) => ({ ...prev, leftWidth: width }))
  }
  function setTopHeight(height) {
    setSizes((prev) => ({ ...prev, topHeight: height }))
  }
  function setBottomHeight(height) {
    setSizes((prev) => ({ ...prev, bottomHeight: height }))
  }

  return [sizes, setLeftWidth, setTopHeight, setBottomHeight]
}
