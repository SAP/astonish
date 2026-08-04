import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { useChannelLinkFlow } from '../useChannelLinkFlow'

const linkedSlackChannel = {
  id: 'slack-1',
  user_id: 'user-1',
  channel_type: 'slack',
  external_id: 'U123',
  display_name: 'alice',
  enabled: true,
  verified: true,
  created_at: '2026-08-04T00:00:00Z',
}

describe('useChannelLinkFlow', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('detects completion when the linked channel is already present in the initial list', async () => {
    vi.useFakeTimers()
    const onLinked = vi.fn()
    const onSuccess = vi.fn()
    const onError = vi.fn()

    let call = 0
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      call += 1
      if (url === '/api/user/channels/link-code') {
        return new Response(JSON.stringify({ code: 'ABC123', expires_in: 300 }), { status: 200 })
      }
      return new Response(JSON.stringify({ channels: [linkedSlackChannel] }), { status: 200 })
    }))

    const { result } = renderHook(() => useChannelLinkFlow({
      channelType: 'slack',
      channels: [linkedSlackChannel],
      onLinked,
      onError,
      onSuccess,
    }))

    await act(async () => {
      await result.current[1].generateCode()
    })

    await act(async () => {
      vi.advanceTimersByTime(3000)
    })

    expect(onLinked).toHaveBeenCalledTimes(1)
    expect(onSuccess).toHaveBeenCalledWith('Slack linked and verified successfully!')
    expect(onError).not.toHaveBeenCalled()
    expect(call).toBeGreaterThanOrEqual(2)
  })
})
