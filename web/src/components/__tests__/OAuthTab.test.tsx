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
})
