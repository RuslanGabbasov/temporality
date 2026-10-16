#!/usr/bin/env node
// Builds the user documentation site: docs/user/*.md → pages/docs/*.html.
//
// - Sources carry YAML-ish front matter (title, order, description).
// - Output is committed, so GitHub Pages serves it without any CI.
// - The chrome reuses the landing's palette and fonts (pages/styles.css)
//   plus a docs-specific stylesheet (pages/docs/docs.css).
//
// Usage: node scripts/build-docs.mjs   (from the repository root)

import { marked } from 'marked'
import { readdirSync, readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs'
import { join, basename } from 'node:path'

const ROOT = new URL('..', import.meta.url).pathname
const SRC = join(ROOT, 'docs', 'user')
const OUT = join(ROOT, 'pages', 'docs')

// ── front matter ────────────────────────────────────────────────────────────

function parseFrontMatter(source) {
  if (!source.startsWith('---')) return { meta: {}, body: source }
  const end = source.indexOf('---', 3)
  if (end < 0) return { meta: {}, body: source }
  const meta = {}
  for (const line of source.slice(3, end).split('\n')) {
    const match = line.match(/^(\w[\w-]*):\s*(.*)$/)
    if (match) meta[match[1]] = match[2].trim().replace(/^["']|["']$/g, '')
  }
  return { meta, body: source.slice(end + 3).replace(/^\n+/, '') }
}

// ── markdown → html ─────────────────────────────────────────────────────────

const renderer = new marked.Renderer({ gfm: true })
renderer.heading = function ({ tokens, depth }) {
  const text = this.parser.parseInline(tokens)
  const anchor = text.toLowerCase()
    .replace(/<[^>]+>/g, '')
    .replace(/[^\p{L}\p{N}\s-]/gu, '')
    .trim()
    .replace(/\s+/g, '-')
  return `<h${depth} id="${anchor}"><a class="anchor" href="#${anchor}" aria-label="Ссылка на раздел">#</a>${text}</h${depth}>`
}
renderer.link = function ({ href, tokens }) {
  const text = this.parser.parseInline(tokens)
  const external = /^[a-z]+:/i.test(href)
  return `<a href="${href}"${external ? ' target="_blank" rel="noreferrer"' : ''}>${text}${external ? '&nbsp;<span class="ext">↗</span>' : ''}</a>`
}
marked.use({ renderer, gfm: true, breaks: false })

function renderMarkdown(body) {
  return marked.parse(body)
}

// ── load pages ──────────────────────────────────────────────────────────────

if (!existsSync(SRC)) {
  console.error(`docs sources not found: ${SRC}`)
  process.exit(1)
}
const files = readdirSync(SRC).filter((f) => f.endsWith('.md')).sort()
const pages = files.map((file) => {
  const { meta, body } = parseFrontMatter(readFileSync(join(SRC, file), 'utf8'))
  // The leading `# Title` duplicates front-matter title and the page h1.
  const stripped = body.replace(/^#\s+.+\n+/, '')
  return {
    slug: basename(file, '.md'),
    order: Number(meta.order ?? 999),
    section: meta.section ?? 'Прочее',
    title: meta.title ?? basename(file),
    description: meta.description ?? '',
    body: stripped,
  }
}).sort((a, b) => a.order - b.order)

const sections = []
for (const page of pages) {
  const last = sections[sections.length - 1]
  if (last && last.name === page.section) last.pages.push(page)
  else sections.push({ name: page.section, pages: [page] })
}

// ── chrome ──────────────────────────────────────────────────────────────────

const BRAND_SVG = `<a class="brand" href="../" aria-label="Temporality home"><img src="../assets/temporality-mark.svg" alt=""/><span>temporality</span></a>`

function topbar(activeSlug) {
  const docsLink = `<a class="nav-docs active" href="/docs/">Документация</a>`
  const nav = [`<a href="../#product">Продукт</a>`, `<a href="../#guide">Руководство</a>`, `<a href="../#architecture">Архитектура</a>`].join('')
  const github = `https://github.com/RuslanGabbasov/temporality`
  return `<header class="topbar">${BRAND_SVG}<nav class="nav">${docsLink}${nav}</nav><div class="top-actions"><a class="github-link" href="${github}/blob/main/docs/user/${activeSlug}.md" target="_blank" rel="noreferrer"><span>Изменить страницу</span><span class="arrow">↗</span></a></div></header>`
}

function sidebar(activeSlug) {
  const items = sections.map((section) => {
    const rows = section.pages.map((page) =>
      `<a class="docs-nav-row${page.slug === activeSlug ? ' active' : ''}" href="${page.slug}.html">${page.title}</a>`).join('\n')
    return `<div class="docs-nav-section">${section.name}</div>\n${rows}`
  }).join('\n')
  return `<aside class="docs-sidebar"><nav>${items}</nav></aside>`
}

function pageHtml(page) {
  const index = pages.indexOf(page)
  const prev = pages[index - 1]
  const next = pages[index + 1]
  const pager = `
  <nav class="docs-pager">
    ${prev ? `<a class="pager-prev" href="${prev.slug}.html"><small>← Назад</small><strong>${prev.title}</strong></a>` : '<span></span>'}
    ${next ? `<a class="pager-next" href="${next.slug}.html"><small>Далее →</small><strong>${next.title}</strong></a>` : ''}
  </nav>`
  const body = renderMarkdown(page.body)
  const description = page.description.replace(/"/g, '&quot;')
  return `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="theme-color" content="#0F1F1D"><meta name="description" content="${description}">
<title>${page.title} — Temporality</title>
<link rel="icon" href="../assets/favicon.svg" type="image/svg+xml">
<link rel="preconnect" href="https://fonts.googleapis.com"><link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=Inter:wght@400;500;600;650;700&display=swap" rel="stylesheet">
<link rel="stylesheet" href="../styles.css"><link rel="stylesheet" href="docs.css">
</head>
<body class="docs-page">
${topbar(page.slug)}
<main class="docs-layout">
${sidebar(page.slug)}
<article class="docs-content">
<div class="docs-eyebrow">${page.section}</div>
<h1>${page.title}</h1>
${page.description ? `<p class="docs-lead">${page.description}</p>` : ''}
${body}
${pager}
</article>
</main>
</body>
</html>
`
}

// ── docs index: landing page for the docs section ───────────────────────────

function docsIndex() {
  const intro = pages.find((p) => p.slug === 'intro')
  const cards = sections.map((section) => {
    const rows = section.pages.map((page) =>
      `<a class="docs-card${page.slug === 'intro' ? '' : ''}" href="${page.slug}.html"><strong>${page.title}</strong><span>${page.description}</span></a>`).join('\n')
    return `<div class="docs-card-group"><h3>${section.name}</h3>${rows}</div>`
  }).join('\n')
  return `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="theme-color" content="#0F1F1D"><meta name="description" content="Документация Temporality: быстрый старт, работа с агентом, знания и опыт, навыки, каналы, администрирование.">
<title>Документация — Temporality</title>
<link rel="icon" href="../assets/favicon.svg" type="image/svg+xml">
<link rel="preconnect" href="https://fonts.googleapis.com"><link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=Inter:wght@400;500;600;650;700&display=swap" rel="stylesheet">
<link rel="stylesheet" href="../styles.css"><link rel="stylesheet" href="docs.css">
</head>
<body class="docs-page">
${topbar('index')}
<main class="docs-layout">
${sidebar('index')}
<article class="docs-content">
<div class="docs-eyebrow">Документация</div>
<h1>Temporality — руководство пользователя</h1>
<p class="docs-lead">Temporality наблюдает, как ИИ-агент выполняет задачи: что он знал и делал, откуда взялись знания, что подтвердилось, и возвращает проверенный опыт в следующие запуски. Этот раздел — практическое руководство: от запуска до повседневной работы.</p>
${intro ? `<p><a class="btn btn-primary" href="intro.html"><span>Начать знакомство</span><span>→</span></a></p>` : ''}
<div class="docs-cards">
${cards}
</div>
</article>
</main>
</body>
</html>
`
}

mkdirSync(OUT, { recursive: true })
writeFileSync(join(OUT, 'docs.css'), readFileSync(join(ROOT, 'docs', 'user', 'docs.css'), 'utf8'))
for (const page of pages) {
  writeFileSync(join(OUT, `${page.slug}.html`), pageHtml(page))
  console.log(`✓ ${page.slug}.html`)
}
writeFileSync(join(OUT, 'index.html'), docsIndex())
console.log('✓ index.html')
console.log(`\n${pages.length} pages → ${OUT}`)
