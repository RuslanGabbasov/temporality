#!/usr/bin/env node
// Builds the user documentation site from markdown sources:
//   docs/user/*.md    → pages/docs/*.html      (Russian, primary)
//   docs/user/en/*.md → pages/docs/en/*.html   (English mirror)
//
// - Sources carry YAML-ish front matter (title, order, section, description).
// - Output is committed, so GitHub Pages serves it without any CI.
// - The chrome reuses the landing's palette and fonts (pages/styles.css)
//   plus a docs-specific stylesheet (pages/docs/docs.css).
//
// Usage: node scripts/build-docs.mjs   (from the repository root)

import { marked } from 'marked'
import { readdirSync, readFileSync, writeFileSync, mkdirSync, existsSync, rmSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { join, basename } from 'node:path'

const ROOT = new URL('..', import.meta.url).pathname

// ── language configuration ──────────────────────────────────────────────────

const LANGS = [
  {
    code: 'ru',
    srcDir: join(ROOT, 'docs', 'user'),
    outDir: join(ROOT, 'pages', 'docs'),
    rootPrefix: '..', // landing and assets, relative to outDir
    cssHref: '__CSS__',
    other: 'en', // the mirrored tree (slug-to-slug)
    toggleLabel: 'EN',
    editBase: 'docs/user',
    strings: {
      docsLabel: 'Документация',
      editLabel: 'Изменить страницу',
      backLabel: '← На сайт',
      pagerPrev: '← Назад',
      pagerNext: 'Далее →',
      docsTitle: 'Temporality — Руководство пользователя',
      docsLead: 'Temporality наблюдает, как ИИ-агент выполняет задачи: что он знал и делал, откуда взялись знания, что подтвердилось, и возвращает проверенный опыт в следующие запуски. Этот раздел — практическое руководство: от запуска до повседневной работы.',
      startLabel: 'Начать знакомство',
      htmlLang: 'ru',
    },
  },
  {
    code: 'en',
    srcDir: join(ROOT, 'docs', 'user', 'en'),
    outDir: join(ROOT, 'pages', 'docs', 'en'),
    rootPrefix: '../..',
    cssHref: '../__CSS__',
    other: 'ru',
    toggleLabel: 'RU',
    editBase: 'docs/user/en',
    strings: {
      docsLabel: 'Docs',
      editLabel: 'Edit this page',
      backLabel: '← Back to site',
      pagerPrev: '← Previous',
      pagerNext: 'Next →',
      docsTitle: 'Temporality — User Guide',
      docsLead: 'Temporality observes how an AI agent works: what it knew and did, where its knowledge came from, what held up — and returns verified experience to later runs. This section is a practical guide, from installation to everyday work.',
      startLabel: 'Get started',
      htmlLang: 'en',
    },
  },
]

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

function makeRenderer() {
  const renderer = new marked.Renderer({ gfm: true })
  renderer.heading = function ({ tokens, depth }) {
    const text = this.parser.parseInline(tokens)
    const anchor = text.toLowerCase()
      .replace(/<[^>]+>/g, '')
      .replace(/[^\p{L}\p{N}\s-]/gu, '')
      .trim()
      .replace(/\s+/g, '-')
    return `<h${depth} id="${anchor}"><a class="anchor" href="#${anchor}" aria-label="Section link">#</a>${text}</h${depth}>`
  }
  renderer.link = function ({ href, tokens }) {
    const text = this.parser.parseInline(tokens)
    const external = /^[a-z]+:/i.test(href)
    return `<a href="${href}"${external ? ' target="_blank" rel="noreferrer"' : ''}>${text}${external ? '&nbsp;<span class="ext">↗</span>' : ''}</a>`
  }
  return renderer
}

// ── build one language tree ─────────────────────────────────────────────────

function buildLang(lang) {
  const { srcDir, outDir, rootPrefix, strings } = lang
  if (!existsSync(srcDir)) {
    console.error(`docs sources not found: ${srcDir}`)
    process.exit(1)
  }
  const renderer = makeRenderer()
  marked.use({ renderer, gfm: true, breaks: false })

  const files = readdirSync(srcDir).filter((f) => f.endsWith('.md')).sort()
  const pages = files.map((file) => {
    const { meta, body } = parseFrontMatter(readFileSync(join(srcDir, file), 'utf8'))
    // The leading `# Title` duplicates front-matter title and the page h1.
    const stripped = body.replace(/^#\s+.+\n+/, '')
    return {
      slug: basename(file, '.md'),
      order: Number(meta.order ?? 999),
      section: meta.section ?? '—',
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

  const brandHref = `${rootPrefix}/`
  const topbar = (slug) => {
    const github = `https://github.com/RuslanGabbasov/temporality`
    const toggleHref = lang.other === 'en' ? `en/${slug}.html` : `../${slug}.html`
    const nav = lang.code === 'ru'
      ? `<a href="${rootPrefix}/#product">Продукт</a><a href="${rootPrefix}/#guide">Руководство</a><a href="${rootPrefix}/#architecture">Архитектура</a>`
      : `<a href="${rootPrefix}/#product">Product</a><a href="${rootPrefix}/#guide">Get started</a><a href="${rootPrefix}/#architecture">Architecture</a>`
    return `<header class="topbar"><a class="brand" href="${brandHref}" aria-label="Temporality home"><img src="${rootPrefix}/assets/temporality-mark.svg" alt=""/><span>temporality</span></a><nav class="nav"><a class="nav-docs active" href="index.html">${strings.docsLabel}</a>${nav}</nav><div class="top-actions"><a class="lang-toggle" href="${toggleHref}" aria-label="Switch language">${lang.toggleLabel}</a><a class="github-link" href="${github}/blob/main/${lang.editBase}/${slug}.md" target="_blank" rel="noreferrer"><span>${strings.editLabel}</span><span class="arrow">↗</span></a></div></header>`
  }

  const sidebar = (activeSlug) => {
    const items = sections.map((section) => {
      const rows = section.pages.map((page) =>
        `<a class="docs-nav-row${page.slug === activeSlug ? ' active' : ''}" href="${page.slug}.html">${page.title}</a>`).join('\n')
      return `<div class="docs-nav-section">${section.name}</div>\n${rows}`
    }).join('\n')
    return `<aside class="docs-sidebar"><nav>${items}\n<div class="docs-nav-section">&nbsp;</div><a class="docs-nav-row" href="${brandHref}">${strings.backLabel}</a></nav></aside>`
  }

  const head = (title, description) => {
    // Cache-buster: a stale cached docs.css once made the primary button
    // invisible (amber on amber) — the version forces fresh styles.
    return `<!doctype html>
<html lang="${strings.htmlLang}">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="theme-color" content="#0F1F1D"><meta name="description" content="${description.replace(/"/g, '&quot;')}">
<title>${title}</title>
<link rel="icon" href="${rootPrefix}/assets/favicon.svg" type="image/svg+xml">
<link rel="preconnect" href="https://fonts.googleapis.com"><link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=Inter:wght@400;500;600;650;700&display=swap" rel="stylesheet">
<link rel="stylesheet" href="${rootPrefix}/styles.css"><link rel="stylesheet" href="${lang.cssHref}">
</head>
<body class="docs-page">`
  }

  const pageHtml = (page) => {
    const index = pages.indexOf(page)
    const prev = pages[index - 1]
    const next = pages[index + 1]
    const pager = `
  <nav class="docs-pager">
    ${prev ? `<a class="pager-prev" href="${prev.slug}.html"><small>${strings.pagerPrev}</small><strong>${prev.title}</strong></a>` : '<span></span>'}
    ${next ? `<a class="pager-next" href="${next.slug}.html"><small>${strings.pagerNext}</small><strong>${next.title}</strong></a>` : ''}
  </nav>`
    return `${head(`${page.title} — Temporality`, page.description)}
${topbar(page.slug)}
<main class="docs-layout">
${sidebar(page.slug)}
<article class="docs-content">
<div class="docs-eyebrow">${page.section}</div>
<h1>${page.title}</h1>
${page.description ? `<p class="docs-lead">${page.description}</p>` : ''}
${marked.parse(page.body)}
${pager}
</article>
</main>
</body>
</html>
`
  }

  const docsIndex = () => {
    const cards = sections.map((section) => {
      const rows = section.pages.map((page) =>
        `<a class="docs-card" href="${page.slug}.html"><strong>${page.title}</strong><span>${page.description}</span></a>`).join('\n')
      return `<div class="docs-card-group"><h3>${section.name}</h3>${rows}</div>`
    }).join('\n')
    return `${head(strings.docsTitle, strings.docsLead)}
${topbar('index')}
<main class="docs-layout">
${sidebar('index')}
<article class="docs-content">
<div class="docs-eyebrow">${strings.docsLabel}</div>
<h1>${strings.docsTitle.replace('Temporality — ', '')}</h1>
<p class="docs-lead">${strings.docsLead}</p>
<p><a class="btn btn-primary" style="color:#1b211f" href="intro.html"><span>${strings.startLabel}</span><span>→</span></a></p>
<div class="docs-cards">
${cards}
</div>
</article>
</main>
</body>
</html>
`
  }

  mkdirSync(outDir, { recursive: true })
  for (const page of pages) {
    writeFileSync(join(outDir, `${page.slug}.html`), pageHtml(page))
    console.log(`✓ ${lang.code}/${page.slug}.html`)
  }
  writeFileSync(join(outDir, 'index.html'), docsIndex())
  console.log(`✓ ${lang.code}/index.html (${pages.length} pages)`)
}

// The shared stylesheet lives with the primary language tree. Its name
// carries a content hash: changing styles invalidates every visitor's cache
// without touching the HTML sources, and the build stays deterministic.
const cssSource = join(ROOT, 'docs', 'user', 'docs.css')
const cssBody = readFileSync(cssSource)
const cssName = 'docs.' + createHash('sha256').update(cssBody).digest('hex').slice(0, 8) + '.css'
const outDir = join(ROOT, 'pages', 'docs')
mkdirSync(outDir, { recursive: true })
// Remove stylesheets of previous builds so the directory holds exactly one.
for (const file of readdirSync(outDir)) {
  if (/^docs\.[0-9a-f]{8}\.css$/.test(file) && file !== cssName) rmSyncSafe(join(outDir, file))
}
writeFileSync(join(outDir, cssName), cssBody)
for (const lang of LANGS) {
  lang.cssHref = lang.cssHref.replace('__CSS__', cssName)
  buildLang(lang)
}
console.log('✓ ' + cssName)


