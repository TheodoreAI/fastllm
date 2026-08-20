import { javascript } from '@codemirror/lang-javascript'
import { python } from '@codemirror/lang-python'
import { markdown } from '@codemirror/lang-markdown'
import { css } from '@codemirror/lang-css'
import { html } from '@codemirror/lang-html'
import { json } from '@codemirror/lang-json'
import { go } from '@codemirror/lang-go'
import { cpp } from '@codemirror/lang-cpp'
import { rust } from '@codemirror/lang-rust'
import { yaml } from '@codemirror/lang-yaml'
import { sql } from '@codemirror/lang-sql'
import { xml } from '@codemirror/lang-xml'

// Maps a file extension to the CodeMirror language extension to load for
// it. jsx()/tsx() aren't separate CodeMirror packages (same situation as
// highlight.js — see the light-theme syntax-highlighting fix elsewhere
// in this app): @codemirror/lang-javascript's javascript() factory takes
// jsx/typescript flags instead.
const EXTENSION_MAP = {
  js: () => javascript({ jsx: true }),
  jsx: () => javascript({ jsx: true }),
  mjs: () => javascript(),
  cjs: () => javascript(),
  ts: () => javascript({ typescript: true }),
  tsx: () => javascript({ jsx: true, typescript: true }),
  py: () => python(),
  md: () => markdown(),
  markdown: () => markdown(),
  mdx: () => markdown(),
  css: () => css(),
  html: () => html(),
  htm: () => html(),
  // No dedicated @codemirror/lang-vue package exists — a .vue Single-File
  // Component is HTML-shaped (<template>/<script>/<style> blocks), and
  // html()'s default config already nests javascript()/css() parsing
  // inside <script>/<style> tags, which covers the common case well
  // enough without pulling in a heavier Vue-specific parser.
  vue: () => html(),
  json: () => json(),
  go: () => go(),
  c: () => cpp(),
  h: () => cpp(),
  cc: () => cpp(),
  cpp: () => cpp(),
  hpp: () => cpp(),
  rs: () => rust(),
  yaml: () => yaml(),
  yml: () => yaml(),
  sql: () => sql(),
  xml: () => xml(),
  svg: () => xml(),
}

// languageExtensionFor returns the CodeMirror language extension array
// for a file path, based on its extension — [] (no language support,
// still editable as plain text) for anything unrecognized.
export function languageExtensionFor(path) {
  const ext = path.split('.').pop()?.toLowerCase()
  const factory = ext ? EXTENSION_MAP[ext] : undefined
  return factory ? [factory()] : []
}

// LANGUAGE_NAMES maps an extension to a human-readable label — used only
// to tell the AI completion backend (see aiCompletion.js) what language
// it's completing, not by CodeMirror itself. Deliberately a separate,
// smaller map from EXTENSION_MAP rather than deriving a name from the
// CodeMirror language object: EXTENSION_MAP's factories return configured
// LanguageSupport instances (e.g. javascript({jsx:true})), not a plain
// name, and reverse-deriving "TypeScript" from that object is more
// fragile than just writing the label once here.
const LANGUAGE_NAMES = {
  js: 'JavaScript',
  jsx: 'JavaScript (JSX)',
  mjs: 'JavaScript',
  cjs: 'JavaScript',
  ts: 'TypeScript',
  tsx: 'TypeScript (TSX)',
  py: 'Python',
  md: 'Markdown',
  markdown: 'Markdown',
  mdx: 'Markdown',
  css: 'CSS',
  html: 'HTML',
  htm: 'HTML',
  vue: 'Vue',
  json: 'JSON',
  go: 'Go',
  c: 'C',
  h: 'C',
  cc: 'C++',
  cpp: 'C++',
  hpp: 'C++',
  rs: 'Rust',
  yaml: 'YAML',
  yml: 'YAML',
  sql: 'SQL',
  xml: 'XML',
  svg: 'XML',
}

// languageNameFor returns a human-readable language name for a file
// path, or '' for an unrecognized extension.
export function languageNameFor(path) {
  const ext = path.split('.').pop()?.toLowerCase()
  return (ext && LANGUAGE_NAMES[ext]) || ''
}
