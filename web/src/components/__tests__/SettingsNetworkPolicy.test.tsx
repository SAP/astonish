import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import NetworkPolicySettings from '../settings/NetworkPolicySettings'
import { teamFetch } from '../../api/teamContext'

vi.mock('../../api/teamContext', () => ({
  teamFetch: vi.fn(),
}))

describe('NetworkPolicySettings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(teamFetch).mockResolvedValue({ ok: true, json: async () => ({ rules: [] }) } as Response)
  })

  it('renders read-only external rules with scope badges', () => {
    render(
      <NetworkPolicySettings
        scope="team"
        readOnly
        rules={[{ id: 'rule-1', host: '*.example.com', port: 443, action: 'allow', scope: 'org' }]}
      />
    )

    expect(screen.getByText('*.example.com:443')).toBeInTheDocument()
    expect(screen.getByText('allow')).toBeInTheDocument()
    expect(screen.getByText('org')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /delete/i })).not.toBeInTheDocument()
  })

  it('adds team rules through the scoped API', async () => {
    const user = userEvent.setup()
    const onRulesChange = vi.fn()
    vi.mocked(teamFetch).mockResolvedValue({ ok: true, json: async () => ({ rules: [] }) } as Response)

    render(<NetworkPolicySettings scope="team" teamSlug="core" onRulesChange={onRulesChange} />)

    await screen.findByText(/no network policy rules configured/i)
    await user.click(screen.getByRole('button', { name: /add rule/i }))
    await user.type(screen.getByLabelText(/host pattern/i), '*.internal')
    await user.type(screen.getByLabelText(/port/i), '8443')
    await user.click(screen.getByRole('button', { name: /^add$/i }))

    await waitFor(() => {
      expect(teamFetch).toHaveBeenCalledWith('/api/network-policies?scope=team', expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ host: '*.internal', port: 8443, action: 'allow' }),
      }), 'core')
    })
    expect(onRulesChange).toHaveBeenCalled()
  })

  it('deletes team rules through the scoped API', async () => {
    const user = userEvent.setup()
    const onRulesChange = vi.fn()
    vi.mocked(teamFetch)
      .mockResolvedValueOnce({ ok: true, json: async () => ({ rules: [{ id: 'rule-1', host: 'api.example.com', port: 0, action: 'deny' }] }) } as Response)
      .mockResolvedValue({ ok: true, json: async () => ({ rules: [] }) } as Response)

    render(<NetworkPolicySettings scope="team" teamSlug="core" onRulesChange={onRulesChange} />)

    expect(await screen.findByText('api.example.com')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /delete api\.example\.com/i }))

    await waitFor(() => {
      expect(teamFetch).toHaveBeenCalledWith('/api/network-policies/rule-1?scope=team', { method: 'DELETE' }, 'core')
    })
    expect(onRulesChange).toHaveBeenCalled()
  })
})
