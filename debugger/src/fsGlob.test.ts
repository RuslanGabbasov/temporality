import { describe, expect, it } from 'vitest'
import { matchFSPath, normalizeFSPath, pathVerdict, validateFilesystemMask } from './fsGlob'

// The client matcher must agree with workspace/fspolicy.go — the cases below
// mirror the backend's own tests (docs/filesystem-policy.md §3): `**` spans
// zero or more segments, `*`/`?`/`[...]` stay within one segment, paths are
// workspace-relative and dot-free.
describe('matchFSPath', () => {
  it('matches single stars within one segment only', () => {
    expect(matchFSPath('src/*.go', 'src/main.go')).toBe(true)
    expect(matchFSPath('src/*.go', 'src/deep/main.go')).toBe(false)
    expect(matchFSPath('*.go', 'main.go')).toBe(true)
    expect(matchFSPath('*.go', 'src/main.go')).toBe(false)
  })

  it('matches ** across zero or more segments', () => {
    // src/** matches src itself — the backend's zero-segment rule.
    expect(matchFSPath('src/**', 'src')).toBe(true)
    expect(matchFSPath('src/**', 'src/a/b.go')).toBe(true)
    expect(matchFSPath('**/x', 'x')).toBe(true)
    expect(matchFSPath('**/x', 'a/b/x')).toBe(true)
    expect(matchFSPath('**', 'anything/at/all')).toBe(true)
    expect(matchFSPath('a/**/b', 'a/b')).toBe(true)
    expect(matchFSPath('a/**/b', 'a/x/y/b')).toBe(true)
  })

  it('supports ? and character classes', () => {
    expect(matchFSPath('src/file?.txt', 'src/file1.txt')).toBe(true)
    expect(matchFSPath('src/file?.txt', 'src/file10.txt')).toBe(false)
    expect(matchFSPath('[a-z]*.log', 'app.log')).toBe(true)
    expect(matchFSPath('[a-z]*.log', 'App.log')).toBe(false)
    expect(matchFSPath('env.[^0-9]*', 'env.dev')).toBe(true)
    expect(matchFSPath('env.[^0-9]*', 'env.1dev')).toBe(false)
  })

  it('is exact for literal paths', () => {
    expect(matchFSPath('docs/readme.md', 'docs/readme.md')).toBe(true)
    expect(matchFSPath('docs/readme.md', 'docs/other.md')).toBe(false)
    expect(matchFSPath('a/b', 'a/b/c')).toBe(false)
  })
})

describe('validateFilesystemMask', () => {
  it('accepts the masks the backend accepts', () => {
    expect(validateFilesystemMask('**')).toBeNull()
    expect(validateFilesystemMask('projects/app/**')).toBeNull()
    expect(validateFilesystemMask('src/**')).toBeNull()
    expect(validateFilesystemMask('**/.env')).toBeNull()
    expect(validateFilesystemMask('logs/[a-z]*.txt')).toBeNull()
    expect(validateFilesystemMask('a?c')).toBeNull()
  })

  it('rejects what the backend rejects', () => {
    expect(validateFilesystemMask('')).toBe('empty')
    expect(validateFilesystemMask('  src/**  ')).toBe('spaces')
    expect(validateFilesystemMask('src\\go')).toBe('backslash')
    expect(validateFilesystemMask('/etc/passwd')).toBe('absolute')
    expect(validateFilesystemMask('a/../b')).toBe('dots')
    expect(validateFilesystemMask('src//main')).toBe('dots')
    expect(validateFilesystemMask('src/./main')).toBe('dots')
    expect(validateFilesystemMask('src/[unclosed')).toBe('pattern')
  })
})

describe('normalizeFSPath', () => {
  it('accepts the path forms the file tools take', () => {
    expect(normalizeFSPath('/workspace/projects/app/src/main.go')).toBe('projects/app/src/main.go')
    expect(normalizeFSPath('projects/app/src/main.go')).toBe('projects/app/src/main.go')
    expect(normalizeFSPath('projects/app//src/./x')).toBe('projects/app/src/x')
    expect(normalizeFSPath('projects/app/a/../b')).toBe('projects/app/b')
  })

  it('rejects escapes and absolutes', () => {
    expect(normalizeFSPath('')).toBeNull()
    expect(normalizeFSPath('/etc/passwd')).toBeNull()
    expect(normalizeFSPath('/workspace')).toBeNull()
    expect(normalizeFSPath('../secrets')).toBeNull()
    expect(normalizeFSPath('a/../../b')).toBeNull()
  })
})

describe('pathVerdict', () => {
  const rules = {
    read: ['projects/app/**'],
    write: ['projects/app/src/**', 'projects/app/tests/**'],
    deny: ['**/.env', 'projects/app/secrets/**'],
  }

  it('deny wins over any allow', () => {
    const v = pathVerdict(rules, 'projects/app/src/.env')
    expect(v).toEqual({ denied: true, readable: false, writable: false })
  })

  it('write requires both write and read match', () => {
    expect(pathVerdict(rules, 'projects/app/src/main.go')).toEqual({ denied: false, readable: true, writable: true })
    // Under read but outside the write masks.
    expect(pathVerdict(rules, 'projects/app/docs/x.md')).toEqual({ denied: false, readable: true, writable: false })
  })

  it('an unset dimension restricts nothing', () => {
    const empty = { read: [], write: [], deny: [] }
    expect(pathVerdict(empty, 'any/path/here')).toEqual({ denied: false, readable: true, writable: true })
    // The zero-segment rule: src/** covers src itself.
    const readOnlyRoot = { read: ['src/**'], write: [], deny: [] }
    expect(pathVerdict(readOnlyRoot, 'src').readable).toBe(true)
  })
})
