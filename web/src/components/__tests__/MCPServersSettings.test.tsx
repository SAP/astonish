import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import MCPServersSettings from '../settings/MCPServersSettings'

/**
 * Regression tests for MCPServersSettings scope prop forwarding.
 *
 * These tests verify that:
 * 1. The trash button on standard servers sends DELETE with the correct ?scope= param
 * 2. The MCPStoreModal receives the scope prop
 * 3. The MCPInspector receives the scope prop
 */

// Mock MCPStoreModal to capture props
let capturedStoreModalProps: Record<string, any> = {}
vi.mock('../MCPStoreModal', () => ({
  default: (props: any) => {
    capturedStoreModalProps = props
    return props.isOpen ? <div data-testid="mcp-store-modal" data-scope={props.scope} data-team={props.teamSlug}>MockStoreModal</div> : null
  }
}))

// Mock MCPInspector to capture props
let capturedInspectorProps: Record<string, any> = {}
vi.mock('../MCPInspector', () => ({
  default: (props: any) => {
    capturedInspectorProps = props
    return <div data-testid="mcp-inspector" data-scope={props.scope} data-team={props.teamSlug}>MockInspector</div>
  }
}))

describe('MCPServersSettings scope forwarding', () => {
  let fetchCalls: { url: string; method: string }[] = []
  const originalFetch = globalThis.fetch

  beforeEach(() => {
    fetchCalls = []
    capturedStoreModalProps = {}
    capturedInspectorProps = {}

    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : (input instanceof URL ? input.toString() : (input as Request).url)
      const method = init?.method?.toUpperCase() || 'GET'
      fetchCalls.push({ url, method })

      // Standard servers list
      if (url.includes('/api/standard-servers') && method === 'GET') {
        return new Response(JSON.stringify({ servers: [] }), {
          status: 200, headers: { 'Content-Type': 'application/json' }
        })
      }

      // Perplexity provider options
      if (url.includes('/api/web-search/perplexity/options')) {
        return new Response(JSON.stringify({ options: [
          { provider: 'SAP AI Core', type: 'openai_compat', models: ['perplexity/sonar-pro'] }
        ] }), {
          status: 200, headers: { 'Content-Type': 'application/json' }
        })
      }

      // MCP status
      if (url.includes('/api/mcp/status')) {
        return new Response(JSON.stringify({ servers: [
          { name: 'context7', status: 'healthy', tool_count: 3 }
        ] }), {
          status: 200, headers: { 'Content-Type': 'application/json' }
        })
      }

      // Standard server DELETE
      if (url.includes('/api/standard-servers/') && method === 'DELETE') {
        return new Response(JSON.stringify({ status: 'uninstalled' }), {
          status: 200, headers: { 'Content-Type': 'application/json' }
        })
      }

      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
    }) as typeof fetch
  })

  afterEach(() => {
    globalThis.fetch = originalFetch
  })

  it('passes scope="platform" to MCPStoreModal when opened from platform tab', async () => {
    const user = userEvent.setup()

    render(
      <MCPServersSettings
        mcpServers={{ context7: { command: 'npx', args: ['-y', '@upstash/context7-mcp'], transport: 'stdio' } }}
        setMcpServers={() => {}}
        mcpServerNames={{ context7: 'context7' }}
        setMcpServerNames={() => {}}
        mcpServerArgs={{ context7: '-y, @upstash/context7-mcp' }}
        setMcpServerArgs={() => {}}
        setMcpHasChanges={() => {}}
        standardServers={[]}
        saving={false}
        setSaving={() => {}}
        setSaveSuccess={() => {}}
        setError={() => {}}
        loadData={() => {}}
        setGeneralForm={() => {}}
        theme="dark"
        scope="platform"
      />
    )

    // Find and click the "Browse Store" button to open the modal
    const browseButton = await screen.findByText(/Browse Store/i)
    await user.click(browseButton)

    // Verify MCPStoreModal was rendered with scope="platform"
    expect(capturedStoreModalProps.scope).toBe('platform')
    expect(capturedStoreModalProps.teamSlug).toBeUndefined()
  })

  it('passes scope="platform" to MCPInspector when Test is clicked from platform tab', async () => {
    const user = userEvent.setup()

    render(
      <MCPServersSettings
        mcpServers={{ context7: { command: 'npx', args: ['-y', '@upstash/context7-mcp'], transport: 'stdio', enabled: true } }}
        setMcpServers={() => {}}
        mcpServerNames={{ context7: 'context7' }}
        setMcpServerNames={() => {}}
        mcpServerArgs={{ context7: '-y, @upstash/context7-mcp' }}
        setMcpServerArgs={() => {}}
        setMcpHasChanges={() => {}}
        standardServers={[]}
        saving={false}
        setSaving={() => {}}
        setSaveSuccess={() => {}}
        setError={() => {}}
        loadData={() => {}}
        setGeneralForm={() => {}}
        theme="dark"
        scope="platform"
      />
    )

    // Find and click the "Test" button on the server card
    const testButton = await screen.findByTitle('Test tools from this server')
    await user.click(testButton)

    // Verify MCPInspector was rendered with scope="platform"
    expect(capturedInspectorProps.scope).toBe('platform')
    expect(capturedInspectorProps.serverName).toBe('context7')
    expect(capturedInspectorProps.teamSlug).toBeUndefined()
  })

  it('loads Perplexity provider options with platform scope during initial setup', async () => {
    const user = userEvent.setup()

    render(
      <MCPServersSettings
        mcpServers={{}}
        setMcpServers={() => {}}
        mcpServerNames={{}}
        setMcpServerNames={() => {}}
        mcpServerArgs={{}}
        setMcpServerArgs={() => {}}
        setMcpHasChanges={() => {}}
        standardServers={[
          { id: 'perplexity', displayName: 'Perplexity / Sonar', installed: false, isDefault: false, envVars: [], capabilities: { webSearch: true, webExtract: false }, kind: 'model' }
        ]}
        saving={false}
        setSaving={() => {}}
        setSaveSuccess={() => {}}
        setError={() => {}}
        loadData={() => {}}
        setGeneralForm={() => {}}
        theme="dark"
        scope="platform"
      />
    )

    await user.click(await screen.findByText('Setup'))

    await waitFor(() => {
      const optionsCall = fetchCalls.find(c => c.url.includes('/api/web-search/perplexity/options'))
      expect(optionsCall).toBeDefined()
      expect(optionsCall!.url).toContain('?scope=platform')
    })
    expect(await screen.findByText(/Setup Perplexity/i)).toBeInTheDocument()
    expect(await screen.findByDisplayValue('SAP AI Core')).toBeInTheDocument()
    expect(screen.getByDisplayValue('perplexity/sonar-pro')).toBeInTheDocument()
  })

  it('shows multiple configured providers without forcing a single active card', async () => {
    render(
      <MCPServersSettings
        mcpServers={{}}
        setMcpServers={() => {}}
        mcpServerNames={{}}
        setMcpServerNames={() => {}}
        mcpServerArgs={{}}
        setMcpServerArgs={() => {}}
        setMcpHasChanges={() => {}}
        standardServers={[
          {
            id: 'perplexity',
            displayName: 'Perplexity / Sonar',
            installed: true,
            isDefault: false,
            envVars: [],
            capabilities: { webSearch: true, webExtract: false },
            kind: 'model',
            details: { provider: 'SAP AI Core', model: 'perplexity/sonar-pro' },
          },
          {
            id: 'tavily',
            displayName: 'Tavily',
            installed: true,
            isDefault: true,
            envVars: [{ name: 'TAVILY_API_KEY', required: true }],
            capabilities: { webSearch: true, webExtract: true },
          },
        ]}
        saving={false}
        setSaving={() => {}}
        setSaveSuccess={() => {}}
        setError={() => {}}
        loadData={() => {}}
        setGeneralForm={() => {}}
        theme="dark"
        scope="platform"
      />
    )

    expect(await screen.findByText(/2 configured/i)).toBeInTheDocument()
    expect(screen.getByText(/General → Web Tools/i)).toBeInTheDocument()
    expect(screen.getAllByText('Configured').length).toBe(2)
    expect(screen.queryByText('Make active')).not.toBeInTheDocument()
    expect(screen.getByText(/perplexity\/sonar-pro/i)).toBeInTheDocument()
  })

  it('trash button sends DELETE with ?scope=platform for platform tab', async () => {
    const user = userEvent.setup()
    const loadData = vi.fn()

    render(
      <MCPServersSettings
        mcpServers={{}}
        setMcpServers={() => {}}
        mcpServerNames={{}}
        setMcpServerNames={() => {}}
        mcpServerArgs={{}}
        setMcpServerArgs={() => {}}
        setMcpHasChanges={() => {}}
        standardServers={[
          { id: 'tavily', displayName: 'Tavily', installed: true, isDefault: false, envVars: [{ name: 'TAVILY_API_KEY', required: true }], capabilities: { webSearch: true, webExtract: false } }
        ]}
        saving={false}
        setSaving={() => {}}
        setSaveSuccess={() => {}}
        setError={() => {}}
        loadData={loadData}
        setGeneralForm={() => {}}
        theme="dark"
        scope="platform"
      />
    )

    // Find the trash button for Tavily (it has title="Remove configuration")
    const trashButton = await screen.findByTitle('Remove configuration')
    await user.click(trashButton)

    // Wait for the DELETE to be sent
    await waitFor(() => {
      const deleteCall = fetchCalls.find(c => c.method === 'DELETE' && c.url.includes('standard-servers'))
      expect(deleteCall).toBeDefined()
      expect(deleteCall!.url).toContain('tavily')
      expect(deleteCall!.url).toContain('?scope=platform')
    })
  })
})
