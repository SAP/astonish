import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, vi, beforeEach } from 'vitest'

import UserDefaultModelSettings from '../UserDefaultModelSettings'
import * as userSettingsApi from '../../../api/userSettings'
import * as settingsApi from '../settingsApi'

vi.mock('../../ProviderModelSelector', () => ({
  default: ({ isOpen, onSelect, currentModel }: { isOpen: boolean, onSelect: (model: string) => void, currentModel: string }) => {
    if (!isOpen) return null
    return (
      <div data-testid="mock-model-selector">
        <p>Current: {currentModel}</p>
        <button onClick={() => onSelect('selected-model-from-modal')}>Select Model</button>
      </div>
    )
  }
}))

describe('UserDefaultModelSettings', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.spyOn(userSettingsApi, 'fetchUserDefaultModel').mockResolvedValue({
      defaultProvider: 'test-provider',
      defaultModel: 'test-model',
    })
    vi.spyOn(settingsApi, 'fetchSettings').mockResolvedValue({
      general: {
        default_provider: 'team-default-provider',
        default_model: 'team-default-model',
        web_search_tool: '',
        web_extract_tool: '',
        timezone: ''
      },
      providers: [
        { name: 'test-provider', type: 'basic-type', display_name: 'Test Provider', configured: true, fields: {} },
        { name: 'another-provider', type: 'anthropic', display_name: 'Another Provider', configured: true, fields: {} },
        { name: 'openrouter-provider', type: 'openrouter', display_name: 'Open Router', configured: true, fields: {} },
      ]
    })
    vi.spyOn(userSettingsApi, 'patchUserDefaultModel').mockResolvedValue(undefined)
    vi.spyOn(settingsApi, 'fetchProviderModels').mockResolvedValue({ models: ['model-1', 'model-2'] })
  })

  it('renders with initial data loaded from APIs', async () => {
    render(<UserDefaultModelSettings />)

    expect(await screen.findByRole('combobox', { name: /default provider/i })).toHaveTextContent('test-provider (basic-type)')
  })

  it('shows inheritance info when no user default is set', async () => {
    vi.spyOn(userSettingsApi, 'fetchUserDefaultModel').mockResolvedValue({
      defaultProvider: '',
      defaultModel: '',
    })

    render(<UserDefaultModelSettings />)

    expect(await screen.findByText(/Inheriting from Team:/)).toBeInTheDocument()
    expect(screen.getByText('team-default-provider')).toBeInTheDocument()
    expect(screen.getByText('team-default-model')).toBeInTheDocument()
  })

  it('calls patchUserDefaultModel with current form values on save', async () => {
    const user = userEvent.setup()
    render(<UserDefaultModelSettings />)

    await screen.findByRole('combobox', { name: /default provider/i })
    await user.click(screen.getByRole('button', { name: /save default/i }))

    await waitFor(() => {
      expect(userSettingsApi.patchUserDefaultModel).toHaveBeenCalledWith('test-provider', 'test-model')
    })
  })

  it('clears the form and calls patchUserDefaultModel on clear', async () => {
    const user = userEvent.setup()
    render(<UserDefaultModelSettings />)

    await screen.findByRole('combobox', { name: /default provider/i })
    await user.click(screen.getByRole('button', { name: /clear/i }))

    await waitFor(() => {
      expect(userSettingsApi.patchUserDefaultModel).toHaveBeenCalledWith('', '')
    })
  })

  it('opens the enhanced model selector for supported provider types', async () => {
    const user = userEvent.setup()
    vi.spyOn(userSettingsApi, 'fetchUserDefaultModel').mockResolvedValue({
      defaultProvider: 'openrouter-provider',
      defaultModel: '',
    })

    render(<UserDefaultModelSettings />)

    await user.click(await screen.findByRole('button', { name: /default model/i }))
    expect(await screen.findByTestId('mock-model-selector')).toBeInTheDocument()
  })

  it('loads models for basic select when opened', async () => {
    const user = userEvent.setup()
    vi.spyOn(userSettingsApi, 'fetchUserDefaultModel').mockResolvedValue({
      defaultProvider: 'test-provider',
      defaultModel: '',
    })

    render(<UserDefaultModelSettings />)

    const modelSelect = await screen.findByRole('combobox', { name: /default model/i })
    await user.click(modelSelect)

    await waitFor(() => {
      expect(settingsApi.fetchProviderModels).toHaveBeenCalledWith('test-provider')
    })
    expect(await screen.findByRole('option', { name: 'model-1' })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'model-2' })).toBeInTheDocument()
  })
})
