import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import rehypeKatex from 'rehype-katex'
import rehypeHighlight from 'rehype-highlight'
import 'katex/dist/katex.min.css'
import 'highlight.js/styles/github-dark.css'

// Recursively pulls the plain text out of a rendered <pre><code>…</code></pre>
// element tree — rehype-highlight wraps tokens in nested <span>s, so the
// copy button can't just read a single child's textContent; it has to
// walk every text node under the block to reconstruct the original code.
function extractText(node) {
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(extractText).join('')
  if (node?.props?.children != null) return extractText(node.props.children)
  return ''
}

function CodeBlock({ children, ...props }) {
  const [copied, setCopied] = useState(false)

  async function handleCopy() {
    try {
      await navigator.clipboard.writeText(extractText(children))
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      // Clipboard access can fail (permissions, insecure context) — the
      // button simply won't flip to "Copied", nothing else to recover.
    }
  }

  return (
    <div className="code-block">
      <button type="button" className="code-copy-btn" onClick={handleCopy}>
        {copied ? 'Copied' : 'Copy'}
      </button>
      <pre {...props}>{children}</pre>
    </div>
  )
}

export default function MessageContent({ content }) {
  return (
    <div className="markdown">
      <ReactMarkdown
        remarkPlugins={[remarkGfm, remarkMath]}
        rehypePlugins={[rehypeKatex, rehypeHighlight]}
        components={{ pre: CodeBlock }}
      >
        {content}
      </ReactMarkdown>
    </div>
  )
}
