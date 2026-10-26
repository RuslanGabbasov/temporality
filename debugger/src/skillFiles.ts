// Bundled skill files: parsing and risk scanning (docs/builtin-skills.md,
// docs/living-skills.md §4). The import pipeline (cmd/agent-kernel/skillimport.go)
// stores a skill's resource files inside the SKILL.md markdown: text files are
// inlined as `### path` + fenced code block under a `## Bundled files` heading;
// binaries and oversized files are listed as `- `path (N bytes)`` bullets under
// "Not inlined". This module makes that appendix visible and reviewable in the
// skill editor: file list, content view, and a pattern scan that surfaces
// dangerous code before a human binds the skill to an agent.

export interface BundledFile {
  path: string
  lang: string
  content: string
}

export interface BundledListed {
  path: string
  sizeLabel: string
}

export interface BundledFiles {
  files: BundledFile[]
  listed: BundledListed[]
}

/** Parse the "## Bundled files" appendix out of a skill's markdown. Tolerant
 * by design: a hand-written appendix with slightly different phrasing still
 * parses; a markdown without the appendix returns an empty result. */
export function extractBundledFiles(markdown: string): BundledFiles {
  const result: BundledFiles = { files: [], listed: [] }
  if (!markdown) return result
  const heading = markdown.match(/^##\s+Bundled files\s*$/m)
  if (!heading || heading.index === undefined) return result
  const appendix = markdown.slice(heading.index + heading[0].length)
  // The appendix ends at the next H2 (the import pipeline appends nothing,
  // but a human-edited skill may continue with more sections).
  const nextH2 = appendix.match(/^##\s+\S/m)
  const body = nextH2 && nextH2.index !== undefined ? appendix.slice(0, nextH2.index) : appendix

  // Inlined files: "### <path>" followed by a fenced code block.
  const blockRe = /^###\s+(.+?)\s*\n+```([^\n`]*)\n([\s\S]*?)\n?```/gm
  for (const match of body.matchAll(blockRe)) {
    const path = match[1].trim().replace(/^`|`$/g, '')
    if (!path) continue
    result.files.push({ path, lang: match[2].trim(), content: match[3] })
  }

  // Not inlined: "- `path (123 bytes)`" bullets after the marker line.
  const listedMarker = body.indexOf('Not inlined')
  if (listedMarker >= 0) {
    const bulletRe = /^-\s+`(.+?)\s*\(([^)]+)\)`/gm
    for (const match of body.slice(listedMarker).matchAll(bulletRe)) {
      result.listed.push({ path: match[1].trim(), sizeLabel: match[2].trim() })
    }
  }
  return result
}

export type RiskSeverity = 'danger' | 'warning'

export interface RiskFinding {
  severity: RiskSeverity
  label: string
  line: number
  excerpt: string
}

interface RiskRule {
  severity: RiskSeverity
  label: string
  re: RegExp
}

// Rules are matched case-insensitively, one line at a time — a finding cites
// its line and carries a trimmed excerpt, so a reviewer sees the evidence
// without opening the whole file.
const RISK_RULES: RiskRule[] = [
  { severity: 'danger', label: 'destructive delete', re: /\brm\s+(-[a-zA-Z]*[rf][a-zA-Z]*\s+)+|--no-preserve-root/ },
  { severity: 'danger', label: 'disk overwrite', re: /\bmkfs\b|\bdd\s+if=|\bshred\b/ },
  { severity: 'danger', label: 'remote code execution', re: /\b(curl|wget)\b[^|\n]*\|\s*(sudo\s+)?(ba)?sh\b/ },
  { severity: 'danger', label: 'sudo', re: /\bsudo\b/ },
  { severity: 'danger', label: 'chmod 777 / world-writable', re: /\bchmod\b[^#\n]*\b777\b/ },
  { severity: 'danger', label: 'dynamic evaluation', re: /\beval\s*\(|\bexec\s*\(\s*[\"']|new Function\s*\(|\bos\.system\b|shell\s*=\s*True\b/ },
  { severity: 'danger', label: 'encoded payload', re: /\bbase64\b[^#\n|]*( -d|--decode)|\bexec\(\s*base64/ },
  { severity: 'danger', label: 'ssh keys / credentials access', re: /\.ssh\b|authorized_keys|\bid_rsa\b|\/etc\/shadow\b/ },
  { severity: 'danger', label: 'persistence / cron', re: /\bcrontab\b|systemctl\s+enable|rc\.local/ },
  { severity: 'warning', label: 'network fetch', re: /\b(curl|wget|nc|ncat)\b/ },
  { severity: 'warning', label: 'remote shell / transfer', re: /\b(ssh|scp|rsync|sftp)\b/ },
  { severity: 'warning', label: 'package install', re: /\b(pip3? install|npm install|apt(-get)? install|apk add)\b/ },
  { severity: 'warning', label: 'env/secret dump', re: /\bprintenv\b|\benv\b[^\w]|\.env\b|process\.env\b|os\.environ\b/ },
  { severity: 'warning', label: 'writes outside workspace', re: /\/etc\/|\/usr\/local\/bin\/|\/root\/|\~\/\./ },
  { severity: 'warning', label: 'reverse shell indicator', re: /\b\/dev\/tcp\b|\bbash\s+-i\s*>&/, },
  { severity: 'warning', label: 'registry push / git remote', re: /\bgit\s+push\b|\bdocker\s+push\b/ },
]

/** Scan one file's content for risky patterns. Returns findings sorted by
 * severity (danger first) then line. Pure function — unit-testable. */
export function scanRiskyPatterns(path: string, content: string): RiskFinding[] {
  void path
  const findings: RiskFinding[] = []
  const lines = content.split('\n')
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    for (const rule of RISK_RULES) {
      if (!rule.re.test(line)) continue
      // One finding per rule per line, even if the line matches twice.
      findings.push({
        severity: rule.severity,
        label: rule.label,
        line: i + 1,
        excerpt: line.trim().slice(0, 120),
      })
    }
  }
  const order: Record<RiskSeverity, number> = { danger: 0, warning: 1 }
  return findings.sort((a, b) => order[a.severity] - order[b.severity] || a.line - b.line)
}

/** Scan every bundled file; returns findings keyed by path. */
export function scanBundledFiles(files: BundledFile[]): Record<string, RiskFinding[]> {
  const result: Record<string, RiskFinding[]> = {}
  for (const file of files) {
    const findings = scanRiskyPatterns(file.path, file.content)
    if (findings.length > 0) result[file.path] = findings
  }
  return result
}
