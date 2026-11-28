import { vi } from 'vitest'
import type { SourceIntakeProposal, SourceIntakeRequest } from '@/api/botReviews'
import type { BotIntakeContent } from '@/api/botIntake'

/** Backend-shaped source proposal/intake endpoints, deliberately rejecting obsolete APIs. */
export function sourceBackend() {
  const proposals: SourceIntakeProposal[] = []
  const decisions: { digest: string; decision: string; reviewed_for_team: boolean }[] = []
  const saved = new Map<string, { revision: number; content: BotIntakeContent }>()
  let decisionError = '', decisionStatus = 503, saveError = ''
  const fetch = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost')
    const path = url.pathname
    const scope = url.searchParams.get('scope') === 'team' ? 'team' : 'personal'
    if (path === '/api/auth/me') return Response.json({ user: { id: 'owner' } })
    const body = init?.body ? JSON.parse(init.body as string) : undefined
    if (path.startsWith('/api/studio/bots/intake/')) {
      const id = path.split('/').at(-1)!
      if (init?.method === 'DELETE') { saved.delete(id); return new Response(null, { status: 204 }) }
      if (init?.method === 'PUT') {
        if (saveError) return new Response(saveError, { status: 503 })
        saved.set(id, { revision: (saved.get(id)?.revision || 0) + 1, content: body.content })
      }
      return Response.json({ id, owner_id: 'owner', scope, ...saved.get(id) })
    }
    if (path === '/api/studio/bots/source-proposals' && init?.method === 'POST') {
      const request = body as SourceIntakeRequest
      let proposal = proposals.find(p => p.request.idempotency_key === request.idempotency_key)
      if (!proposal) {
        proposal = { id: `proposal-${proposals.length}`, owner_id: 'owner', scope, request, source_hash: 'hash', source_updated_at: '2026-11-05T22:00:00Z', digest: `digest-${proposals.length}`, status: 'proposed', created_at: '2026-11-05T22:00:00Z' }
        proposals.push(proposal)
      }
      return Response.json(proposal)
    }
    if (/\/source-proposals\/[^/]+\/decision$/.test(path) && init?.method === 'POST') {
      decisions.push(body)
      if (decisionError) return new Response(decisionError, { status: decisionStatus })
      const proposal = proposals.find(p => path.includes(`/${p.id}/`))!
      return Response.json({ ...proposal, status: 'applied', bot_id: 'new' })
    }
    throw new Error(`Unexpected endpoint: ${init?.method || 'GET'} ${url}`)
  })
  vi.stubGlobal('fetch', fetch)
  return { fetch, proposals, decisions, saved, failDecision: (message: string, status = 503) => { decisionError = message; decisionStatus = status }, failSave: (message: string) => { saveError = message } }
}
