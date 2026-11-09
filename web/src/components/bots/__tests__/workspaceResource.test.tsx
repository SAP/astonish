import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { useWorkspaceResource } from '../workspaceResource'

describe('workspace resource identity', () => {
  it('ignores a stale response even when transport ignores abort', async () => {
    let resolveOld!: (value: string) => void
    const old = new Promise<string>(resolve => { resolveOld = resolve })
    const load = vi.fn().mockReturnValueOnce(old).mockResolvedValue('new')
    const { result, rerender } = renderHook(({ id }) => useWorkspaceResource(load, id), { initialProps: { id: 'personal/a' } })
    await waitFor(() => expect(load).toHaveBeenCalledTimes(1))
    rerender({ id: 'team/b' })
    expect(result.current.data).toBeUndefined()
    await waitFor(() => expect(result.current.data).toBe('new'))
    await act(async () => resolveOld('old'))
    expect(result.current.data).toBe('new')
  })
  it('retains same-key data on retry failure, but not on identity change', async () => {
    const load = vi.fn().mockResolvedValueOnce('draft').mockRejectedValue(new Error('CAS conflict'))
    const { result, rerender } = renderHook(({ id, revision }) => useWorkspaceResource(load, id, revision), { initialProps: { id: 'a', revision: 0 } })
    await waitFor(() => expect(result.current.data).toBe('draft'))
    rerender({ id: 'a', revision: 1 })
    await waitFor(() => expect(result.current.error).toBe('CAS conflict'))
    expect(result.current.data).toBe('draft')
    rerender({ id: 'b', revision: 1 })
    expect(result.current.data).toBeUndefined()
    await waitFor(() => expect(result.current.error).toBe('CAS conflict'))
  })
})
