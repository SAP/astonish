import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { fetchCacheDiagnostics } from '@/api/studioChat'
import CacheDiagnosticsPanel from '../CacheDiagnosticsPanel'

vi.mock('@/api/studioChat', () => ({ fetchCacheDiagnostics: vi.fn() }))

describe('CacheDiagnosticsPanel', () => {
  beforeEach(() => vi.mocked(fetchCacheDiagnostics).mockReset())

  it('renders loading then an empty state', async () => {
    let resolve!: (value: { sessionId: string; assistantTurn: number; rounds: [] }) => void
    vi.mocked(fetchCacheDiagnostics).mockReturnValue(new Promise(r => { resolve = r }))
    render(<CacheDiagnosticsPanel sessionId="s1" assistantTurn={1} />)

    expect(screen.getByText('Loading cache diagnostics…')).toBeInTheDocument()
    resolve({ sessionId: 's1', assistantTurn: 1, rounds: [] })
    expect(await screen.findByText('No cache diagnostics were recorded for this assistant turn.')).toBeInTheDocument()
  })

  it('renders multiple rounds, hit statuses, diffs, and payloads', async () => {
    vi.mocked(fetchCacheDiagnostics).mockResolvedValue({
      sessionId: 's1',
      assistantTurn: 1,
      rounds: [
        {
          round: 1,
          cacheStatus: 'miss',
          systemInstruction: { changed: false, currentHash: 'sys-a' },
          toolDeclarations: { changed: false, currentHash: 'tools-a', count: 3 },
        },
        {
          round: 2,
          cacheStatus: 'hit',
          systemInstruction: { changed: true, previousHash: 'sys-a', currentHash: 'sys-b' },
          toolDeclarations: { changed: true, previousHash: 'tools-a', currentHash: 'tools-b', count: 4 },
          payload: { promptTokens: 42 },
        },
      ],
    })

    render(<CacheDiagnosticsPanel sessionId="s1" assistantTurn={1} />)
    expect(await screen.findByText('Model round 1')).toBeInTheDocument()
    expect(screen.getByText('Model round 2')).toBeInTheDocument()
    expect(screen.getByText('Cache miss')).toBeInTheDocument()
    expect(screen.getByText('Cache hit')).toBeInTheDocument()
    expect(screen.getAllByText('Changed')).toHaveLength(2)
    expect(screen.getByText('Payload')).toBeInTheDocument()
    expect(screen.getByText(/"promptTokens": 42/)).toBeInTheDocument()
  })
})
