import { describe, expect, it } from 'vitest'
import { baseSandboxIsLive, type BaseConfigSummary } from '../platformSandbox'

function summary(partial: Partial<BaseConfigSummary>): BaseConfigSummary {
  return {
    layer_id: '',
    size_bytes: 0,
    config: null,
    configured_by: '',
    configured_at: null,
    updated_at: '',
    ...partial,
  }
}

describe('baseSandboxIsLive', () => {
  it('treats Incus leftover config as not live', () => {
    expect(baseSandboxIsLive(summary({
      legacy_config: true,
      config: { core: true, optional_tools: [], browser: { engine: 'cloakbrowser' } },
      configured_at: '2026-09-06T20:02:59Z',
    }))).toBe(false)
  })

  it('treats missing overlay as not live', () => {
    expect(baseSandboxIsLive(summary({ overlay_ready: false, layer_id: 'abc' }))).toBe(false)
  })

  it('requires a real layer id', () => {
    expect(baseSandboxIsLive(summary({ overlay_ready: true, layer_id: '@base' }))).toBe(false)
    expect(baseSandboxIsLive(summary({ overlay_ready: true, layer_id: '' }))).toBe(false)
    expect(baseSandboxIsLive(summary({ overlay_ready: true, layer_id: 'deadbeefcafe' }))).toBe(true)
  })
})
