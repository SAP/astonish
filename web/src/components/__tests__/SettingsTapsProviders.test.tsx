import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import ProvidersSettings from '../settings/ProvidersSettings'
import TapsSettings from '../settings/TapsSettings'
import { addTap, fetchTaps, removeTap, saveSettings } from '../settings/settingsApi'
import { teamFetch } from '../../api/teamContext'

vi.mock('../ProviderModelSelector', () => ({
  default: () => null,
}))

vi.mock('../settings/settingsApi', () => ({
  fetchTaps: vi.fn(),
  addTap: vi.fn(),
  removeTap: vi.fn(),
  saveSettings: vi.fn(),
  replaceAllProviders: vi.fn(),
  savePlatformProviders: vi.fn(),
  saveOrgProviders: vi.fn(),
  deleteProviderAtLevel: vi.fn(),
  fetchProviderModels: vi.fn(),
  testProviderConnection: vi.fn(),
}))

vi.mock('../../api/teamContext', () => ({
  teamFetch: vi.fn(),
}))

describe('TapsSettings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(fetchTaps).mockResolvedValue({ taps: [] })
    vi.mocked(addTap).mockResolvedValue({})
    vi.mocked(removeTap).mockResolvedValue({})
    vi.mocked(teamFetch).mockResolvedValue({ ok: true, json: async () => ({}) } as Response)
  })

  it('adds a repository tap and reloads the list', async () => {
    const user = userEvent.setup()
    render(<TapsSettings />)

    await screen.findByText(/no repositories configured/i)
    await user.type(screen.getByLabelText(/repository url or owner\/repo/i), 'SAP/astonish-flows')
    await user.type(screen.getByLabelText(/alias/i), 'flows')
    await user.click(screen.getByRole('button', { name: /add repository/i }))

    await waitFor(() => {
      expect(addTap).toHaveBeenCalledWith('SAP/astonish-flows', 'flows', undefined)
    })
    expect(fetchTaps).toHaveBeenCalledTimes(2)
  })

  it('refreshes taps and removes custom repositories', async () => {
    const user = userEvent.setup()
    vi.mocked(fetchTaps).mockResolvedValue({
      taps: [
        { name: 'official', url: 'https://example.com/official.git' },
        { name: 'custom', url: 'https://example.com/custom.git' },
      ],
    })

    render(<TapsSettings teamSlug="core" />)

    expect((await screen.findAllByText('official')).length).toBeGreaterThanOrEqual(1)
    expect(screen.getByText('custom')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /refresh/i }))
    await waitFor(() => {
      expect(teamFetch).toHaveBeenCalledWith('/api/taps/update', { method: 'POST' }, 'core')
    })

    await user.click(screen.getByRole('button', { name: /remove custom/i }))
    await waitFor(() => {
      expect(removeTap).toHaveBeenCalledWith('custom', 'core')
    })
  })
})

describe('ProvidersSettings', () => {
  const baseProps = {
    settings: {
      general: {
        default_provider: '',
        default_model: '',
        web_search_tool: '',
        web_extract_tool: '',
        timezone: '',
      },
      providers: [],
    },
    providerForms: {},
    setProviderForms: vi.fn(),
    generalForm: {
      default_provider: '',
      default_model: '',
    },
    setGeneralForm: vi.fn(),
    saving: false,
    setSaving: vi.fn(),
    setSaveSuccess: vi.fn(),
    error: null,
    setError: vi.fn(),
    loadData: vi.fn(),
  }

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(saveSettings).mockResolvedValue({})
  })

  it('uses the shared dialog flow to add a provider instance', async () => {
    const user = userEvent.setup()
    const loadData = vi.fn()
    const onSettingsSaved = vi.fn()

    render(
      <ProvidersSettings
        {...baseProps}
        loadData={loadData}
        onSettingsSaved={onSettingsSaved}
      />
    )

    await user.click(screen.getByRole('button', { name: /add provider instance/i }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/this provider type requires/i)).toBeInTheDocument()
    expect(within(dialog).getByText('API Key')).toBeInTheDocument()

    await user.type(within(dialog).getByLabelText(/instance name/i), 'openai-prod')
    await user.click(within(dialog).getByRole('button', { name: /^add provider$/i }))

    await waitFor(() => {
      expect(saveSettings).toHaveBeenCalledWith({
        providers: {
          'openai-prod': { type: 'openai' },
        },
      })
    })
    expect(loadData).toHaveBeenCalled()
    expect(onSettingsSaved).toHaveBeenCalled()
  })
})
