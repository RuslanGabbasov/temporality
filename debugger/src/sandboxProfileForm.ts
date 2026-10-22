import type { SandboxProfile } from './workspaceApi'

/** Recipe source of the sandbox-profile form (spec 2026-10-21, D2.1/D6):
 *  exactly one of the composed base[+packages] recipe or the operator's full
 *  Dockerfile. The server answers 422 on any mixture — the form must never
 *  generate one. */
export type SandboxRecipeMode = 'base' | 'dockerfile'

/** Live state of the operator-authored Dockerfile against the preview
 *  endpoint (spec §4): the save button accepts only 'valid'. */
export type DockerfileCheck = 'empty' | 'checking' | 'valid' | 'invalid'

export type SandboxProfileForm = {
  id: string
  name: string
  description: string
  image: string
  base: string
  packages: string
  /** Full operator Dockerfile text (dockerfile_override), mode 'dockerfile'. */
  dockerfile: string
  toolchains: string
  memory: string
  cpus: string
  scratch_size: string
  tmp_size: string
  cache_env: string
  docker_socket: boolean
}

export const emptySandboxProfileForm: SandboxProfileForm = {
  id: '',
  name: '',
  description: '',
  image: '',
  base: '',
  packages: '',
  dockerfile: '',
  toolchains: '',
  memory: '',
  cpus: '',
  scratch_size: '',
  tmp_size: '',
  cache_env: '',
  docker_socket: false,
}

const commaList = (value: string) => value.split(',').map((s) => s.trim()).filter(Boolean)

/** An opened profile starts in the mode of its stored recipe source: an
 *  existing override owns the form (D1), everything else builds from base. */
export function recipeModeFor(profile: SandboxProfile | null): SandboxRecipeMode {
  return profile?.dockerfile_override?.trim() ? 'dockerfile' : 'base'
}

/** Prefill when the operator switches into "own Dockerfile" (D6): the current
 *  base+packages composition becomes the starting text; already-typed
 *  operator text is never overwritten. */
export function prefilledDockerfile(typed: string, composedPreview: string): string {
  return typed.trim() ? typed : composedPreview
}

/** The XOR-safe save payload (D2.1): override mode blanks base/packages/image
 *  even when the form carries leftovers from the base/external mode; base mode
 *  drops the override so switching back cannot keep a stale recipe source. */
export function buildSandboxProfilePayload(
  form: SandboxProfileForm,
  mode: SandboxRecipeMode,
): Omit<Partial<SandboxProfile>, 'id' | 'name'> {
  const common = {
    description: form.description,
    toolchains: commaList(form.toolchains),
    memory: form.memory.trim(),
    cpus: form.cpus.trim(),
    scratch_size: form.scratch_size.trim(),
    tmp_size: form.tmp_size.trim(),
    cache_env: form.cache_env.split('\n').map((s) => s.trim()).filter(Boolean),
    docker_socket: form.docker_socket,
  }
  if (mode === 'dockerfile') {
    return {
      ...common,
      image: '',
      base: '',
      packages: [],
      dockerfile_override: form.dockerfile,
    }
  }
  return {
    ...common,
    image: form.base ? '' : form.image.trim(),
    base: form.base,
    packages: form.base ? commaList(form.packages) : [],
    dockerfile_override: '',
  }
}

/** Save gating: identity fields always; in override mode the text must have
 *  passed the live preview check ('empty'/'checking'/'invalid' all block —
 *  spec AC-8); a new base-mode profile still needs a base or an image. */
export function sandboxSaveBlocked(
  form: SandboxProfileForm,
  mode: SandboxRecipeMode,
  dockerfileCheck: DockerfileCheck,
  editing: SandboxProfile | null,
): boolean {
  if (!form.id.trim() || !form.name.trim()) return true
  if (mode === 'dockerfile') return dockerfileCheck !== 'valid'
  if (!editing && !form.base && !form.image.trim()) return true
  return false
}
