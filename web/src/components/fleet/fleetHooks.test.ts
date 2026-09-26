import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { useFleetDetailTab } from './fleetHooks'

afterEach(() => {
  window.location.hash = ''
})

describe('useFleetDetailTab', () => {
  it('accepts the yaml tab from the hash', () => {
    window.location.hash = '/fleet/plan/my-plan/yaml'
    const { result } = renderHook(() => useFleetDetailTab('plan', 'my-plan'))
    expect(result.current[0]).toBe('yaml')
  })

  it('defaults to overview for an unknown tab', () => {
    window.location.hash = '/fleet/plan/my-plan/bogus'
    const { result } = renderHook(() => useFleetDetailTab('plan', 'my-plan'))
    expect(result.current[0]).toBe('overview')
  })

  it('navigates to the yaml tab via setTab', () => {
    const { result } = renderHook(() => useFleetDetailTab('plan', 'my-plan'))
    act(() => result.current[1]('yaml'))
    expect(result.current[0]).toBe('yaml')
    expect(window.location.hash).toContain('/fleet/plan/my-plan/yaml')
  })
})
