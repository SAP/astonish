import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import OAuthTab from '../OAuthTab'
import * as adminApi from '../../../api/platformAdmin'

vi.mock('../../../api/platformAdmin', () => ({
  getOAuthDiscovery: vi.fn(),
  listOAuthClients: vi.fn(),
  createOAuthClient: vi.fn(),
  updateOAuthClient: vi.fn(),
}))

const discovery = {
  issuer: 'https://studio.example', resource: 'https://studio.example/api/mcp',
  authorization_endpoint: 'https://studio.example/oauth/authorize', token_endpoint: 'https://studio.example/oauth/token',
  jwks_uri: 'https://studio.example/oauth/jwks', revocation_endpoint: 'https://studio.example/oauth/revoke', introspection_endpoint: 'https://studio.example/oauth/introspect', a2a_endpoint: 'https://studio.example/api/a2a',
  scopes: [
    { value: 'tool:execute', label: 'MCP access', description: 'Allows MCP.' },
    { value: 'chat', label: 'Chat access', description: 'Allows chat.' },
  ],
}

describe('platform OAuthTab', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
    vi.mocked(adminApi.getOAuthDiscovery).mockResolvedValue(discovery)
    vi.mocked(adminApi.listOAuthClients).mockResolvedValue([])
  })

  it('shows and copies the protected A2A endpoint', async () => {
    const user = userEvent.setup()
    render(<OAuthTab />)

    expect(await screen.findByText(discovery.a2a_endpoint)).toBeInTheDocument()
    await user.click(screen.getByTitle('Copy A2A endpoint'))
    expect(await screen.findByText('A2A endpoint copied')).toBeInTheDocument()
  })

  it('selects both catalog permissions by default and sends only checked values', async () => {
    const user = userEvent.setup()
    vi.mocked(adminApi.createOAuthClient).mockResolvedValue({ client: { client_id: 'ast_client' } } as never)
    render(<OAuthTab />)
    await user.click(await screen.findByRole('button', { name: /add client/i }))
    expect(screen.getByRole('checkbox', { name: 'MCP access' })).toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Chat access' })).toBeChecked()
    await user.click(screen.getByRole('checkbox', { name: 'Chat access' }))
    await user.type(screen.getByLabelText('Name'), 'MCP client')
    await user.type(screen.getByLabelText('Allowed resources'), 'https://studio.example/api/mcp')
    await user.click(screen.getByRole('button', { name: 'Create' }))
    expect(adminApi.createOAuthClient).toHaveBeenCalledWith(expect.objectContaining({ scopes: ['tool:execute'] }))
  })
})
