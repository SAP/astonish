import { describe, it, expect } from 'vitest'
import { buildPath, parseHash } from '../../hooks/useHashRouter'

// Note: parseHash is not exported, but we can test it indirectly through the hook
// or test buildPath which is the exported function

describe('buildPath', () => {
  it('builds agent path', () => {
    expect(buildPath('agent', { agentName: 'my-agent' })).toBe('/agent/my-agent')
  })

  it('encodes special characters in agent name', () => {
    expect(buildPath('agent', { agentName: 'hello world' })).toBe('/agent/hello%20world')
  })

  it('builds settings path with default section', () => {
    expect(buildPath('settings')).toBe('/settings/chat')
  })

  it('builds settings path with specific section', () => {
    expect(buildPath('settings', { section: 'credentials' })).toBe('/settings/credentials')
  })

  it('builds chat path without session', () => {
    expect(buildPath('chat')).toBe('/chat')
  })

  it('builds chat path with session', () => {
    expect(buildPath('chat', { sessionId: 'abc-123' })).toBe('/chat/abc-123')
  })

  it('builds canvas path', () => {
    expect(buildPath('canvas')).toBe('/canvas')
  })

  it('builds fleet path', () => {
    expect(buildPath('fleet')).toBe('/fleet')
  })

  it('builds fleet path with subview and key', () => {
    expect(buildPath('fleet', { subView: 'session', subKey: 's1' })).toBe('/fleet/session/s1')
  })

  it('builds drill path', () => {
    expect(buildPath('drill')).toBe('/drill')
  })

  it('builds drill path with suite', () => {
    expect(buildPath('drill', { subView: 'suite', subKey: 'my-suite' })).toBe('/drill/suite/my-suite')
  })

  it('builds drill path with drill detail', () => {
    expect(buildPath('drill', { subView: 'drill', subKey: 'suite1', subKey2: 'drill1' }))
      .toBe('/drill/drill/suite1/drill1')
  })

  it('defaults to /chat for unknown view', () => {
    expect(buildPath('unknown')).toBe('/chat')
  })

  it('builds slides path', () => {
    expect(buildPath('slides')).toBe('/slides')
  })

  it('builds slides path with deck slug', () => {
    expect(buildPath('slides', { subKey: 'my-deck' })).toBe('/slides/my-deck')
  })

  it('encodes special characters in slides deck slug', () => {
    expect(buildPath('slides', { subKey: 'q3 review' })).toBe('/slides/q3%20review')
  })
})

describe('parseHash', () => {
  it('parses #/slides to slides view with empty deckSlug', () => {
    expect(parseHash('#/slides')).toEqual({ view: 'slides', params: { deckSlug: '' } })
  })

  it('parses #/slides/my-deck to slides view with deckSlug', () => {
    expect(parseHash('#/slides/my-deck')).toEqual({ view: 'slides', params: { deckSlug: 'my-deck' } })
  })

  it('decodes encoded deckSlug', () => {
    expect(parseHash('#/slides/q3%20review')).toEqual({ view: 'slides', params: { deckSlug: 'q3 review' } })
  })
})
