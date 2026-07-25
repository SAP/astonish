import { describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import Sidebar from '../Sidebar'
import TopBar from '../TopBar'

const teams = [
  { slug: 'alpha', name: 'Alpha Team' },
  { slug: 'beta', name: 'Beta Team' },
]

const orgs = [
  { id: 'org-1', slug: 'acme', name: 'Acme', role: 'admin' },
  { id: 'org-2', slug: 'globex', name: 'Globex', role: 'member' },
]

describe('redesigned app shell', () => {
  it('navigates from top-level shell controls and switches team/user menus', async () => {
    const onNavigate = vi.fn()
    const onTeamChange = vi.fn()
    const onOrgSwitch = vi.fn()
    const onLogout = vi.fn()
    const onToggleTheme = vi.fn()

    render(
      <TopBar
        theme="dark"
        onToggleTheme={onToggleTheme}
        onOpenSandbox={vi.fn()}
        defaultProvider="SpaceXAI"
        defaultModel="grok-4.5"
        currentView="chat"
        onNavigate={onNavigate}
        isPlatformMode
        user={{ id: 'u1', email: 'user@example.test', display_name: 'Ada Lovelace', role: 'admin' }}
        org={orgs[0]}
        orgs={orgs}
        onOrgSwitch={onOrgSwitch}
        activeTeam="alpha"
        teams={teams}
        onTeamChange={onTeamChange}
        onLogout={onLogout}
      />
    )

    await userEvent.click(screen.getByRole('button', { name: 'Flows' }))
    expect(onNavigate).toHaveBeenCalledWith('canvas')

    await userEvent.click(screen.getByRole('button', { name: /Alpha Team/i }))
    await userEvent.click(await screen.findByRole('menuitem', { name: 'Beta Team' }))
    expect(onTeamChange).toHaveBeenCalledWith('beta')

    await userEvent.click(screen.getByRole('button', { name: 'Open user menu' }))
    expect(await screen.findByText('Ada Lovelace')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('menuitem', { name: /Globex/ }))
    expect(onOrgSwitch).toHaveBeenCalledWith('globex')

    await userEvent.click(screen.getByRole('button', { name: 'Open user menu' }))
    await userEvent.click(screen.getByRole('menuitem', { name: /Logout/ }))
    expect(onLogout).toHaveBeenCalledOnce()
  })

  it('filters, selects, and invokes Sidebar flow actions', async () => {
    const onAgentSelect = vi.fn()
    const onCreateNew = vi.fn()
    const onDeleteAgent = vi.fn()
    const onPublishFlow = vi.fn()
    const onForkFlow = vi.fn()

    const agents = [
      { id: 'a1', name: 'research_flow', description: 'Research helper', source: 'local', scope: 'personal' },
      { id: 'a2', name: 'deploy_flow', description: 'Deploy helper', source: 'local', scope: 'team' },
      { id: 'a3', name: 'official/report_writer', description: 'Write reports', source: 'store', tapName: 'official' },
    ]

    render(
      <Sidebar
        agents={agents}
        selectedAgent={agents[0]}
        onAgentSelect={onAgentSelect}
        onCreateNew={onCreateNew}
        onDeleteAgent={onDeleteAgent}
        onPublishFlow={onPublishFlow}
        onForkFlow={onForkFlow}
        isLoading={false}
      />
    )

    await userEvent.click(screen.getByRole('button', { name: 'New Flow' }))
    expect(onCreateNew).toHaveBeenCalledOnce()

    await userEvent.type(screen.getByPlaceholderText('Search flows'), 'deploy')
    expect(screen.queryByText('Research Flow')).not.toBeInTheDocument()

    await userEvent.click(screen.getByText('Deploy Flow'))
    expect(onAgentSelect).toHaveBeenCalledWith(agents[1])

    await userEvent.click(screen.getByTitle('Fork to Personal'))
    expect(onForkFlow).toHaveBeenCalledWith(agents[1])

    const teamSection = screen.getByRole('button', { name: /Team/ })
    await userEvent.click(teamSection)
    expect(screen.queryByText('Deploy Flow')).not.toBeInTheDocument()
  })

  it('shows Sidebar empty state after filtering', async () => {
    render(
      <Sidebar
        agents={[{ id: 'a1', name: 'research_flow', source: 'local' }]}
        selectedAgent={null}
        onAgentSelect={vi.fn()}
        onCreateNew={vi.fn()}
        onDeleteAgent={vi.fn()}
        isLoading={false}
      />
    )

    await userEvent.type(screen.getByPlaceholderText('Search flows'), 'missing')
    expect(screen.getByText('No flows match your search')).toBeInTheDocument()
  })

  it('shows mobile shell navigation through a sheet', async () => {
    const onNavigate = vi.fn()
    const originalWidth = window.innerWidth
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: 500 })

    render(
      <TopBar
        theme="light"
        onToggleTheme={vi.fn()}
        onOpenSandbox={vi.fn()}
        defaultProvider={null}
        defaultModel={null}
        currentView="chat"
        onNavigate={onNavigate}
      />
    )

    await userEvent.click(screen.getByRole('button', { name: 'Open navigation menu' }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Settings' }))
    expect(onNavigate).toHaveBeenCalledWith('settings')

    Object.defineProperty(window, 'innerWidth', { configurable: true, value: originalWidth })
  })
})
