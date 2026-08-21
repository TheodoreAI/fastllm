// CodeMirror extension factories for gopls-backed Go language features —
// the frontend half of internal/lsp. Each is built fresh per open tab
// (same reasoning EditorView.jsx's oxlintExtensionFor/aiCompletionExtension
// already use for their own per-path extensions): every open .go tab
// needs its own closures over its own path, not one shared instance.
import { linter } from '@codemirror/lint'
import { autocompletion } from '@codemirror/autocomplete'
import { hoverTooltip, keymap, tooltips, EditorView as CMView } from '@codemirror/view'
import { Prec } from '@codemirror/state'
import { uriToPath } from './lsp'

// gopls positions are 0-indexed {line, character}; CodeMirror positions
// are a single doc offset, 1-indexed lines internally. This is the one
// conversion point every extension below needs in one direction or the
// other.
function offsetToPosition(doc, offset) {
  const line = doc.lineAt(offset)
  return { line: line.number - 1, character: offset - line.from }
}

// Diagnostics (and, less often, other responses) can reference a
// position computed against a document version slightly newer or older
// than what CodeMirror currently holds — gopls's replies are inherently
// a little behind live typing. Clamping both line and character to the
// document's actual bounds is required, not just cosmetic: an
// unclamped character past the end of its line produces an offset past
// doc.length, which throws inside CodeMirror's own position lookups
// (RangeError: Invalid position) and was observed to silently break
// unrelated in-flight UI (the autocomplete popup) in the same update
// cycle — never assume a server-supplied position is still valid
// against the current buffer.
function positionToOffset(doc, position) {
  const line = doc.line(Math.min(Math.max(position.line + 1, 1), doc.lines))
  const character = Math.min(Math.max(position.character, 0), line.length)
  return line.from + character
}

// severityFor collapses gopls's DiagnosticSeverity (1=Error, 2=Warning,
// 3=Information, 4=Hint) down to what @codemirror/lint's severity prop
// accepts ("error"/"warning"/"info"). A 4-way collapse, deliberately
// different from internal/lint.normalizeSeverity's 2-way one for oxlint:
// gopls's Hint/Information severities are common enough (e.g. "this
// import could be removed") that flattening them to "warning" would
// overstate them.
function severityFor(n) {
  return { 1: 'error', 2: 'warning', 3: 'info', 4: 'info' }[n] ?? 'warning'
}

// lspLintExtensionFor is a sibling to EditorView.jsx's oxlintExtensionFor,
// not a merge into it — no file is ever both a JS/JSX file (oxlint) and a
// Go file (gopls), so there's nothing to actually combine. Multiple
// linter() sources can coexist in one extensions array; CodeMirror merges
// their output in the gutter/panel on its own. No lintGutter() call here
// — oxlintExtensionFor already contributes one for every file including
// .go ones (it's just always empty there), so adding a second would be
// redundant.
export function lspLintExtensionFor(path, lspDiagnosticsByPath) {
  return [
    linter((view) =>
      (lspDiagnosticsByPath.current[path] ?? []).map((d) => ({
        from: positionToOffset(view.state.doc, d.range.start),
        to: positionToOffset(view.state.doc, d.range.end),
        severity: severityFor(d.severity),
        message: d.message,
        source: 'gopls',
      }))
    ),
  ]
}

function kindToType(kind) {
  // LSP CompletionItemKind (1-25) -> @codemirror/autocomplete's `type`
  // string, which only drives which icon the popup shows — an
  // unrecognized/missing kind just gets no icon, not a broken entry.
  const KIND_TYPE = {
    2: 'method',
    3: 'function',
    4: 'constructor',
    5: 'field',
    6: 'variable',
    7: 'class',
    8: 'interface',
    9: 'module',
    10: 'property',
    13: 'enum',
    14: 'keyword',
    21: 'constant',
    22: 'type',
  }
  return KIND_TYPE[kind]
}

// lspCompletionExtensionFor wires gopls's real symbol completion into
// @codemirror/autocomplete's autocompletion() — installed since day one
// but unused anywhere else in this codebase (aiCompletion.js deliberately
// avoids its popup UI for ghost-text continuation; a symbol list is
// exactly what that popup UI is for instead). Coexists with ghost text:
// they're unrelated CodeMirror mechanisms (a Facet-based override here vs.
// aiCompletionExtension's StateField + Decoration.widget), so both can be
// present in the same extensions array without fighting over Tab.
//
// sourceCache (a per-path ref owned by EditorView.jsx, mirroring
// lintDiagnosticsByPath/codeMirrorViewsByPath's pattern) is required, not
// an optimization: @codemirror/autocomplete's CompletionState.update()
// correlates an in-flight query with the current state by *reference
// equality of the source function itself* (`this.active.find(s =>
// s.source == source)`). languageExtensionsFor is called fresh on every
// render (every keystroke, since content lives in React state), so a
// source function built inline here would get a new closure identity
// each time — any completion request still awaiting gopls's response
// when the next render happens becomes unmatchable against the new
// state's source list and gopls's real answer gets silently dropped,
// which is exactly what was observed end-to-end (request sent, gopls
// replied with real completions, popup never appeared) before this was
// diagnosed. Caching the same function reference per path keeps the
// source's identity stable across renders so in-flight queries survive.
export function lspCompletionExtensionFor(path, lspClientRef, flushLspChange, sourceCache) {
  if (!sourceCache.current[path]) {
    sourceCache.current[path] = async (context) => {
      const client = lspClientRef.current
      if (!client) return null
      flushLspChange(path, context.view)
      const { line, character } = offsetToPosition(context.state.doc, context.pos)
      let result
      try {
        result = await client.completion(path, line, character)
      } catch {
        return null
      }
      if (!result) return null
      const items = Array.isArray(result) ? result : (result.items ?? [])
      if (items.length === 0) return null
      // The replacement range must come from gopls's own textEdit, not a
      // regex guess at word boundaries: CodeMirror filters every option's
      // label against state.sliceDoc(from, cursor), so a `from` that
      // includes a package qualifier (e.g. "strings." before the typed
      // "S") filters out every bare label ("Split", "Builder", ...) since
      // none of them start with "strings.S" — the completion source runs
      // and gopls answers correctly, but the popup silently renders
      // nothing. gopls reports the same range for every item in one
      // response, so the first item's is representative.
      const from = items[0].textEdit
        ? positionToOffset(context.state.doc, items[0].textEdit.range.start)
        : context.pos
      return {
        from,
        options: items.map((item) => ({
          label: item.label,
          type: kindToType(item.kind),
          detail: item.detail,
          info: typeof item.documentation === 'string' ? item.documentation : item.documentation?.value,
          apply: item.insertText ?? item.label,
        })),
      }
    }
  }
  return autocompletion({ override: [sourceCache.current[path]] })
}

// gopls's hover response shape depends on what contentFormat the client
// declared (see internal/lsp/handler.go's initializeParams, which offers
// both): sometimes a ```go fenced signature followed by a "---" rule and
// the doc as markdown paragraphs, sometimes (observed with this
// environment's gopls) plain text — just the signature as the first
// line, a blank line, then the doc comment verbatim. Splitting the
// signature out either way lets it render in its own monospace block
// instead of being flattened into the doc paragraph's plain text — a
// plain textContent dump of the whole string reads as one run-on
// sentence with no visual distinction between "what this is" and "how
// it's documented".
function parseHoverMarkdown(text) {
  const fence = text.match(/```go\r?\n([\s\S]*?)```/)
  if (fence) {
    const doc = text
      .slice(fence.index + fence[0].length)
      .replace(/^\s*---\s*/, '')
      .trim()
    return { signature: fence[1].trim(), doc }
  }
  const firstLineEnd = text.indexOf('\n')
  const firstLine = (firstLineEnd === -1 ? text : text.slice(0, firstLineEnd)).trim()
  if (/^(func|type|var|const|package)\b/.test(firstLine)) {
    return { signature: firstLine, doc: text.slice(firstLineEnd + 1).trim() }
  }
  return { signature: null, doc: text.trim() }
}

// lspHoverExtensionFor shows gopls's textDocument/hover result (a
// symbol's doc comment/signature) in a tooltip — hoverTooltip comes from
// @codemirror/view, already a dependency, no new package needed.
export function lspHoverExtensionFor(path, lspClientRef, flushLspChange) {
  return [
    // The Editor pane's columns clip overflow for scrolling — without
    // this, the tooltip renders as a child of that clipped DOM subtree
    // and gets visually cut off at the column edge instead of floating
    // over whatever's next to it, same as any other editor's hover card.
    tooltips({ parent: document.body }),
    hoverTooltip(async (view, pos) => {
    const client = lspClientRef.current
    if (!client) return null
    flushLspChange(path, view)
    const { line, character } = offsetToPosition(view.state.doc, pos)
    let result
    try {
      result = await client.hover(path, line, character)
    } catch {
      return null
    }
    if (!result?.contents) return null
    const contents = result.contents
    const text = typeof contents === 'string' ? contents : (contents.value ?? '')
    if (!text) return null
    return {
      pos,
      end: pos,
      above: true,
      create() {
        const dom = document.createElement('div')
        dom.className = 'cm-lsp-hover'
        const { signature, doc } = parseHoverMarkdown(text)
        if (signature) {
          const sig = document.createElement('div')
          sig.className = 'cm-lsp-hover-sig'
          sig.textContent = signature
          dom.appendChild(sig)
        }
        if (doc) {
          const docEl = document.createElement('div')
          docEl.className = 'cm-lsp-hover-doc'
          docEl.textContent = doc
          dom.appendChild(docEl)
        }
        if (!signature && !doc) dom.textContent = text
        return { dom }
      },
    }
    }),
  ]
}

// lspDefinitionExtensionFor wires go-to-definition to F12 (VS Code muscle
// memory — free in fastllm's own bindings; Mod-S is Save, Mod-P is
// QuickOpen) and Ctrl/Cmd+Click. onJumpTo(path, line, character) is
// EditorView.jsx's jumpToDefinition: it opens/activates the target tab
// (same-file jumps just move the cursor there) and scrolls to the
// position.
export function lspDefinitionExtensionFor(path, lspClientRef, onJumpTo, flushLspChange) {
  async function triggerDefinition(view, pos) {
    const client = lspClientRef.current
    if (!client) return
    flushLspChange(path, view)
    const { line, character } = offsetToPosition(view.state.doc, pos)
    let result
    try {
      result = await client.definition(path, line, character)
    } catch {
      return
    }
    const loc = Array.isArray(result) ? result[0] : result
    if (!loc) return
    onJumpTo(uriToPath(client.root, loc.uri), loc.range.start.line, loc.range.start.character)
  }

  return [
    Prec.highest(
      keymap.of([
        {
          key: 'F12',
          run: (view) => {
            triggerDefinition(view, view.state.selection.main.head)
            return true
          },
        },
      ])
    ),
    CMView.domEventHandlers({
      mousedown(event, view) {
        if (!(event.ctrlKey || event.metaKey)) return false
        const pos = view.posAtCoords({ x: event.clientX, y: event.clientY })
        if (pos == null) return false
        triggerDefinition(view, pos)
        return true
      },
    }),
  ]
}

export { offsetToPosition, positionToOffset }
