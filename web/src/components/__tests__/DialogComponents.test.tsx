import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import ConfirmDeleteModal from '../ConfirmDeleteModal'
import InstallMcpModal from '../InstallMcpModal'
import UpgradeDialog from '../UpgradeDialog'
import { teamFetch } from '@/api/teamContext'

vi.mock('@/api/teamContext', () => ({
  teamFetch: vi.fn(),
}))

describe('redesigned dialog components', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('confirms destructive flow deletion', async () => {
    const onClose = vi.fn()
    const onConfirm = vi.fn()

    render(
      <ConfirmDeleteModal
        isOpen
        onClose={onClose}
        onConfirm={onConfirm}
        agentName="Research Agent"
        isStoreFlow={false}
      />
    )

    expect(screen.getByRole('heading', { name: 'Delete Agent' })).toBeInTheDocument()
    expect(screen.getByText('Research Agent')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Delete Agent' }))
    expect(onConfirm).toHaveBeenCalledOnce()
  })

  it('submits non-sensitive MCP installation configuration', async () => {
    const onClose = vi.fn()
    const onInstall = vi.fn().mockResolvedValue(undefined)

    render(
      <InstallMcpModal
        isOpen
        onClose={onClose}
        onInstall={onInstall}
        server={{
          name: 'Logger',
          description: 'Logging tools',
          config: { env: { LOG_LEVEL: 'info' } },
        }}
      />
    )

    await userEvent.clear(screen.getByLabelText('LOG_LEVEL'))
    await userEvent.type(screen.getByLabelText('LOG_LEVEL'), 'debug')
    await userEvent.click(screen.getByRole('button', { name: 'Install' }))

    expect(onInstall).toHaveBeenCalledWith({ LOG_LEVEL: 'debug' })
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('omits blank sensitive MCP installation values', async () => {
    const onClose = vi.fn()
    const onInstall = vi.fn().mockResolvedValue(undefined)

    render(
      <InstallMcpModal
        isOpen
        onClose={onClose}
        onInstall={onInstall}
        server={{
          name: 'GitHub',
          description: 'GitHub tools',
          config: { env: { GITHUB_TOKEN: 'token description' } },
        }}
      />
    )

    await userEvent.click(screen.getByRole('button', { name: 'Install' }))

    expect(onInstall).toHaveBeenCalledWith({})
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('submits credential-bound MCP installation configuration', async () => {
    vi.mocked(teamFetch).mockResolvedValue({
      ok: true,
      json: async () => ({ credentials: [{ name: 'github', type: 'bearer', scope: 'personal' }] }),
    } as Response)
    const onClose = vi.fn()
    const onInstall = vi.fn().mockResolvedValue(undefined)

    render(
      <InstallMcpModal
        isOpen
        onClose={onClose}
        onInstall={onInstall}
        server={{
          name: 'GitHub',
          description: 'GitHub tools',
          config: { env: { GITHUB_TOKEN: 'token description' } },
        }}
      />
    )

    await userEvent.click(screen.getByRole('button', { name: 'Credential' }))
    await userEvent.click(await screen.findByRole('button', { name: /github/i }))
    await waitFor(() => expect(screen.getByText('{{CREDENTIAL:github:token}}')).toBeInTheDocument())
    await userEvent.click(screen.getByRole('button', { name: 'Bind' }))
    await userEvent.click(screen.getByRole('button', { name: 'Install' }))

    expect(onInstall).toHaveBeenCalledWith({ GITHUB_TOKEN: '{{CREDENTIAL:github:token}}' })
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('renders upgrade instructions and closes', async () => {
    const onClose = vi.fn()
    const open = vi.spyOn(window, 'open').mockImplementation(() => null)

    render(<UpgradeDialog info={{ version: '2.5.0', url: 'https://example.test/releases' }} onClose={onClose} />)

    expect(screen.getByRole('heading', { name: 'Update Available: 2.5.0' })).toBeInTheDocument()
    expect(screen.getByText('brew upgrade SAP/astonish/astonish')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Download from GitHub Releases' }))
    expect(open).toHaveBeenCalledWith('https://example.test/releases', '_blank')

    await userEvent.click(screen.getAllByRole('button', { name: 'Close' })[0])
    expect(onClose).toHaveBeenCalledOnce()

    open.mockRestore()
  })
})
