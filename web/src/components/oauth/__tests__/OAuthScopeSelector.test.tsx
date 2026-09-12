import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import OAuthScopeSelector from '../OAuthScopeSelector'

const scopes = [
  { value: 'tool:execute', label: 'MCP access', description: 'Allows MCP.' },
  { value: 'chat', label: 'Chat access', description: 'Allows chat.' },
]

describe('OAuthScopeSelector', () => {
  it('renders accessible permissions and emits catalog-ordered selections', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<OAuthScopeSelector scopes={scopes} value={['chat']} onChange={onChange} />)

    expect(screen.getByRole('checkbox', { name: 'MCP access' })).not.toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Chat access' })).toBeChecked()
    await user.click(screen.getByRole('checkbox', { name: 'MCP access' }))
    expect(onChange).toHaveBeenCalledWith(['tool:execute', 'chat'])
  })

  it('disables every permission row when saving', () => {
    render(<OAuthScopeSelector scopes={scopes} value={[]} onChange={vi.fn()} disabled />)
    expect(screen.getByRole('checkbox', { name: 'MCP access' })).toBeDisabled()
    expect(screen.getByRole('checkbox', { name: 'Chat access' })).toBeDisabled()
  })
})
