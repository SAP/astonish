import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import OAuthSettings from '../settings/OAuthSettings'
import * as oauthApi from '../../api/oauth'

vi.mock('../../api/oauth', () => ({
  getOAuthDiscovery: vi.fn(),
  listOAuthClients: vi.fn(),
  listOAuthContexts: vi.fn(),
  createOAuthClient: vi.fn(),
  updateOAuthClient: vi.fn(),
  deleteOAuthClient: vi.fn(),
}))

describe('OAuthSettings', () => {
  const discovery = {
    issuer: 'https://studio.example',
    resource: 'https://studio.example/api/mcp',
    authorization_endpoint: 'https://studio.example/oauth/authorize',
    token_endpoint: 'https://studio.example/oauth/token',
    jwks_uri: 'https://studio.example/oauth/jwks',
    revocation_endpoint: 'https://studio.example/oauth/revoke',
    introspection_endpoint: 'https://studio.example/oauth/introspect',
  }

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(oauthApi.getOAuthDiscovery).mockResolvedValue(discovery as never)
    vi.mocked(oauthApi.listOAuthClients).mockResolvedValue([])
    vi.mocked(oauthApi.listOAuthContexts).mockResolvedValue([])
  })

  it('renders and copies every endpoint returned by the authenticated personal discovery API', async () => {
    const user = userEvent.setup()
    render(<OAuthSettings />)

    expect(await screen.findByText(discovery.issuer)).toBeInTheDocument()
    expect(screen.getByText(discovery.authorization_endpoint)).toBeInTheDocument()
    expect(screen.getByText(discovery.token_endpoint)).toBeInTheDocument()
    expect(screen.getByText(discovery.jwks_uri)).toBeInTheDocument()
    expect(screen.getByText(discovery.resource)).toBeInTheDocument()

    await user.click(screen.getByTitle('Copy MCP resource'))
    expect(await screen.findByText('MCP resource copied')).toBeInTheDocument()
  })

  it('confirms deletion and removes the client card after the owner-scoped request succeeds', async () => {
    const user = userEvent.setup()
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    vi.mocked(oauthApi.listOAuthClients).mockResolvedValue([{ id: '1', client_id: 'ast_client', name: 'Joule', active: true, client_type: 'public', org_id: 'org', team_id: 'team', redirect_uris: [], grant_types: ['authorization_code'], resources: [], scopes: [], created_at: '', updated_at: '' }])
    vi.mocked(oauthApi.deleteOAuthClient).mockResolvedValue()
    render(<OAuthSettings />)

    expect(await screen.findByText('Joule')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /delete/i }))

    expect(oauthApi.deleteOAuthClient).toHaveBeenCalledWith('ast_client')
    expect(screen.queryByText('Joule')).not.toBeInTheDocument()
    expect(await screen.findByText('OAuth client Joule deleted')).toBeInTheDocument()
  })
})
