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
