import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import MCPInspector from '../MCPInspector'
import MCPStoreModal from '../MCPStoreModal'
import ProviderModelSelector from '../ProviderModelSelector'
import { teamFetch } from '../../api/teamContext'

vi.mock('../../api/teamContext', () => ({
  teamFetch: vi.fn(),
}))

describe('ProviderModelSelector', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(teamFetch).mockResolvedValue({
      ok: true,
      json: async () => ({
        models: [
          { id: 'model-a', name: 'Model A', context_length: 128000, pricing: { prompt: '0.000001', completion: '0.000002' } },
          { id: 'model-b', name: 'Model B', context_length: 32000 },
        ],
      }),
    } as Response)
  })

  it('loads models and selects one', async () => {
    const user = userEvent.setup()
    const onSelect = vi.fn()
    const onClose = vi.fn()

    render(
      <ProviderModelSelector
        isOpen
        onClose={onClose}
        onSelect={onSelect}
        currentModel="model-b"
        provider="openrouter"
      />
    )

    expect(await screen.findByText('Select OpenRouter Model')).toBeInTheDocument()
    expect(await screen.findByText('Model A')).toBeInTheDocument()
    expect(screen.getByText('Model B')).toBeInTheDocument()

    await user.type(screen.getByPlaceholderText(/search models/i), 'Model A')
    expect(screen.getByText('Model A')).toBeInTheDocument()
    expect(screen.queryByText('Model B')).not.toBeInTheDocument()

    await user.click(screen.getByText('Model A'))
    expect(onSelect).toHaveBeenCalledWith('model-a')
    expect(onClose).toHaveBeenCalled()
  })
})

describe('MCPStoreModal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(teamFetch).mockImplementation(async (url, init) => {
      if (String(url).includes('/install')) {
        return { ok: true, json: async () => ({}) } as Response
      }
      return {
        ok: true,
        json: async () => ({
          servers: [{
            mcpId: 'github/server',
            name: 'GitHub Tools',
            author: 'astonish',
            description: 'GitHub integration tools',
            githubStars: 12,
            githubUrl: 'https://example.com/docs',
            tags: ['git'],
            config: { command: 'npx', env: { GITHUB_TOKEN: '' } },
          }],
          sources: ['official'],
        }),
      } as Response
    })
  })

  it('browses and installs an MCP server', async () => {
    const user = userEvent.setup()
    const onInstall = vi.fn()

    render(<MCPStoreModal isOpen onClose={vi.fn()} onInstall={onInstall} teamSlug="core" scope="team" />)

    expect(await screen.findByText('GitHub Tools')).toBeInTheDocument()
    await user.click(screen.getByText('GitHub Tools'))
    await user.type(screen.getByLabelText('GITHUB_TOKEN'), 'secret-token')
    await user.click(screen.getByRole('button', { name: /^install$/i }))

    await waitFor(() => {
      expect(teamFetch).toHaveBeenCalledWith(
        '/api/mcp-store/github/server/install?scope=team',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ env: { GITHUB_TOKEN: 'secret-token' } }),
        }),
        'core'
      )
    })
    expect(onInstall).toHaveBeenCalled()
    expect(await screen.findByText(/installed!/i)).toBeInTheDocument()
  })

  it('omits blank sensitive env overrides when installing an MCP server', async () => {
    const user = userEvent.setup()

    render(<MCPStoreModal isOpen onClose={vi.fn()} teamSlug="core" scope="team" />)

    expect(await screen.findByText('GitHub Tools')).toBeInTheDocument()
    await user.click(screen.getByText('GitHub Tools'))
    await user.click(screen.getByRole('button', { name: /^install$/i }))

    await waitFor(() => {
      expect(teamFetch).toHaveBeenCalledWith(
        '/api/mcp-store/github/server/install?scope=team',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ env: {} }),
        }),
        'core'
      )
    })
  })
})

describe('MCPInspector', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(teamFetch).mockImplementation(async (url, init) => {
      if (String(url).includes('/run')) {
        return {
          ok: true,
          json: async () => ({ success: true, result: { ok: true }, time_taken: '12ms' }),
        } as Response
      }
      return {
        ok: true,
        json: async () => ({
          tools: [
            {
              name: 'echo',
              description: 'Echo a message',
              parameters: { properties: { message: { type: 'string', description: 'Text to echo' } } },
            },
          ],
        }),
      } as Response
    })
  })

  it('loads tools and runs the selected tool', async () => {
    const user = userEvent.setup()

    render(<MCPInspector serverName="demo-server" onClose={vi.fn()} />)

    expect(await screen.findByText('Tool Inspector')).toBeInTheDocument()
    expect((await screen.findAllByText('echo')).length).toBeGreaterThanOrEqual(1)
    expect(screen.getByText('Echo a message')).toBeInTheDocument()

    await user.type(screen.getByLabelText(/message/i), 'hello')
    await user.click(screen.getByRole('button', { name: /run tool/i }))

    await waitFor(() => {
      expect(teamFetch).toHaveBeenCalledWith(
        '/api/mcp/demo-server/tools/echo/run',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ params: { message: 'hello' } }),
        }),
        undefined
      )
    })
    expect(await screen.findByText('Success')).toBeInTheDocument()
    expect(screen.getByText(/"ok": true/)).toBeInTheDocument()
  })

  it('shows MCP network grant prompt and retries after approval', async () => {
    const user = userEvent.setup()
    let toolFetches = 0
    vi.mocked(teamFetch).mockImplementation(async (url, init) => {
      const urlText = String(url)
      if (urlText.includes('/network-grants')) {
        return { ok: true, json: async () => ({ approved: true }) } as Response
      }
      if (urlText.includes('/tools') && !init) {
        toolFetches += 1
        if (toolFetches === 1) {
          return {
            ok: true,
            json: async () => ({
              error: 'Failed to list tools: npm ECONNRESET',
              network_authorization: {
                required: true,
                message: 'This MCP server needs outbound network access before Astonish can install or start it.',
                denials: [{ host: 'registry.npmjs.org', port: 443, broader_pattern: '*.npmjs.org' }],
              },
            }),
          } as Response
        }
        return {
          ok: true,
          json: async () => ({ tools: [{ name: 'echo', description: 'Echo a message' }] }),
        } as Response
      }
      return { ok: true, json: async () => ({}) } as Response
    })

    render(<MCPInspector serverName="context7" teamSlug="core" scope="team" onClose={vi.fn()} />)

    expect(await screen.findByText('Outbound network access is required')).toBeInTheDocument()
    expect(screen.getByText('registry.npmjs.org:443')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /grant access and retry/i }))

    await waitFor(() => {
      expect(teamFetch).toHaveBeenCalledWith(
        '/api/mcp/context7/network-grants?scope=team',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ host: 'registry.npmjs.org', port: 443 }),
        }),
        'core'
      )
    })
    expect(await screen.findByText('Echo a message')).toBeInTheDocument()
  })
})
