import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { fetchCacheDiagnostics } from '@/api/studioChat'
import CacheDiagnosticsPanel from '../CacheDiagnosticsPanel'

vi.mock('@/api/studioChat', () => ({ fetchCacheDiagnostics: vi.fn() }))

const round = {
  invocationId: 'inv-1', kind: 'provider' as const, stage: 'provider_dispatch', status: 'succeeded' as const,
  call: 1, stream: true, provider: 'google', model: 'gemini',
  captureLevel: 'canonical-adk', inputHash: 'request-hash', stablePrefixElements: 3,
  stablePrefixBytes: 512, startedAt: '2026-08-29T00:00:00Z', timeToFirstResponse: 1000000,
  duration: 2000000, responseCount: 2, payloadOriginalBytes: 10, payloadCapturedBytes: 10,
  payloadTruncated: false, binaryElisions: 0,
  usage: { reported: true, cacheReported: true, promptTokens: 100, cachedTokens: 75, candidateTokens: 20, thoughtTokens: 0, toolUseTokens: 0, totalTokens: 120 },
}

describe('CacheDiagnosticsPanel', () => {
  beforeEach(() => vi.mocked(fetchCacheDiagnostics).mockReset())

  it('renders loading then an empty state', async () => {
    vi.mocked(fetchCacheDiagnostics).mockResolvedValue({ sessionId: 's1', invocationId: 'inv-1', rounds: [] })
    render(<CacheDiagnosticsPanel sessionId="s1" invocationId="inv-1" />)
    expect(screen.getByText('Loading cache diagnostics…')).toBeInTheDocument()
    expect(await screen.findByText(/No retained cache diagnostics/)).toBeInTheDocument()
  })

  it('renders preparation chronologically before provider status and sanitized payload', async () => {
    const preparation = {
      ...round,
      kind: 'preparation' as const,
      stage: 'memory_embedding',
      status: 'failed' as const,
      call: 0,
      startedAt: '2026-08-28T23:59:59Z',
      duration: 500000,
      error: 'embedding unavailable',
    }
    vi.mocked(fetchCacheDiagnostics).mockResolvedValue({ sessionId: 's1', invocationId: 'inv-1', rounds: [{ ...round, payload: { prompt: '[REDACTED]' } }, preparation] })
    render(<CacheDiagnosticsPanel sessionId="s1" invocationId="inv-1" />)
    expect(await screen.findByText('Provider round 1')).toBeInTheDocument()
    expect(screen.getByText('Provider cache hit')).toBeInTheDocument()
    expect(screen.getByText('Memory Embedding')).toBeInTheDocument()
    expect(screen.getByText('Astonish preparation')).toBeInTheDocument()
    expect(screen.getByText('embedding unavailable')).toBeInTheDocument()
    const stages = screen.getAllByRole('heading', { level: 4 })
    expect(stages.map(stage => stage.textContent)).toEqual(['Memory Embedding', 'Provider round 1'])
    expect(screen.getByText('3 elements · 512 bytes')).toBeInTheDocument()
    expect(screen.getByText('75 cached / 100 input')).toBeInTheDocument()
    expect(screen.getByText('Sanitized payload')).toBeInTheDocument()
    expect(screen.getByText(/\[REDACTED\]/)).toBeInTheDocument()
  })
})
