import { describe, expect, it } from 'vitest'
import { maskServerAddress } from './Mcp'

/** The MCP card must never put credentials on screen: servers that demand a
 *  token inside the URL (query param or user:pass@host) get it masked in the
 *  one-line card view — and the long address ellipsizes instead of stretching
 *  the card. */
describe('maskServerAddress', () => {
  it('masks token-like query values', () => {
    expect(maskServerAddress('https://mcp.example.com/sse?token=abc123def456'))
      .toBe('https://mcp.example.com/sse?token=•••')
    expect(maskServerAddress('https://mcp.example.com/sse?api_key=secret&x=1'))
      .toBe('https://mcp.example.com/sse?api_key=•••&x=1')
    expect(maskServerAddress('https://mcp.example.com/sse?password=hunter2'))
      .toBe('https://mcp.example.com/sse?password=•••')
  })

  it('masks user:password@ credentials', () => {
    expect(maskServerAddress('https://user:secret@mcpgw.internal/sse'))
      .toBe('https://•••@mcpgw.internal/sse')
  })

  it('leaves plain addresses alone', () => {
    expect(maskServerAddress('https://mcp.example.com/sse'))
      .toBe('https://mcp.example.com/sse')
  })
})
