import { act, renderHook } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { useBotClock } from '../useBotClock'

afterEach(() => vi.useRealTimers())

it('updates time-dependent Bot controls and cleans up its timer', () => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-01-01T00:00:00Z'))
  const { result, unmount } = renderHook(() => useBotClock())
  const initial = result.current
  act(() => vi.advanceTimersByTime(1000))
  expect(result.current).toBe(initial + 1000)
  unmount()
  expect(vi.getTimerCount()).toBe(0)
})
