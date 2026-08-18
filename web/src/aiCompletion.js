// Inline "ghost text" AI code completion for the Editor tab — the
// CodeMirror side of internal/chat.EditorComplete. Modeled on how
// Copilot/Cursor render an inline suggestion: gray text appended after
// the cursor, accepted with Tab, dismissed by Escape or by continuing to
// type/move the cursor. Always asks the LOCAL model (see that handler's
// doc comment for why), so this never costs a cloud API call.
//
// Built by hand with @codemirror/view primitives (Decoration.widget +
// a StateField for "what's the current suggestion") rather than
// @codemirror/autocomplete's completion popup, since a dropdown list of
// alternatives is the wrong UI for "one continuation of the code" —
// that's what makes this feel like Copilot instead of a symbol picker.
import { EditorView, Decoration, WidgetType, keymap, ViewPlugin } from '@codemirror/view'
import { StateField, StateEffect, Prec } from '@codemirror/state'
import { fetchEditorCompletion } from './api'

const DEBOUNCE_MS = 400

const setSuggestion = StateEffect.define()

class GhostTextWidget extends WidgetType {
  constructor(text) {
    super()
    this.text = text
  }
  eq(other) {
    return other.text === this.text
  }
  toDOM() {
    const span = document.createElement('span')
    span.className = 'ai-ghost-text'
    span.textContent = this.text
    return span
  }
  ignoreEvent() {
    return true
  }
}

// Holds the current suggestion (text + the position it applies to) as
// editor state, so the widget can be positioned via a normal
// Decoration.widget and cleared declaratively (any document change or an
// explicit clear effect wipes it) instead of being tracked as
// component-external mutable state that could drift out of sync with
// what's actually rendered.
const suggestionField = StateField.define({
  create() {
    return null
  },
  update(value, tr) {
    for (const effect of tr.effects) {
      if (effect.is(setSuggestion)) return effect.value
    }
    // Any document change (typing, accepting, external content reset)
    // invalidates whatever suggestion was showing — a stale ghost text
    // continuing to display after the buffer moved under it would show
    // a continuation of code that no longer matches what's actually there.
    if (tr.docChanged) return null
    return value
  },
  provide: (field) =>
    EditorView.decorations.from(field, (suggestion) => {
      if (!suggestion) return Decoration.none
      return Decoration.set([
        Decoration.widget({ widget: new GhostTextWidget(suggestion.text), side: 1 }).range(suggestion.pos),
      ])
    }),
})

function currentSuggestion(state) {
  return state.field(suggestionField, false) ?? null
}

// acceptCompletion inserts the current suggestion's text at the cursor
// and clears it — bound to Tab, but only takes over Tab's default
// behavior (indent) when there's actually a suggestion showing.
function acceptCompletion(view) {
  const suggestion = currentSuggestion(view.state)
  if (!suggestion) return false
  view.dispatch({
    changes: { from: suggestion.pos, insert: suggestion.text },
    selection: { anchor: suggestion.pos + suggestion.text.length },
    effects: setSuggestion.of(null),
  })
  return true
}

function dismissCompletion(view) {
  if (!currentSuggestion(view.state)) return false
  view.dispatch({ effects: setSuggestion.of(null) })
  return true
}

// requestCompletion fetches a suggestion for the cursor position captured
// at call time (`pos`) and only applies it if the view's cursor is still
// exactly there when the response arrives — otherwise the user has since
// moved on and an inline suggestion appearing somewhere they're no longer
// looking would be actively confusing, not helpful.
function requestCompletion(view, language) {
  const pos = view.state.selection.main.head
  const doc = view.state.doc
  const prefix = doc.sliceString(0, pos)
  const suffix = doc.sliceString(pos)
  const controller = new AbortController()

  fetchEditorCompletion(prefix, suffix, language, controller.signal)
    .then((text) => {
      const trimmed = text.replace(/\s+$/, '')
      if (!trimmed) return
      if (view.state.selection.main.head !== pos) return // cursor moved — stale
      view.dispatch({ effects: setSuggestion.of({ pos, text: trimmed }) })
    })
    .catch(() => {
      // A failed/aborted completion request just means no ghost text
      // appears — same as any IDE's autocomplete silently having nothing
      // to offer, never surfaced as an editor error.
    })

  return controller
}

// aiCompletionExtension returns the CodeMirror extensions that wire up
// ghost-text completion for one open file. language is a human-readable
// label (see editorLanguages.js's languageNameFor) sent to the backend so
// the model knows what it's completing; enabled lets the caller gate the
// whole feature off (e.g. file access disabled) without conditionally
// including/excluding extensions in the surrounding extensions array.
export function aiCompletionExtension(language, enabled) {
  if (!enabled) return []

  let debounceTimer = null
  let inFlight = null

  const trigger = ViewPlugin.fromClass(
    class {
      update(update) {
        if (!update.docChanged && !update.selectionSet) return
        if (debounceTimer) clearTimeout(debounceTimer)
        if (inFlight) inFlight.abort()
        // A document change already clears any stale suggestion via
        // suggestionField's update() above; a plain cursor move (no doc
        // change) should also drop it rather than leave ghost text
        // floating at a position the cursor no longer points at.
        if (!update.docChanged && currentSuggestion(update.state)) {
          update.view.dispatch({ effects: setSuggestion.of(null) })
        }
        const view = update.view
        debounceTimer = setTimeout(() => {
          inFlight = requestCompletion(view, language)
        }, DEBOUNCE_MS)
      }
      destroy() {
        if (debounceTimer) clearTimeout(debounceTimer)
        if (inFlight) inFlight.abort()
      }
    }
  )

  // Prec.highest so Tab is intercepted before @uiw/react-codemirror's
  // default indentWithTab keymap — run() only returns true (consuming
  // the key) when there's actually a suggestion showing, so normal
  // Tab-to-indent behavior is completely unaffected the rest of the time.
  const acceptKeymap = Prec.highest(
    keymap.of([
      { key: 'Tab', run: acceptCompletion },
      { key: 'Escape', run: dismissCompletion },
    ])
  )

  return [suggestionField, trigger, acceptKeymap]
}
