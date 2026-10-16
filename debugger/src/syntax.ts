/** Syntax highlighting for the storage file preview (highlight.js, core build
 *  with explicitly registered languages — no auto-detection: a mis-detected
 *  grammar looks worse than plain text, and the preview must stay
 *  deterministic). */

import hljs from 'highlight.js/lib/core'
import bash from 'highlight.js/lib/languages/bash'
import c from 'highlight.js/lib/languages/c'
import cpp from 'highlight.js/lib/languages/cpp'
import css from 'highlight.js/lib/languages/css'
import dockerfile from 'highlight.js/lib/languages/dockerfile'
import go from 'highlight.js/lib/languages/go'
import ini from 'highlight.js/lib/languages/ini'
import java from 'highlight.js/lib/languages/java'
import javascript from 'highlight.js/lib/languages/javascript'
import json from 'highlight.js/lib/languages/json'
import markdown from 'highlight.js/lib/languages/markdown'
import php from 'highlight.js/lib/languages/php'
import protobuf from 'highlight.js/lib/languages/protobuf'
import python from 'highlight.js/lib/languages/python'
import ruby from 'highlight.js/lib/languages/ruby'
import rust from 'highlight.js/lib/languages/rust'
import sql from 'highlight.js/lib/languages/sql'
import typescript from 'highlight.js/lib/languages/typescript'
import xml from 'highlight.js/lib/languages/xml'
import yaml from 'highlight.js/lib/languages/yaml'

const LANGUAGES: Record<string, Parameters<typeof hljs.registerLanguage>[1]> = {
  bash, c, cpp, css, dockerfile, go, ini, java, javascript, json,
  markdown, php, protobuf, python, ruby, rust, sql, typescript, xml, yaml,
}
for (const [name, grammar] of Object.entries(LANGUAGES)) {
  hljs.registerLanguage(name, grammar)
}

/** Extension → highlight.js language. TOML/INI-style configs share the ini
 *  grammar; html/svg route to xml. */
const EXT_LANGUAGE: Record<string, string> = {
  go: 'go',
  ts: 'typescript', tsx: 'typescript', mts: 'typescript', cts: 'typescript',
  js: 'javascript', jsx: 'javascript', mjs: 'javascript', cjs: 'javascript',
  py: 'python',
  sh: 'bash', bash: 'bash', zsh: 'bash',
  json: 'json',
  yaml: 'yaml', yml: 'yaml',
  toml: 'ini', ini: 'ini', conf: 'ini', cfg: 'ini', env: 'ini',
  sql: 'sql',
  css: 'css',
  html: 'xml', xml: 'xml', svg: 'xml', xsd: 'xml', xsl: 'xml',
  dockerfile: 'dockerfile',
  rs: 'rust',
  java: 'java',
  c: 'c', h: 'c',
  cpp: 'cpp', cc: 'cpp', cxx: 'cpp', hpp: 'cpp',
  rb: 'ruby',
  php: 'php',
  proto: 'protobuf',
  md: 'markdown', markdown: 'markdown',
}

/** Language for a file name, or '' when the extension is unknown. */
export function languageForFile(name: string): string {
  const base = name.toLowerCase()
  if (base === 'dockerfile' || base.startsWith('dockerfile.')) return 'dockerfile'
  const ext = base.slice(base.lastIndexOf('.') + 1)
  return EXT_LANGUAGE[ext] ?? ''
}

function escapeHtml(text: string): string {
  return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

/** Highlighted HTML for a code string (hljs output escapes the source
 *  itself). Unknown languages return escaped plain text — never a wrong
 *  grammar. */
export function highlightCode(code: string, language: string): string {
  if (!language || !hljs.getLanguage(language)) return escapeHtml(code)
  try {
    return hljs.highlight(code, { language, ignoreIllegals: true }).value
  } catch {
    return escapeHtml(code)
  }
}
