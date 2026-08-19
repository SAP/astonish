import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, act, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import SettingsPage from '../SettingsPage'

// Mock all child settings components to avoid deep dependency trees
vi.mock('../settings/GeneralSettings', () => ({ default: () => <div data-testid="general-settings">GeneralSettings</div> }))
vi.mock('../settings/ProvidersSettings', () => ({ default: (props: any) => <div data-testid="providers-settings">ProvidersSettings</div> }))
vi.mock('../settings/MCPServersSettings', () => ({ default: () => <div data-testid="mcp-settings">MCPServersSettings</div> }))
vi.mock('../settings/TapsSettings', () => ({ default: () => <div data-testid="taps-settings">TapsSettings</div> }))
vi.mock('../FlowStorePanel', () => ({ default: () => <div data-testid="flow-store">FlowStorePanel</div> }))
vi.mock('../settings/CredentialsSettings', () => ({ default: () => <div data-testid="credentials-settings">CredentialsSettings</div> }))
vi.mock('../settings/ChatSettings', () => ({ default: () => <div data-testid="chat-settings">ChatSettings</div> }))
vi.mock('../settings/BrowserSettings', () => ({ default: () => <div data-testid="browser-settings">BrowserSettings</div> }))
vi.mock('../settings/ChannelsSettings', () => ({ default: () => <div data-testid="channels-settings">ChannelsSettings</div> }))
vi.mock('../settings/SessionsSettings', () => ({ default: () => <div data-testid="sessions-settings">SessionsSettings</div> }))
vi.mock('../settings/MemorySettings', () => ({ default: () => <div data-testid="memory-settings">MemorySettings</div> }))
vi.mock('../settings/SubAgentsSettings', () => ({ default: () => <div data-testid="sub-agents-settings">SubAgentsSettings</div> }))
vi.mock('../settings/SkillsSettings', () => ({ default: () => <div data-testid="skills-settings">SkillsSettings</div> }))
vi.mock('../settings/SchedulerSettings', () => ({ default: () => <div data-testid="scheduler-settings">SchedulerSettings</div> }))
vi.mock('../settings/DaemonSettings', () => ({ default: () => <div data-testid="daemon-settings">DaemonSettings</div> }))
vi.mock('../settings/IdentitySettings', () => ({ default: () => <div data-testid="identity-settings">IdentitySettings</div> }))
vi.mock('../settings/SandboxSettings', () => ({ default: () => <div data-testid="sandbox-settings">SandboxSettings</div> }))

// Mock the settings API
vi.mock('../settings/settingsApi', () => ({
  fetchSettings: vi.fn().mockResolvedValue({
    general: { default_provider: 'openai', default_model: 'gpt-4', web_search_tool: '', web_extract_tool: '', timezone: '' },
    providers: [],
  }),
  fetchMCPConfig: vi.fn().mockResolvedValue({ mcpServers: {} }),
  fetchWebCapableTools: vi.fn().mockResolvedValue({ webSearch: [], webExtract: [] }),
  fetchFullConfig: vi.fn().mockResolvedValue({}),
  saveSettings: vi.fn().mockResolvedValue({}),
  fetchPlatformProviders: vi.fn().mockResolvedValue({ providers: {}, default_provider: '', default_model: '' }),
  fetchOrgProviders: vi.fn().mockResolvedValue({ providers: {}, default_provider: '', default_model: '' }),
  fetchTeamProviders: vi.fn().mockResolvedValue({ providers: {}, default_provider: '', default_model: '' }),
  savePlatformProviders: vi.fn().mockResolvedValue({}),
  saveOrgProviders: vi.fn().mockResolvedValue({}),
  inputClass: '',
  inputStyle: {},
  labelStyle: {},
  hintStyle: {},
  sectionBorderStyle: {},
  saveButtonStyle: {},
}))

// Mock global fetch for /api/standard-servers and other API calls
beforeEach(() => {
  vi.clearAllMocks()
  globalThis.fetch = vi.fn().mockResolvedValue({
    ok: true,
    json: () => Promise.resolve({ servers: [] }),
  }) as unknown as typeof fetch
})

describe('SettingsPage', () => {
  // Default props simulate a superadmin user who can see all sections
  const defaultProps = {
    activeSection: 'platform-general',
    onSectionChange: vi.fn(),
    theme: 'dark',
    platformRole: 'superadmin',
    userRole: 'owner',
  }

  it('renders loading state initially', async () => {
    render(<SettingsPage {...defaultProps} />)
    expect(screen.getByText('Loading settings...')).toBeInTheDocument()
    // Wait for async effects to settle (fetchSettings, fetchMCPConfig, etc.)
    await act(async () => {
      await new Promise(resolve => setTimeout(resolve, 0))
    })
  })

  it('renders the settings title after loading', async () => {
    render(<SettingsPage {...defaultProps} />)
    const title = await screen.findByText('Settings')
    expect(title).toBeInTheDocument()
  })

  it('renders menu items after loading', async () => {
    render(<SettingsPage {...defaultProps} />)
    // Wait for content to load
    await screen.findByText('Settings')
    // After loading, menu items should be present in the sidebar nav.
    // "General" appears in multiple sections (Org General + Platform General + section heading).
    const allGeneral = await screen.findAllByText('General')
    expect(allGeneral.length).toBeGreaterThanOrEqual(2)
    // "Providers" appears in Team, Org, and Platform sections
    const allProviders = screen.getAllByText('Providers')
    expect(allProviders.length).toBeGreaterThanOrEqual(1)
    // "MCP Servers" appears in Team, Org, and Platform sections
    const allMCP = screen.getAllByText('MCP Servers')
    expect(allMCP.length).toBeGreaterThanOrEqual(1)
  })

  it('calls onSectionChange when a menu item is clicked', async () => {
    const user = userEvent.setup()
    const onSectionChange = vi.fn()
    render(<SettingsPage {...defaultProps} onSectionChange={onSectionChange} />)
    // "Knowledge" is a Personal item visible to all users
    const knowledgeButton = await screen.findByText('Knowledge')
    await user.click(knowledgeButton)
    expect(onSectionChange).toHaveBeenCalledWith('knowledge')
  })

  it('shows version info after loading', async () => {
    render(<SettingsPage {...defaultProps} appVersion="1.2.3" />)
    const version = await screen.findByText(/1\.2\.3/)
    expect(version).toBeInTheDocument()
  })

  it('shows update available when provided', async () => {
    render(
      <SettingsPage
        {...defaultProps}
        updateAvailable={{ version: '2.0.0', url: 'https://example.com' } as any}
        onUpdateClick={vi.fn()}
      />
    )
    const update = await screen.findByText(/2\.0\.0/)
    expect(update).toBeInTheDocument()
  })

  it('renders GeneralSettings for platform-general section', async () => {
    render(<SettingsPage {...defaultProps} activeSection="platform-general" />)
    // platform-general maps through PLATFORM_SYSTEM_SECTIONS → 'general' → SettingsContent → GeneralSettings
    const el = await screen.findByTestId('general-settings')
    expect(el).toBeInTheDocument()
  })

  it('unlinks Slack with the CSRF header and refreshes the channel list', async () => {
    const user = userEvent.setup()
    const slackChannel = {
      id: 'slack-1',
      user_id: 'user-1',
      channel_type: 'slack',
      external_id: 'U123',
      display_name: 'Test Slack',
      enabled: true,
      verified: true,
      created_at: '2026-08-19T00:00:00Z',
    }
    let channelLoads = 0
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/user/channels' && (!init?.method || init.method === 'GET')) {
        channelLoads += 1
        return new Response(JSON.stringify({ channels: channelLoads === 1 ? [slackChannel] : [] }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }
      if (url === '/api/channels/info') {
        return new Response(JSON.stringify({
          telegram: { bot_username: '', configured: false, connected: false, enabled: false, error: '' },
          slack: { bot_user_id: 'B123', configured: true, connected: true, enabled: true, error: '' },
        }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      if (url === '/api/user/channels/slack-1' && init?.method === 'DELETE') {
        expect(new Headers(init.headers).get('X-Requested-With')).toBe('XMLHttpRequest')
        return new Response(null, { status: 204 })
      }
      return new Response(JSON.stringify({ servers: [] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    })
    globalThis.fetch = fetchMock as unknown as typeof fetch

    render(<SettingsPage {...defaultProps} activeSection="channels" isPlatformMode />)
    await screen.findByText('Test Slack')
    await user.click(screen.getByTitle('Unlink'))

    expect(await screen.findByText('Channel unlinked')).toBeInTheDocument()
    await waitFor(() => expect(channelLoads).toBe(2))
    expect(screen.queryByText('Test Slack')).not.toBeInTheDocument()
  })

  it('displays the server error when Slack unlink fails', async () => {
    const user = userEvent.setup()
    const slackChannel = {
      id: 'slack-1',
      user_id: 'user-1',
      channel_type: 'slack',
      external_id: 'U123',
      display_name: 'Test Slack',
      enabled: true,
      verified: true,
      created_at: '2026-08-19T00:00:00Z',
    }
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/user/channels/slack-1' && init?.method === 'DELETE') {
        return new Response(JSON.stringify({ error: 'Slack unlink was rejected' }), {
          status: 500,
          headers: { 'Content-Type': 'application/json' },
        })
      }
      if (url === '/api/user/channels') {
        return new Response(JSON.stringify({ channels: [slackChannel] }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }
      if (url === '/api/channels/info') {
        return new Response(JSON.stringify({
          telegram: { bot_username: '', configured: false, connected: false, enabled: false, error: '' },
          slack: { bot_user_id: 'B123', configured: true, connected: true, enabled: true, error: '' },
        }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      return new Response(JSON.stringify({ servers: [] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }) as unknown as typeof fetch

    render(<SettingsPage {...defaultProps} activeSection="channels" isPlatformMode />)
    await screen.findByText('Test Slack')
    await user.click(screen.getByTitle('Unlink'))

    expect(await screen.findByText('Slack unlink was rejected')).toBeInTheDocument()
    expect(screen.getByText('Test Slack')).toBeInTheDocument()
  })

  it('renders ProvidersSettings for platform-providers section', async () => {
    render(<SettingsPage {...defaultProps} activeSection="platform-providers" />)
    // platform-providers renders PlatformProvidersTab which wraps ProvidersSettings
    const el = await screen.findByTestId('providers-settings')
    expect(el).toBeInTheDocument()
  })
})
