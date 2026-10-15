import { describe, expect, it } from 'vitest'
import { humanArtifactSize } from './Artifacts'

describe('humanArtifactSize', () => {
  it('renders empty url-provider bodies as a dash', () => {
    expect(humanArtifactSize(0)).toBe('—')
  })

  it('scales bytes through KiB and MiB', () => {
    expect(humanArtifactSize(512)).toBe('512 B')
    expect(humanArtifactSize(12698)).toBe('12.4 KiB')
    expect(humanArtifactSize(3 * 1024 * 1024)).toBe('3.0 MiB')
  })
})
