// Parses a unified diff (as returned by `git diff`, see EditorGitDiff)
// into per-line records the diff view can style — added/removed/context
// rows with real +/- gutter coloring, instead of dumping raw diff text
// into a plain <pre>. Deliberately simple line-classification (no actual
// diff algorithm) since the backend already produced a unified diff;
// this only has to read git's own format back out.
export function parseDiff(diffText) {
  if (!diffText) return []
  const lines = diffText.split('\n')
  // A trailing empty string from a final '\n' isn't a real line.
  if (lines.length > 0 && lines[lines.length - 1] === '') lines.pop()

  return lines.map((line) => {
    if (line.startsWith('+++') || line.startsWith('---')) {
      return { type: 'file', text: line }
    }
    if (line.startsWith('diff --git') || line.startsWith('index ')) {
      return { type: 'meta', text: line }
    }
    if (line.startsWith('@@')) {
      return { type: 'hunk', text: line }
    }
    if (line.startsWith('+')) {
      return { type: 'add', text: line.slice(1) }
    }
    if (line.startsWith('-')) {
      return { type: 'del', text: line.slice(1) }
    }
    if (line.startsWith(' ')) {
      return { type: 'context', text: line.slice(1) }
    }
    return { type: 'context', text: line }
  })
}
