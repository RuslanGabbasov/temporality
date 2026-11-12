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

// ── search-engine indexing (docs/artifact-lifecycle is unrelated) ───────────
// The canonical origin GitHub Pages serves the site from. Every generated
// page carries a canonical URL, Open Graph / Twitter cards, and — where a
// mirror exists — hreflang alternates; the builder also emits sitemap.xml
// and pages/robots.txt points at it.
const SITE = {
  origin: 'https://ruslangabbasov.github.io',
  base: '/temporality',
  ogImage: '/assets/run.webp',
  ogImageWidth: 1600,
  ogImageHeight: 900,
}
const siteUrl = (path) => `${SITE.origin}${SITE.base}${path}`

// ── language configuration ──────────────────────────────────────────────────

const LANGS = [
  {
    code: 'ru',
    srcDir: join(ROOT, 'docs', 'user'),
    outDir: join(ROOT, 'pages', 'docs'),
    rootPrefix: '..', // landing and assets, relative to outDir
    cssHref: '__CSS__',
    techPrefix: 'tech', // engineering docs live under docs/tech/ on the site
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
      llmsHeading: 'Руководство пользователя (русский)',
    },
  },
  {
    code: 'en',
    srcDir: join(ROOT, 'docs', 'user', 'en'),
    outDir: join(ROOT, 'pages', 'docs', 'en'),
    rootPrefix: '../..',
    cssHref: '../__CSS__',
    techPrefix: '../tech',
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
      llmsHeading: 'User guide (English)',
    },
  },
  {
    // Engineering docs: the repository's top-level `docs/*.md`, published as
    // one monolingual reference tree. No front matter there — titles come
    // from the leading `# ` heading.
    code: 'tech',
    srcDir: join(ROOT, 'docs'),
    outDir: join(ROOT, 'pages', 'docs', 'tech'),
    rootPrefix: '../..',
    cssHref: '../__CSS__',
    techPrefix: '.', // docs/x.md references are siblings in this tree
    other: null, // no mirror — the toggle is omitted
    editBase: 'docs',
    sectionOverride: 'Инженерные документы',
    strings: {
      docsLabel: 'Инженерные документы',
      editLabel: 'Изменить страницу',
      backLabel: '← На сайт',
      pagerPrev: '← Назад',
      pagerNext: 'Далее →',
      docsTitle: 'Temporality — Инженерные документы',
      docsLead: 'Внутренние документы разработки: архитектура, модели событий и знаний, контракты подсистем, планы и разборы. Рабочие материалы инженеров — точность важнее полировки.',
      startLabel: '',
      htmlLang: 'ru',
      llmsHeading: 'Engineering reference (русский)',
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

function makeRenderer(techPrefix) {
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
    // Internal cross-references stay written as .md in the sources (natural
    // in the repo), but the site serves .html slugs — rewrite so no generated
    // page ever links at a markdown file. References to the engineering docs
    // (docs/xxx.md) land on the tech tree's pages.
    let target = href
    if (!external) {
      target = target.replace(/^docs\/([A-Za-z0-9-]+)\.md(?=($|[?#]))/, `${techPrefix}/$1.html`)
      target = target.replace(/\.md(?=($|[?#]))/, '.html')
    }
    return `<a href="${target}"${external ? ' target="_blank" rel="noreferrer"' : ''}>${text}${external ? '&nbsp;<span class="ext">↗</span>' : ''}</a>`
  }
  return renderer
}

// ── build one language tree ─────────────────────────────────────────────────

const llmsSections = []

function buildLang(lang) {
  const { srcDir, outDir, rootPrefix, strings } = lang
  if (!existsSync(srcDir)) {
    console.error(`docs sources not found: ${srcDir}`)
    process.exit(1)
  }
  const renderer = makeRenderer(lang.techPrefix ?? 'tech')
  marked.use({ renderer, gfm: true, breaks: false })

  const files = readdirSync(srcDir).filter((f) => f.endsWith('.md')).sort()
  const pages = files.map((file) => {
    const { meta, body } = parseFrontMatter(readFileSync(join(srcDir, file), 'utf8'))
    // The leading `# Title` duplicates front-matter title and the page h1 —
    // but for sources without front matter (the engineering docs) it IS the
    // title.
    const h1 = body.match(/^#\s+(.+)$/m)?.[1]?.trim() ?? ''
    const stripped = body.replace(/^#\s+.+\n+/, '')
    return {
      slug: basename(file, '.md'),
      order: Number(meta.order ?? 999),
      section: meta.section ?? lang.sectionOverride ?? '—',
      title: meta.title ?? h1 ?? basename(file),
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
    const nav = lang.code === 'en'
      ? `<a href="${rootPrefix}/#product">Product</a><a href="${rootPrefix}/#guide">Get started</a><a href="${rootPrefix}/#architecture">Architecture</a>`
      : `<a href="${rootPrefix}/#product">Продукт</a><a href="${rootPrefix}/#guide">Руководство</a><a href="${rootPrefix}/#architecture">Архитектура</a>`
    const toggle = lang.other ? `<a class="lang-toggle" href="${toggleHref}" aria-label="Switch language">${lang.toggleLabel}</a>` : ''
    return `<header class="topbar"><a class="brand" href="${brandHref}" aria-label="Temporality home"><img src="${rootPrefix}/assets/temporality-mark.svg" alt=""/><span>temporality</span></a><nav class="nav"><a class="nav-docs active" href="index.html">${strings.docsLabel}</a>${nav}</nav><div class="top-actions">${toggle}<a class="github-link" href="${github}/blob/main/${lang.editBase}/${slug}.md" target="_blank" rel="noreferrer"><span>${strings.editLabel}</span><span class="arrow">↗</span></a></div></header>`
  }

  const sidebar = (activeSlug) => {
    const items = sections.map((section) => {
      const rows = section.pages.map((page) =>
        `<a class="docs-nav-row${page.slug === activeSlug ? ' active' : ''}" href="${page.slug}.html">${page.title}</a>`).join('\n')
      return `<div class="docs-nav-section">${section.name}</div>\n${rows}`
    }).join('\n')
    return `<aside class="docs-sidebar"><nav>${items}\n<div class="docs-nav-section">&nbsp;</div><a class="docs-nav-row" href="${brandHref}">${strings.backLabel}</a></nav></aside>`
  }

  const head = (title, description, slug = 'index') => {
    // Cache-buster: a stale cached docs.css once made the primary button
    // invisible (amber on amber) — the version forces fresh styles.
    const canonical = siteUrl(`/docs/${lang.code === 'ru' ? '' : lang.code + '/'}${slug}.html`)
    // hreflang only where a mirror exists (tech tree is monolingual).
    const mirror = lang.other
      ? `<link rel="alternate" hreflang="${lang.other}" href="${siteUrl(`/docs/${lang.other}/${slug}.html`)}">\n<link rel="alternate" hreflang="ru" href="${siteUrl(`/docs/${slug}.html`)}">\n<link rel="alternate" hreflang="x-default" href="${siteUrl(`/docs/${slug}.html`)}">`
      : `<link rel="alternate" hreflang="ru" href="${siteUrl(`/docs/${slug}.html`)}">`
    const escaped = (s) => String(s ?? '').replace(/"/g, '&quot;').replace(/</g, '&lt;')
    const desc = escaped(description)
    const og = `
<meta property="og:type" content="article">
<meta property="og:site_name" content="Temporality">
<meta property="og:title" content="${escaped(title)}">
${desc ? `<meta property="og:description" content="${desc}">` : ''}
<meta property="og:url" content="${canonical}">
<meta property="og:image" content="${siteUrl(SITE.ogImage)}">
<meta property="og:image:width" content="${SITE.ogImageWidth}">
<meta property="og:image:height" content="${SITE.ogImageHeight}">
<meta property="og:locale" content="${strings.htmlLang === 'ru' ? 'ru_RU' : 'en_US'}">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:title" content="${escaped(title)}">
${desc ? `<meta name="twitter:description" content="${desc}">` : ''}
<meta name="twitter:image" content="${siteUrl(SITE.ogImage)}">`
    return `<!doctype html>
<html lang="${strings.htmlLang}">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="index, follow">
<meta name="theme-color" content="#0F1F1D">${desc ? `<meta name="description" content="${desc}">` : ''}
<title>${title}</title>
<link rel="canonical" href="${canonical}">
${mirror}
${og}
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
    return `${head(`${page.title} — Temporality`, page.description, page.slug)}
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
    return `${head(strings.docsTitle, strings.docsLead, 'index')}
${topbar('index')}
<main class="docs-layout">
${sidebar('index')}
<article class="docs-content">
<div class="docs-eyebrow">${strings.docsLabel}</div>
<h1>${strings.docsTitle.replace('Temporality — ', '')}</h1>
<p class="docs-lead">${strings.docsLead}</p>
${strings.startLabel ? `<p><a class="btn btn-primary" style="color:#1b211f" href="intro.html"><span>${strings.startLabel}</span><span>→</span></a></p>` : ''}
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
  // llms.txt section: every page's clean markdown source on GitHub — the
  // LLM-friendly form the llms.txt convention asks links to point at.
  const raw = (slug) => `https://raw.githubusercontent.com/RuslanGabbasov/temporality/main/${lang.editBase}/${slug}.md`
  llmsSections.push({
    code: lang.code,
    heading: lang.strings.llmsHeading ?? lang.strings.docsTitle,
    entries: pages.map((p) => `- [${p.title}](${raw(p.slug)})${p.description ? `: ${p.description}` : ''}`),
    index: `- [${lang.strings.docsTitle}](${raw('index')})`,
  })
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
  if (/^docs\.[0-9a-f]{8}\.css$/.test(file) && file !== cssName) {
    try {
      rmSync(join(outDir, file))
    } catch {
      // A stale stylesheet that refuses to leave must not kill the build:
      // the pages already point at the new hash.
    }
  }
}
writeFileSync(join(outDir, cssName), cssBody)
for (const lang of LANGS) {
  lang.cssHref = lang.cssHref.replace('__CSS__', cssName)
  buildLang(lang)
}
console.log('✓ ' + cssName)

// ── search-engine files ─────────────────────────────────────────────────────
// sitemap.xml covers every generated doc page plus the landing; robots.txt
// (a static file, written here so one command refreshes both) points at it.
// GitHub Pages serves the whole tree from the /temporality/ subpath, and the
// lastmod stamps use today's build time — the honest "freshly regenerated".
const htmlFiles = (dir) => readdirSync(dir).filter((f) => f.endsWith('.html')).map((f) => f.slice(0, -5))
const docUrls = []
for (const slug of htmlFiles(outDir)) docUrls.push({ loc: `/docs/${slug}.html`, lang: 'ru' })
const enDir = join(ROOT, 'pages', 'docs', 'en')
for (const slug of htmlFiles(enDir)) docUrls.push({ loc: `/docs/en/${slug}.html`, lang: 'en' })
const techDir = join(ROOT, 'pages', 'docs', 'tech')
for (const slug of htmlFiles(techDir)) docUrls.push({ loc: `/docs/tech/${slug}.html`, lang: 'ru' })
const today = new Date().toISOString().slice(0, 10)
const urlEntries = [{ loc: '/', lang: 'ru', priority: '1.0' }, { loc: '/docs/index.html', lang: 'ru', priority: '0.8' },
  ...docUrls.filter((u) => u.loc !== '/docs/index.html').map((u) => ({ ...u, priority: u.loc.startsWith('/docs/en/') ? '0.6' : '0.7' }))]
const sitemap = `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">\n${urlEntries.map((u) => {
  const alts = u.lang === 'ru' && docUrls.some((d) => d.loc === `/docs/en/${u.loc.slice('/docs/'.length, -5)}.html`)
    ? `\n  <xhtml:link rel="alternate" hreflang="en" href="${siteUrl(`/docs/en/${u.loc.slice('/docs/'.length, -5)}.html`)}"/><xhtml:link rel="alternate" hreflang="x-default" href="${siteUrl(u.loc)}"/>`
    : ''
  return `  <url>\n    <loc>${siteUrl(u.loc)}</loc>${alts}\n    <lastmod>${today}</lastmod>\n    <priority>${u.priority}</priority>\n  </url>`
}).join('\n')}\n</urlset>\n`
writeFileSync(join(ROOT, 'pages', 'sitemap.xml'), sitemap)
writeFileSync(join(ROOT, 'pages', 'robots.txt'), `User-agent: *\nAllow: /\nSitemap: ${siteUrl('/sitemap.xml')}\n`)
console.log(`✓ sitemap.xml (${urlEntries.length} urls) + robots.txt`)

// ── AI-agent discovery: llms.txt + ai-catalog.json (ARD 1.0) ────────────────
// llms.txt (llmstxt.org): an H1, a blockquote summary, then curated H2
// sections of links — every link points at the CLEAN MARKDOWN source on
// GitHub, the LLM-friendly form of the page. ai-catalog.json follows the
// Agentic Resource Discovery schema (specVersion 1.0) so registries and
// autonomous agents can verify the resources; it is published twice — at
// the site root and under .well-known/ — because project sites cannot
// write to the origin's root well-known directory.
const llms = [`# Temporality`, '',
  '> Temporality is an open-source runtime and control plane for AI agent systems: it runs long-lived tasks, keeps an evidence-backed history of every action, governs permissions and oversight, and turns verified experience into knowledge and skills. Self-hosted on your infrastructure. Go · Temporal · PostgreSQL.',
  '',
  'Documentation is available in Russian and English; every link below points at the clean Markdown source of the page (the rendered HTML lives at the matching .html path on this site).',
  '',
]
for (const section of llmsSections) {
  if (section.entries.length === 0) continue
  llms.push(`## ${section.heading}`, section.index, ...section.entries, '')
}
llms.push('## Project', '',
  '- [Source code (GitHub)](https://github.com/RuslanGabbasov/temporality): the full runtime, control plane, agent kernel and this site',
  '- [License (Apache-2.0)](https://github.com/RuslanGabbasov/temporality/blob/main/LICENSE)',
  '- [Issue tracker](https://github.com/RuslanGabbasov/temporality/issues)',
  '- [Sitemap](https://ruslangabbasov.github.io/temporality/sitemap.xml): every indexed page for crawlers',
  '')
writeFileSync(join(ROOT, 'pages', 'llms.txt'), llms.join('\n'))

const site = siteUrl('/')
const catalog = {
  specVersion: '1.0',
  host: {
    displayName: 'Temporality',
    documentationUrl: site,
    logoUrl: siteUrl('/assets/temporality-mark.svg'),
  },
  entries: [
    {
      identifier: 'urn:air:ruslangabbasov.github.io:temporality:docs',
      displayName: 'Temporality documentation',
      type: 'text/html',
      url: siteUrl('/docs/index.html'),
      description: 'User guide for the Temporality agent runtime and control plane: installation, chat runs, traces, artifacts with review flows, knowledge and skills.',
      tags: ['documentation', 'ai-agents', 'temporal', 'postgresql', 'self-hosted'],
      capabilities: ['installation-guide', 'run-traces', 'artifact-review', 'knowledge-management', 'skills'],
      representativeQueries: [
        'How do I install and run Temporality?',
        'How does artifact review work in Temporality?',
        'Где посмотреть историю действий агента?',
      ],
      version: '1',
      updatedAt: `${today}T00:00:00Z`,
    },
    {
      identifier: 'urn:air:ruslangabbasov.github.io:temporality:llms-txt',
      displayName: 'Temporality llms.txt',
      type: 'text/plain',
      url: siteUrl('/llms.txt'),
      description: 'LLM-oriented overview of the site: curated links to the clean Markdown source of every documentation page.',
      tags: ['llms-txt', 'documentation', 'markdown'],
      representativeQueries: [
        'Temporality documentation for LLM agents',
        'Clean markdown sources of Temporality docs',
      ],
      version: '1',
      updatedAt: `${today}T00:00:00Z`,
    },
  ],
}
const catalogJson = JSON.stringify(catalog, null, 2) + '\n'
writeFileSync(join(ROOT, 'pages', 'ai-catalog.json'), catalogJson)
mkdirSync(join(ROOT, 'pages', '.well-known'), { recursive: true })
writeFileSync(join(ROOT, 'pages', '.well-known', 'ai-catalog.json'), catalogJson)
console.log(`✓ llms.txt + ai-catalog.json (root + .well-known)`)


