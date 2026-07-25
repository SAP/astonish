import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import ChatSettings from '../settings/ChatSettings'
import GeneralSettings from '../settings/GeneralSettings'
import { saveFullConfigSection } from '../settings/settingsApi'
import * as platformAdmin from '../../api/platformAdmin'

vi.mock('../settings/settingsApi', async () => {
  const actual = await vi.importActual<typeof import('../settings/settingsApi')>('../settings/settingsApi')
  return {
    ...actual,
    saveFullConfigSection: vi.fn().mockResolvedValue({}),
  }
})

vi.mock('../../api/platformAdmin', () => ({
  getPlatformAuthSettings: vi.fn().mockResolvedValue({
    allow_registration: true,
    require_email_verification: false,
    dev_environment: false,
  }),
  savePlatformAuthSettings: vi.fn().mockResolvedValue({
    allow_registration: true,
    require_email_verification: false,
    dev_environment: true,
  }),
}))

describe('GeneralSettings', () => {
  const baseForm = {
    default_provider: '',
    default_model: '',
    web_search_tool: '',
    web_extract_tool: '',
    timezone: '',
  }

  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders web tool controls, updates form values, and links to MCP setup', async () => {
    const user = userEvent.setup()
    const setGeneralForm = vi.fn()
    const onSectionChange = vi.fn()

    render(
      <GeneralSettings
        settings={null}
        generalForm={baseForm}
        setGeneralForm={setGeneralForm}
        webCapableTools={{
          webSearch: [{ source: 'tavily', name: 'search' }],
          webExtract: [{ source: 'firecrawl', name: 'extract' }],
        }}
        standardServers={[{
          id: 'tavily',
          displayName: 'Tavily',
          isDefault: true,
          installed: false,
          envVars: [],
          capabilities: { webSearch: true, webExtract: false },
        }]}
        saving={false}
        onSave={vi.fn()}
        onSectionChange={onSectionChange}
      />
    )

    await user.click(screen.getByRole('combobox', { name: /web search tool/i }))
    await user.click(await screen.findByRole('option', { name: 'tavily (search)' }))
    expect(setGeneralForm).toHaveBeenCalledWith({ ...baseForm, web_search_tool: 'tavily:search' })

    await user.click(screen.getByRole('button', { name: /mcp servers/i }))
    expect(onSectionChange).toHaveBeenCalledWith('mcp')
  })

  it('saves the general form and updates the platform environment switch', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn()

    render(
      <GeneralSettings
        settings={null}
        generalForm={{ ...baseForm, timezone: 'UTC' }}
        setGeneralForm={vi.fn()}
        webCapableTools={{ webSearch: [], webExtract: [] }}
        standardServers={[]}
        saving={false}
        onSave={onSave}
        isPlatform
      />
    )

    await user.click(screen.getByRole('button', { name: /save changes/i }))
    expect(onSave).toHaveBeenCalled()

    const environmentSwitch = await screen.findByRole('switch', { name: /toggle development environment banner/i })
    await user.click(environmentSwitch)

    expect(platformAdmin.savePlatformAuthSettings).toHaveBeenCalledWith({ dev_environment: true })
    expect(await screen.findByText('Development environment enabled')).toBeInTheDocument()
  })
})

describe('ChatSettings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders chat fields and saves edited values through the full-config API', async () => {
    const user = userEvent.setup()
    const onSaved = vi.fn()

    render(
      <ChatSettings
        config={{
          system_prompt: 'Be concise.',
          max_tool_calls: 12,
          max_tools: 24,
          auto_approve: false,
          workspace_dir: '/workspace',
          flow_save_dir: '/flows',
        }}
        onSaved={onSaved}
      />
    )

    const prompt = await screen.findByLabelText(/system prompt/i)
    await user.clear(prompt)
    await user.type(prompt, 'Be helpful and precise.')

    await user.clear(screen.getByLabelText(/max tool calls/i))
    await user.type(screen.getByLabelText(/max tool calls/i), '20')

    await user.click(screen.getByRole('switch', { name: /toggle auto-approve tool calls/i }))
    await user.click(screen.getByRole('button', { name: /save changes/i }))

    await waitFor(() => {
      expect(saveFullConfigSection).toHaveBeenCalledWith('chat', expect.objectContaining({
        system_prompt: 'Be helpful and precise.',
        max_tool_calls: 20,
        max_tools: 24,
        auto_approve: true,
        workspace_dir: '/workspace',
        flow_save_dir: '/flows',
      }))
    })
    expect(onSaved).toHaveBeenCalled()
    expect(await screen.findByText('Saved')).toBeInTheDocument()
  })
})
