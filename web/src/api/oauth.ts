export interface OAuthDiscovery {
  issuer: string
  resource: string
  authorization_endpoint: string
  token_endpoint: string
  jwks_uri: string
  revocation_endpoint: string
  introspection_endpoint: string
  a2a_endpoint: string
  scopes: OAuthScopeOption[]
}

export interface OAuthScopeOption {
  value: string
  label: string
  description: string
}

export interface OAuthClient {
  id: string
  org_id: string
  team_id: string
  client_id: string
  name: string
  client_type: 'public' | 'confidential'
  redirect_uris: string[]
  grant_types: string[]
  resources: string[]
  scopes: string[]
  active: boolean
  created_at: string
  updated_at: string
}

export interface OAuthClientInput {
  name: string
  client_type: 'public' | 'confidential'
  org_id?: string
  team_id?: string
  redirect_uris: string[]
  grant_types: string[]
  resources: string[]
  scopes: string[]
  active: boolean
  rotate_secret?: boolean
}

export interface OAuthClientWriteResponse {
  client: OAuthClient
  client_secret: string
}

async function oauthFetch(input: string, init?: RequestInit): Promise<Response> {
  const headers = new Headers(init?.headers)
  headers.set('X-Requested-With', 'XMLHttpRequest')
  const request = { credentials: 'include' as const, ...init, headers }
  let response = await fetch(input, request)
  if (response.status !== 401) return response

  const refresh = await fetch('/api/auth/refresh', {
    method: 'POST',
    credentials: 'include',
    headers: { 'X-Requested-With': 'XMLHttpRequest' },
  })
  if (!refresh.ok) return response

  response = await fetch(input, request)
  return response
}

async function throwIfNotOk(res: Response, fallback: string): Promise<void> {
  if (res.ok) return
  const body = await res.json().catch(() => ({})) as Record<string, unknown>
  if (res.status === 401) {
    throw new Error('Your session expired. Sign in again, then reopen OAuth settings.')
  }
  throw new Error((body.error as string) || fallback)
}

export interface OAuthContext {
  id: string
  name: string
  slug: string
  teams: { id: string; name: string }[]
}

export async function listOAuthContexts(): Promise<OAuthContext[]> {
  const res = await oauthFetch('/api/oauth/contexts')
  await throwIfNotOk(res, 'Failed to load OAuth organization and team choices')
  const data = await res.json()
  return data.organizations || []
}

export async function getOAuthDiscovery(): Promise<OAuthDiscovery> {
  const res = await oauthFetch('/api/oauth/discovery')
  await throwIfNotOk(res, 'Failed to load OAuth discovery')
  return res.json()
}

export async function listOAuthClients(): Promise<OAuthClient[]> {
  const res = await oauthFetch('/api/oauth/clients')
  await throwIfNotOk(res, 'Failed to list OAuth clients')
  const data = await res.json()
  return data.clients || []
}

export async function createOAuthClient(input: OAuthClientInput): Promise<OAuthClientWriteResponse> {
  const res = await oauthFetch('/api/oauth/clients', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input),
  })
  await throwIfNotOk(res, 'Failed to create OAuth client')
  return res.json()
}

export async function updateOAuthClient(clientID: string, input: OAuthClientInput): Promise<OAuthClientWriteResponse> {
  const res = await oauthFetch(`/api/oauth/clients/${encodeURIComponent(clientID)}`, {
    method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input),
  })
  await throwIfNotOk(res, 'Failed to update OAuth client')
  return res.json()
}
