import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'

import { fetchKnowledgeDebug } from '@/api/studioChat'
import KnowledgeDebugPanel from '../KnowledgeDebugPanel'

vi.mock('@/api/studioChat', () => ({
  fetchKnowledgeDebug: vi.fn(),
}))

const baseResult = {
  path: 'Deploy K8s pods with kubectl',
  score: 0.85,
  category: 'knowledge',
  id: 'mem-123',
  scope: 'team',
  created_by: 'user-1',
  created_at: '2025-01-15T10:00:00Z',
  session_id: 'sess-abc',
}

describe('KnowledgeDebugPanel', () => {
  beforeEach(() => {
    vi.mocked(fetchKnowledgeDebug).mockReset()
  })

  it('renders loading then an empty state', async () => {
    vi.mocked(fetchKnowledgeDebug).mockResolvedValue({
      sessionId: 's1',
      invocationId: 'inv1',
      knowledge: null,
      tools: null,
    })

    render(<KnowledgeDebugPanel sessionId="s1" invocationId="inv1" />)
    expect(screen.getByText('Loading knowledge debug…')).toBeTruthy()
    expect(await screen.findByText(/No knowledge or tool injection data/)).toBeTruthy()
  })

  it('renders knowledge results with score, category, and scope', async () => {
    const guidanceResult = {
      ...baseResult,
      path: 'Always use --dry-run for kubectl apply',
      score: 0.92,
      category: 'guidance',
      scope: 'personal',
    }

    vi.mocked(fetchKnowledgeDebug).mockResolvedValue({
      sessionId: 's1',
      invocationId: 'inv1',
      knowledge: {
        type: 'knowledge',
        query: 'deploy kubernetes pods',
        bm25_query_len: 42,
        results: [guidanceResult, baseResult],
        result_count: 2,
        estimated_tokens: 150,
      },
      tools: null,
    })

    render(<KnowledgeDebugPanel sessionId="s1" invocationId="inv1" />)

    // Wait for content to load
    expect(await screen.findByText('Knowledge injection')).toBeTruthy()

    // Verify type badge (appears as type badge and as category on second result)
    expect(screen.getAllByText('knowledge').length).toBeGreaterThanOrEqual(1)

    // Verify query display
    expect(screen.getByText('deploy kubernetes pods')).toBeTruthy()

    // Verify token estimate
    expect(screen.getByText('~150 tokens')).toBeTruthy()

    // Verify result count
    expect(screen.getByText('2 results')).toBeTruthy()

    // Verify result paths
    expect(screen.getByText('Always use --dry-run for kubectl apply')).toBeTruthy()
    expect(screen.getByText('Deploy K8s pods with kubectl')).toBeTruthy()

    // Verify scores
    expect(screen.getByText('92%')).toBeTruthy()
    expect(screen.getByText('85%')).toBeTruthy()

    // Verify categories
    expect(screen.getByText('guidance')).toBeTruthy()

    // Verify scopes
    expect(screen.getByText('personal')).toBeTruthy()
    expect(screen.getByText('team')).toBeTruthy()
  })

  it('renders tool discovery section when tools are present', async () => {
    vi.mocked(fetchKnowledgeDebug).mockResolvedValue({
      sessionId: 's1',
      invocationId: 'inv1',
      knowledge: null,
      tools: {
        query: 'search kubernetes',
        results: [
          { name: 'kubectl_apply', group: 'k8s', score: 0.9 },
          { name: 'helm_install', group: 'k8s', score: 0.7 },
        ],
        result_count: 2,
      },
    })

    render(<KnowledgeDebugPanel sessionId="s1" invocationId="inv1" />)

    expect(await screen.findByText('Tool discovery')).toBeTruthy()
    expect(screen.getByText('kubectl_apply')).toBeTruthy()
    expect(screen.getByText('helm_install')).toBeTruthy()
    expect(screen.getByText('2 tools')).toBeTruthy()
  })

  it('renders error state', async () => {
    vi.mocked(fetchKnowledgeDebug).mockRejectedValue(new Error('Network failure'))

    render(<KnowledgeDebugPanel sessionId="s1" invocationId="inv1" />)

    expect(await screen.findByText('Network failure')).toBeTruthy()
  })
})
