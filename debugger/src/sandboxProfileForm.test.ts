import { describe, expect, it } from 'vitest'
import {
  buildSandboxProfilePayload,
  emptySandboxProfileForm,
  prefilledDockerfile,
  recipeModeFor,
  sandboxSaveBlocked,
  type SandboxProfileForm,
} from './sandboxProfileForm'
import type { SandboxProfile } from './workspaceApi'

const form = (overrides: Partial<SandboxProfileForm> = {}): SandboxProfileForm => ({
  ...emptySandboxProfileForm,
  id: 'qa-tools',
  name: 'QA tools',
  ...overrides,
})

const profile = (overrides: Partial<SandboxProfile> = {}): SandboxProfile => ({
  id: 'qa-tools',
  name: 'QA tools',
  builtin: false,
  created_at: '2026-10-21T00:00:00Z',
  updated_at: '2026-10-21T00:00:00Z',
  ...overrides,
})

const recipe = 'FROM python:3.12-alpine AS build\nRUN apk add --no-cache gcc\nFROM python:3.12-alpine\nCOPY --from=build /a /a\n'

describe('recipeModeFor (D6)', () => {
  it('opens a profile with an override in "own Dockerfile" mode', () => {
    expect(recipeModeFor(profile({ dockerfile_override: recipe }))).toBe('dockerfile')
  })

  it('keeps base and external profiles in "build from base" mode', () => {
    expect(recipeModeFor(profile({ base: 'python', packages: ['gcc'] }))).toBe('base')
    expect(recipeModeFor(profile({ image: 'registry.local/toolbox@sha256:ab' }))).toBe('base')
    expect(recipeModeFor(null)).toBe('base')
  })

  it('treats a whitespace-only override as absent', () => {
    expect(recipeModeFor(profile({ dockerfile_override: '   \n  ' }))).toBe('base')
  })
})

describe('prefilledDockerfile (D6: the textarea starts from the composed recipe)', () => {
  it('seeds the editor with the current base+packages composition', () => {
    expect(prefilledDockerfile('', recipe)).toBe(recipe)
  })

  it('never overwrites already-typed operator text', () => {
    expect(prefilledDockerfile('FROM alpine\n', recipe)).toBe('FROM alpine\n')
  })

  it('stays empty when there is neither text nor composition', () => {
    expect(prefilledDockerfile('', '')).toBe('')
    // Whitespace does not count as typed operator text — the composition wins.
    expect(prefilledDockerfile('   ', '')).toBe('')
    expect(prefilledDockerfile('   \n', recipe)).toBe(recipe)
  })
})

describe('buildSandboxProfilePayload — recipe-source XOR (D2.1, AC-8)', () => {
  it('sends dockerfile_override verbatim and blanks base/packages/image', () => {
    // Leftovers from the base/external mode must not leak into the payload:
    // the server rejects any mixture with 422 (workspace.ValidateSandboxProfile).
    const payload = buildSandboxProfilePayload(form({
      base: 'python',
      packages: 'gcc, musl-dev',
      image: 'registry.local/toolbox@sha256:ab',
      dockerfile: recipe,
    }), 'dockerfile')

    expect(payload.dockerfile_override).toBe(recipe)
    expect(payload.base).toBe('')
    expect(payload.packages).toEqual([])
    expect(payload.image).toBe('')
    // The invariant the UI must never break: no mixing can leave the form.
    expect((payload.base ?? '').trim() === '' && (payload.packages ?? []).length === 0 && !payload.image).toBe(true)
  })

  it('keeps identity/resource fields in both modes', () => {
    const common = {
      toolchains: 'rust, wasm',
      memory: ' 2g ',
      cpus: '2',
      scratch_size: '1g',
      tmp_size: '512m',
      cache_env: 'CARGO_HOME=/cache/cargo\n\nRUSTUP_HOME=/cache/rustup\n',
      docker_socket: true,
      description: 'desc',
    }
    const base = buildSandboxProfilePayload(form(common), 'base')
    const own = buildSandboxProfilePayload(form({ ...common, dockerfile: recipe }), 'dockerfile')
    for (const payload of [base, own]) {
      expect(payload.toolchains).toEqual(['rust', 'wasm'])
      expect(payload.memory).toBe('2g')
      expect(payload.cpus).toBe('2')
      expect(payload.scratch_size).toBe('1g')
      expect(payload.tmp_size).toBe('512m')
      expect(payload.cache_env).toEqual(['CARGO_HOME=/cache/cargo', 'RUSTUP_HOME=/cache/rustup'])
      expect(payload.docker_socket).toBe(true)
      expect(payload.description).toBe('desc')
    }
  })

  it('base mode mirrors the legacy payload and drops a stale override', () => {
    // Switching back from "own Dockerfile" must clear the override, otherwise
    // the stored row would mix recipe sources.
    const composed = buildSandboxProfilePayload(form({
      base: 'python',
      packages: 'gcc, , musl-dev',
      dockerfile: recipe,
    }), 'base')
    expect(composed.base).toBe('python')
    expect(composed.packages).toEqual(['gcc', 'musl-dev'])
    expect(composed.image).toBe('')
    expect(composed.dockerfile_override).toBe('')

    // External mode: image is the recipe source, packages are not sent.
    const external = buildSandboxProfilePayload(form({
      image: '  registry.local/toolbox@sha256:ab  ',
      packages: 'gcc',
      dockerfile: recipe,
    }), 'base')
    expect(external.image).toBe('registry.local/toolbox@sha256:ab')
    expect(external.packages).toEqual([])
    expect(external.base).toBe('')
    expect(external.dockerfile_override).toBe('')
  })
})

describe('sandboxSaveBlocked (AC-8: invalid text never saves)', () => {
  const editing = profile({ dockerfile_override: recipe })

  it('blocks the override mode until the live check turns valid', () => {
    expect(sandboxSaveBlocked(form({ dockerfile: recipe }), 'dockerfile', 'empty', null)).toBe(true)
    expect(sandboxSaveBlocked(form({ dockerfile: recipe }), 'dockerfile', 'checking', null)).toBe(true)
    expect(sandboxSaveBlocked(form({ dockerfile: recipe }), 'dockerfile', 'invalid', null)).toBe(true)
    expect(sandboxSaveBlocked(form({ dockerfile: recipe }), 'dockerfile', 'valid', null)).toBe(false)
    expect(sandboxSaveBlocked(form({ dockerfile: recipe }), 'dockerfile', 'valid', editing)).toBe(false)
  })

  it('always requires id and name', () => {
    expect(sandboxSaveBlocked(form({ id: ' ' }), 'dockerfile', 'valid', null)).toBe(true)
    expect(sandboxSaveBlocked(form({ name: '' }), 'base', 'empty', editing)).toBe(true)
  })

  it('keeps the legacy base-mode rules: a new profile needs a base or an image', () => {
    expect(sandboxSaveBlocked(form({ image: 'reg@sha256:ab' }), 'base', 'empty', null)).toBe(false)
    expect(sandboxSaveBlocked(form({ base: 'python' }), 'base', 'empty', null)).toBe(false)
    expect(sandboxSaveBlocked(form({}), 'base', 'empty', null)).toBe(true)
    // Editing never re-demands a recipe source.
    expect(sandboxSaveBlocked(form({}), 'base', 'empty', editing)).toBe(false)
  })

  it('a new override profile does not need base or image (the text is the source)', () => {
    expect(sandboxSaveBlocked(form({ dockerfile: recipe }), 'dockerfile', 'valid', null)).toBe(false)
  })
})
