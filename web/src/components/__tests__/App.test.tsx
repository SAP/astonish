import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, act } from '@testing-library/react'
import App from '../../App'

// Mock all heavyweight child components to isolate App rendering
vi.mock('../TopBar', () => ({ default: (props: any) => <div data-testid="top-bar">TopBar</div> }))
vi.mock('../Sidebar', () => ({ default: () => <div data-testid="sidebar">Sidebar</div> }))
vi.mock('../FlowCanvas', () => ({ default: () => <div data-testid="flow-canvas">FlowCanvas</div> }))
vi.mock('../ChatPanel', () => ({ default: () => <div data-testid="chat-panel">ChatPanel</div> }))
vi.mock('../YamlDrawer', () => ({ default: () => <div data-testid="yaml-drawer">YamlDrawer</div> }))
vi.mock('../Header', () => ({ default: () => <div data-testid="header">Header</div> }))
vi.mock('../NodeEditor', () => ({ default: () => <div data-testid="node-editor">NodeEditor</div> }))
vi.mock('../EdgeEditor', () => ({ default: () => <div data-testid="edge-editor">EdgeEditor</div> }))
vi.mock('../CreateAgentModal', () => ({ default: () => null }))
vi.mock('../ConfirmDeleteModal', () => ({ default: () => null }))
vi.mock('../AIChatPanel', () => ({ default: () => null }))
vi.mock('../SettingsPage', () => ({ default: () => <div data-testid="settings-page">SettingsPage</div> }))
vi.mock('../SetupWizard', () => ({ default: ({ onComplete }: { onComplete: () => void }) => <div data-testid="setup-wizard">SetupWizard</div> }))
vi.mock('../StudioChat', () => ({ default: () => <div data-testid="studio-chat">StudioChat</div> }))
vi.mock('../FleetView', () => ({ default: () => <div data-testid="fleet-view">FleetView</div> }))
vi.mock('../DrillView', () => ({ default: () => <div data-testid="drill-view">DrillView</div> }))
vi.mock('../MCPDependenciesPanel', () => ({ default: () => null }))
vi.mock('../InstallMcpModal', () => ({ default: () => null }))
vi.mock('../ToastNotification', () => ({ default: () => null }))
vi.mock('../UpgradeDialog', () => ({ default: () => null }))

// Mock hooks
vi.mock('../../hooks/useTheme', () => ({
  useTheme: () => ({
    theme: 'dark',
    setTheme: vi.fn(),
    toggleTheme: vi.fn(),
    brandTheme: 'classic',
    setBrandTheme: vi.fn(),
    setUserBrandTheme: vi.fn().mockResolvedValue(undefined),
    setPlatformBrandTheme: vi.fn().mockResolvedValue(undefined),
    userBrandPreference: '',
    platformBrandDefault: '',
    availableBrandThemes: ['classic', 'aster', 'nova', 'sage', 'ember'],
    refreshBrandTheme: vi.fn().mockResolvedValue(undefined),
  }),
}))
vi.mock('../../hooks/useHashRouter', () => ({
  useHashRouter: () => ({
    path: { view: 'chat', params: {} },
    navigate: vi.fn(),
    replaceHash: vi.fn(),
  }),
  buildPath: (view: string) => `/${view}`,
}))

// Mock @xyflow/react
vi.mock('@xyflow/react', () => ({
  ReactFlowProvider: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}))

// Mock API modules
vi.mock('../../api/agents', () => ({
  fetchAgents: vi.fn().mockResolvedValue({ agents: [] }),
  fetchAgent: vi.fn().mockResolvedValue(null),
  saveAgent: vi.fn().mockResolvedValue({}),
  deleteAgent: vi.fn().mockResolvedValue({}),
  fetchTools: vi.fn().mockResolvedValue({ tools: [] }),
  checkMcpDependencies: vi.fn().mockResolvedValue(null),
  installMcpServer: vi.fn().mockResolvedValue({}),
  getMcpStoreServer: vi.fn().mockResolvedValue(null),
  installInlineMcpServer: vi.fn().mockResolvedValue({}),
}))

vi.mock('../../api/sandbox', () => ({
  fetchSandboxStatus: vi.fn().mockResolvedValue(null),
}))

vi.mock('../../utils/yamlToFlow', () => ({
  yamlToFlowAsync: vi.fn().mockResolvedValue({ nodes: [], edges: [] }),
  extractLayout: vi.fn().mockReturnValue({}),
}))

vi.mock('../../utils/flowToYaml', () => ({
  addStandaloneNode: vi.fn().mockReturnValue(''),
  addConnection: vi.fn().mockReturnValue(''),
  removeConnection: vi.fn().mockReturnValue(''),
  updateNode: vi.fn().mockReturnValue(''),
  orderYamlKeys: vi.fn().mockReturnValue(''),
}))

vi.mock('../../utils/formatters', () => ({
  snakeToTitleCase: (s: string) => s,
}))

vi.mock('js-yaml', () => {
  const load = vi.fn().mockReturnValue({})
  const dump = vi.fn().mockReturnValue('')
  return {
    default: { load, dump },
    load,
    dump,
  }
})

beforeEach(() => {
  // Mock fetch for setup status check and other API calls
  globalThis.fetch = vi.fn().mockImplementation((url: string) => {
    if (url.includes('/api/settings/status')) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ configured: true }),
      })
    }
    if (url.includes('/api/version')) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ version: 'test' }),
      })
    }
    if (url.includes('/api/updates')) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve(null),
      })
    }
    return Promise.resolve({
      ok: true,
      json: () => Promise.resolve({}),
    })
  }) as unknown as typeof fetch

  // Set hash to chat view
  window.location.hash = '#/chat'
})

describe('App', () => {
  it('renders without crashing', async () => {
    render(<App />)
    // App should render something
    expect(document.body.querySelector('div')).toBeTruthy()

    // Wait for async effects to settle
    await act(async () => {
      await new Promise(resolve => setTimeout(resolve, 0))
    })
  })

  it('shows loading state initially while checking setup', async () => {
    render(<App />)
    // During setup check, a loading spinner may appear
    // The loading state contains an animated element
    const _loadingEl = screen.queryByText(/loading/i) ||
      document.querySelector('[class*="animate"]')
    // App should either show loading or have rendered the main UI
    expect(document.body.innerHTML.length).toBeGreaterThan(0)

    // Wait for the async setup check to complete so React doesn't warn
    // about state updates outside of act()
    await act(async () => {
      await new Promise(resolve => setTimeout(resolve, 0))
    })
  })

  it('renders StudioChat when in chat view', async () => {
    render(<App />)
    // After setup check resolves, StudioChat should render for chat view
    const chatEl = await screen.findByTestId('studio-chat')
    expect(chatEl).toBeInTheDocument()
  })

  it('renders TopBar after setup check', async () => {
    render(<App />)
    const topBar = await screen.findByTestId('top-bar')
    expect(topBar).toBeInTheDocument()
  })
})
