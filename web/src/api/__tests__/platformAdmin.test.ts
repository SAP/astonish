import { afterEach, describe, expect, it, vi } from 'vitest'

import { createOAuthClient, getOAuthDiscovery, listOAuthClients, updateOAuthClient } from '../platformAdmin'

const originalFetch = globalThis.fetch

function mockFetch(data: unknown, ok = true) {
  return vi.fn().mockResolvedValue({ ok, statusText: 'error', json: () => Promise.resolve(data) })
}

afterEach(() => { globalThis.fetch = originalFetch })

describe('OAuth platform administration API', () => {
  it('loads discovery and lists secret-free clients through the admin transport', async () => {
    globalThis.fetch = mockFetch({ Issuer: 'https://issuer.example', Resource: 'https://api.example' })
    await expect(getOAuthDiscovery()).resolves.toEqual(expect.objectContaining({
      issuer: 'https://issuer.example',
      resource: 'https://api.example',
    }))
    expect(globalThis.fetch).toHaveBeenCalledWith('/api/platform/admin/oauth/discovery', expect.objectContaining({ credentials: 'include' }))

    globalThis.fetch = mockFetch({ clients: [{ client_id: 'ast_client', active: true }] })
    await expect(listOAuthClients()).resolves.toEqual([{ client_id: 'ast_client', active: true }])
    expect(globalThis.fetch).toHaveBeenCalledWith('/api/platform/admin/oauth/clients', expect.objectContaining({ credentials: 'include' }))
  })

  it('creates and rotates clients with typed JSON requests', async () => {
    const input = { name: 'MCP', client_type: 'confidential' as const, redirect_uris: [], grant_types: ['client_credentials'], resources: ['https://api.example'], scopes: ['tool:execute'], active: true }
    globalThis.fetch = mockFetch({ client: { client_id: 'ast_client' }, client_secret: 'shown-once' })
    await expect(createOAuthClient(input)).resolves.toMatchObject({ client_secret: 'shown-once' })
    expect(globalThis.fetch).toHaveBeenCalledWith('/api/platform/admin/oauth/clients', expect.objectContaining({ method: 'POST', body: JSON.stringify(input) }))

    const rotate = { ...input, rotate_secret: true }
    globalThis.fetch = mockFetch({ client: { client_id: 'ast_client' }, client_secret: 'rotated-once' })
    await expect(updateOAuthClient('ast_client', rotate)).resolves.toMatchObject({ client_secret: 'rotated-once' })
    expect(globalThis.fetch).toHaveBeenCalledWith('/api/platform/admin/oauth/clients/ast_client', expect.objectContaining({ method: 'PATCH', body: JSON.stringify(rotate) }))
  })
})
