// Minimal subsequence fuzzy matcher for QuickOpen/CommandPalette — good
// enough for a few hundred file paths or a dozen commands typed a
// character at a time; not meant to compete with a real fuzzy-finder
// library at larger scale.

// Returns a score (higher is better) if every character of `query` appears
// in `target` in order (case-insensitive), or null if it doesn't match at
// all. Consecutive matched characters and matches right after a path
// separator or a camelCase boundary score higher, so typing "eview" ranks
// EditorView.jsx above a path where those letters just happen to appear
// scattered apart.
export function fuzzyScore(query, target) {
  if (!query) return 0
  const q = query.toLowerCase()
  const t = target.toLowerCase()

  let qi = 0
  let score = 0
  let prevMatchedIndex = -2
  for (let ti = 0; ti < t.length && qi < q.length; ti++) {
    if (t[ti] !== q[qi]) continue
    let charScore = 1
    if (ti === prevMatchedIndex + 1) charScore += 3 // consecutive run
    const prevChar = target[ti - 1]
    const boundary = ti === 0 || prevChar === '/' || (/[a-z]/.test(prevChar) && /[A-Z]/.test(target[ti]))
    if (boundary) charScore += 2
    score += charScore
    prevMatchedIndex = ti
    qi++
  }
  if (qi < q.length) return null // not every query character was found in order

  // Reward shorter targets slightly, so an exact short match beats a long
  // path that happens to also contain the subsequence.
  return score - target.length * 0.01
}

// Filters and ranks `items` by fuzzyScore against getText(item), best match
// first. Items that don't match at all are dropped. An empty query returns
// items unranked, in their original order.
export function fuzzyFilter(query, items, getText) {
  if (!query) return items
  const scored = []
  for (const item of items) {
    const score = fuzzyScore(query, getText(item))
    if (score !== null) scored.push({ item, score })
  }
  scored.sort((a, b) => b.score - a.score)
  return scored.map((s) => s.item)
}
