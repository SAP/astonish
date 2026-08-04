import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import ConfirmDeleteModal from '../ConfirmDeleteModal'
import InstallMcpModal from '../InstallMcpModal'
import UpgradeDialog from '../UpgradeDialog'

describe('redesigned dialog components', () => {
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

  it('submits MCP installation configuration', async () => {
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

    await userEvent.type(screen.getByLabelText('GITHUB_TOKEN'), 'ghp_test')
    await userEvent.click(screen.getByRole('button', { name: 'Install' }))

    expect(onInstall).toHaveBeenCalledWith({ GITHUB_TOKEN: 'ghp_test' })
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
