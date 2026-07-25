/**
 * API client for Platform Administration.
 * Used by the PlatformAdminPanel component (superadmins only).
 */

const ADMIN_BASE = '/api/platform/admin'

// Wrapper for admin API requests — always includes credentials and CSRF header.
async function adminFetch(input: string, init?: Parameters<typeof fetch>[1]): Promise<Response> {
  const headers = new Headers(init?.headers)
  if (!headers.has('X-Requested-With')) {
    headers.set('X-Requested-With', 'XMLHttpRequest')
  }
  return fetch(input, { credentials: 'include', ...init, headers })
}

// Throw a descriptive error if the response is not ok.
// Attempts to parse a JSON error body; falls back to the provided message.
async function throwIfNotOk(res: Response, fallbackMsg: string): Promise<void> {
  if (res.ok) return
  const body = await res.json().catch(() => ({})) as Record<string, unknown>
  throw new Error((body.error as string) || fallbackMsg)
}

// --- Types ---

export interface AdminOrg {
  id: string
  name: string
  slug: string
  status: string
  created_at: string
  member_count: number
  team_count: number
}

export interface AdminOrgDetail {
  organization: {
    id: string
    name: string
    slug: string
    db_name: string
    status: string
    created_at: string
  }
  members: AdminUserWithRole[]
  teams: AdminTeam[]
}

export interface AdminUserWithRole {
  id: string
  email: string
  display_name: string
  role: string
}

export interface AdminTeam {
  id: string
  name: string
  slug: string
  schema_name: string
  created_at: string
}

export interface OrgMembership {
  org_id: string
  org_name?: string
  org_slug?: string
  role: string
}

export interface AdminUser {
  id: string
  email: string
  display_name: string
  platform_role: string
  status: string
  created_at: string
  orgs: OrgMembership[]
}

// --- Organization API ---

export async function listOrgs(): Promise<AdminOrg[]> {
  const res = await adminFetch(`${ADMIN_BASE}/orgs`, { credentials: 'include' })
  await throwIfNotOk(res, 'Failed to list organizations')
  const data = await res.json()
  return data.organizations
}

export async function createOrg(params: {
  name: string
  slug?: string
  owner_email?: string
}): Promise<{ organization: AdminOrg; message: string }> {
  const res = await adminFetch(`${ADMIN_BASE}/orgs`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(params),
  })
  await throwIfNotOk(res, 'Failed to create organization')
  return res.json()
}

export async function getOrg(slug: string): Promise<AdminOrgDetail> {
  const res = await adminFetch(`${ADMIN_BASE}/orgs/${encodeURIComponent(slug)}`, {
    credentials: 'include',
  })
  await throwIfNotOk(res, 'Organization not found')
  return res.json()
}

export async function updateOrg(
  slug: string,
  params: { name?: string; status?: string }
): Promise<{ organization: AdminOrg }> {
  const res = await adminFetch(`${ADMIN_BASE}/orgs/${encodeURIComponent(slug)}`, {
    method: 'PATCH',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(params),
  })
  await throwIfNotOk(res, 'Failed to update organization')
  return res.json()
}

export async function deleteOrg(slug: string): Promise<void> {
  const res = await adminFetch(`${ADMIN_BASE}/orgs/${encodeURIComponent(slug)}`, {
    method: 'DELETE',
    credentials: 'include',
  })
  await throwIfNotOk(res, 'Failed to delete organization')
}

// --- Org Team Management API ---

export async function createOrgTeam(
  orgSlug: string,
  params: { name: string; slug?: string }
): Promise<AdminTeam> {
  const res = await adminFetch(`${ADMIN_BASE}/orgs/${encodeURIComponent(orgSlug)}/teams`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(params),
  })
  await throwIfNotOk(res, 'Failed to create team')
  return res.json()
}

export async function deleteOrgTeam(orgSlug: string, teamSlug: string): Promise<void> {
  const res = await adminFetch(
    `${ADMIN_BASE}/orgs/${encodeURIComponent(orgSlug)}/teams/${encodeURIComponent(teamSlug)}`,
    { method: 'DELETE', credentials: 'include' }
  )
  await throwIfNotOk(res, 'Failed to delete team')
}

export interface TeamMemberDetail {
  user_id: string
  team_id: string
  role: string
  joined_at: string
  email: string
  display_name: string
}

export async function listOrgTeamMembers(
  orgSlug: string,
  teamSlug: string
): Promise<TeamMemberDetail[]> {
  const res = await adminFetch(
    `${ADMIN_BASE}/orgs/${encodeURIComponent(orgSlug)}/teams/${encodeURIComponent(teamSlug)}/members`,
    { credentials: 'include' }
  )
  await throwIfNotOk(res, 'Failed to list team members')
  const data = await res.json()
  return data.members
}

export async function addOrgTeamMember(
  orgSlug: string,
  teamSlug: string,
  params: { email: string; role?: string }
): Promise<void> {
  const res = await adminFetch(
    `${ADMIN_BASE}/orgs/${encodeURIComponent(orgSlug)}/teams/${encodeURIComponent(teamSlug)}/members`,
    {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(params),
    }
  )
  await throwIfNotOk(res, 'Failed to add member to team')
}

export async function removeOrgTeamMember(
  orgSlug: string,
  teamSlug: string,
  userId: string
): Promise<void> {
  const res = await adminFetch(
    `${ADMIN_BASE}/orgs/${encodeURIComponent(orgSlug)}/teams/${encodeURIComponent(teamSlug)}/members/${encodeURIComponent(userId)}`,
    { method: 'DELETE', credentials: 'include' }
  )
  await throwIfNotOk(res, 'Failed to remove member from team')
}

export async function setOrgTeamMemberRole(
  orgSlug: string,
  teamSlug: string,
  userId: string,
  role: string
): Promise<void> {
  const res = await adminFetch(
    `${ADMIN_BASE}/orgs/${encodeURIComponent(orgSlug)}/teams/${encodeURIComponent(teamSlug)}/members/${encodeURIComponent(userId)}/role`,
    {
      method: 'PUT',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ role }),
    }
  )
  await throwIfNotOk(res, 'Failed to update member role')
}

// --- User API ---

export async function listUsers(): Promise<AdminUser[]> {
  const res = await adminFetch(`${ADMIN_BASE}/users`, { credentials: 'include' })
  await throwIfNotOk(res, 'Failed to list users')
  const data = await res.json()
  return data.users
}

export async function createUser(params: {
  email: string
  display_name: string
  password?: string
}): Promise<{ user: AdminUser; message: string }> {
  const res = await adminFetch(`${ADMIN_BASE}/users`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(params),
  })
  await throwIfNotOk(res, 'Failed to create user')
  return res.json()
}

export async function getUser(id: string): Promise<{ user: AdminUser; orgs: OrgMembership[] }> {
  const res = await adminFetch(`${ADMIN_BASE}/users/${encodeURIComponent(id)}`, {
    credentials: 'include',
  })
  await throwIfNotOk(res, 'User not found')
  return res.json()
}

export async function updateUser(
  id: string,
  params: {
    display_name?: string
    status?: string
    platform_role?: string
    password?: string
  }
): Promise<{ user: AdminUser }> {
  const res = await adminFetch(`${ADMIN_BASE}/users/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(params),
  })
  await throwIfNotOk(res, 'Failed to update user')
  return res.json()
}

export async function deleteUser(id: string): Promise<void> {
  const res = await adminFetch(`${ADMIN_BASE}/users/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    credentials: 'include',
  })
  await throwIfNotOk(res, 'Failed to delete user')
}

export async function addUserToOrg(
  userId: string,
  params: { org_slug: string; role?: string; team_slug?: string }
): Promise<void> {
  const res = await adminFetch(`${ADMIN_BASE}/users/${encodeURIComponent(userId)}/orgs`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(params),
  })
  await throwIfNotOk(res, 'Failed to add user to org')
}

export async function removeUserFromOrg(userId: string, orgSlug: string): Promise<void> {
  const res = await adminFetch(
    `${ADMIN_BASE}/users/${encodeURIComponent(userId)}/orgs/${encodeURIComponent(orgSlug)}`,
    { method: 'DELETE', credentials: 'include' }
  )
  await throwIfNotOk(res, 'Failed to remove user from org')
}

// --- Channel Configuration Management ---

export interface ChannelSecretInfo {
  key: string
  label: string
  configured: boolean
}

export interface ChannelFullInfo {
  type: string
  description: string
  enabled: boolean
  config: Record<string, any>
  secrets: ChannelSecretInfo[]
  secrets_configured: boolean
}

export async function listChannels(): Promise<ChannelFullInfo[]> {
  const res = await adminFetch(`${ADMIN_BASE}/channels`, { credentials: 'include' })
  await throwIfNotOk(res, 'Failed to list channels')
  return res.json()
}

export async function saveChannel(
  channelType: string,
  payload: { enabled: boolean; config: Record<string, any>; secrets: Record<string, string> }
): Promise<{ message: string }> {
  const res = await adminFetch(`${ADMIN_BASE}/channels/${encodeURIComponent(channelType)}`, {
    method: 'PUT',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
  await throwIfNotOk(res, 'Failed to save channel configuration')
  return res.json()
}

export async function deleteChannel(channelType: string): Promise<{ message: string }> {
  const res = await adminFetch(`${ADMIN_BASE}/channels/${encodeURIComponent(channelType)}`, {
    method: 'DELETE',
    credentials: 'include',
  })
  await throwIfNotOk(res, 'Failed to delete channel')
  return res.json()
}

export async function testEmailConnection(secrets?: Record<string, string>): Promise<{ status: string; message: string; email?: string; displayName?: string }> {
  const res = await adminFetch(`${ADMIN_BASE}/channels/email/test`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ secrets: secrets || {} }),
  })
  await throwIfNotOk(res, 'Failed to test email connection')
  return res.json()
}

// --- Web Services (Standard MCP Servers) ---

export interface WebServiceInfo {
  id: string
  name: string
  description: string
  category: string
  configured: boolean
  secret_key: string
}

export async function listWebServices(): Promise<WebServiceInfo[]> {
  const res = await adminFetch(`${ADMIN_BASE}/web-services`, { credentials: 'include' })
  await throwIfNotOk(res, 'Failed to list web services')
  return res.json()
}

export async function setWebServiceKey(
  id: string,
  apiKey: string
): Promise<{ message: string }> {
  const res = await adminFetch(`${ADMIN_BASE}/web-services/${encodeURIComponent(id)}`, {
    method: 'PUT',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ api_key: apiKey }),
  })
  await throwIfNotOk(res, 'Failed to save web service key')
  return res.json()
}

export async function deleteWebService(id: string): Promise<void> {
  const res = await adminFetch(`${ADMIN_BASE}/web-services/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    credentials: 'include',
  })
  await throwIfNotOk(res, 'Failed to delete web service')
}

// --- OIDC Provider API ---

export interface OIDCProvider {
  id: string
  org_id?: string
  name: string
  issuer_url: string
  discovery_url?: string
  client_id: string
  client_secret?: string
  scopes: string[]
  team_claim?: string
  enabled: boolean
  created_at: string
}

export async function listOIDCProviders(): Promise<OIDCProvider[]> {
  const res = await adminFetch(`${ADMIN_BASE}/oidc-providers`, { credentials: 'include' })
  await throwIfNotOk(res, 'Failed to fetch OIDC providers')
  const data = await res.json()
  return data.providers || []
}

export async function createOIDCProvider(params: {
  name: string
  issuer_url: string
  discovery_url?: string
  client_id: string
  client_secret: string
  scopes?: string[]
  team_claim?: string
  enabled?: boolean
}): Promise<OIDCProvider> {
  const res = await adminFetch(`${ADMIN_BASE}/oidc-providers`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(params),
  })
  await throwIfNotOk(res, 'Failed to create OIDC provider')
  const data = await res.json()
  return data.provider
}

export async function updateOIDCProvider(
  id: string,
  params: Partial<Omit<OIDCProvider, 'id' | 'created_at'>>
): Promise<OIDCProvider> {
  const res = await adminFetch(`${ADMIN_BASE}/oidc-providers/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(params),
  })
  await throwIfNotOk(res, 'Failed to update OIDC provider')
  const data = await res.json()
  return data.provider
}

export async function deleteOIDCProvider(id: string): Promise<void> {
  const res = await adminFetch(`${ADMIN_BASE}/oidc-providers/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    credentials: 'include',
  })
  await throwIfNotOk(res, 'Failed to delete OIDC provider')
}

// --- Auth Settings (Registration Policy) ---

export interface PlatformAuthSettings {
  allow_registration: boolean
  require_email_verification: boolean
  dev_environment: boolean
}

export async function getPlatformAuthSettings(): Promise<PlatformAuthSettings> {
  const res = await adminFetch(`${ADMIN_BASE}/auth-settings`)
  await throwIfNotOk(res, 'Failed to load auth settings')
  return res.json()
}

export async function savePlatformAuthSettings(
  settings: Partial<PlatformAuthSettings>
): Promise<PlatformAuthSettings> {
  const res = await adminFetch(`${ADMIN_BASE}/auth-settings`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(settings),
  })
  await throwIfNotOk(res, 'Failed to save auth settings')
  return res.json()
}

// --- Brand theme (platform default for login + user inherit) ---

export interface PlatformBrandTheme {
  default_brand_theme: string
}

export async function getPlatformBrandTheme(): Promise<PlatformBrandTheme> {
  const res = await adminFetch(`${ADMIN_BASE}/brand-theme`)
  await throwIfNotOk(res, 'Failed to load platform brand theme')
  return res.json()
}

export async function savePlatformBrandTheme(defaultBrandTheme: string): Promise<PlatformBrandTheme> {
  const res = await adminFetch(`${ADMIN_BASE}/brand-theme`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ default_brand_theme: defaultBrandTheme }),
  })
  await throwIfNotOk(res, 'Failed to save platform brand theme')
  return res.json()
}
