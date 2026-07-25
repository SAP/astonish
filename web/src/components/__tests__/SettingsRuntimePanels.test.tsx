import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import BrowserSettings from '../settings/BrowserSettings'
import ChannelsSettings from '../settings/ChannelsSettings'
import DaemonSettings from '../settings/DaemonSettings'
import IdentitySettings from '../settings/IdentitySettings'
import MemorySettings from '../settings/MemorySettings'
import SessionsSettings from '../settings/SessionsSettings'
import SubAgentsSettings from '../settings/SubAgentsSettings'
import { saveFullConfigSection } from '../settings/settingsApi'

vi.mock('../settings/settingsApi', async () => {
  const actual = await vi.importActual<typeof import('../settings/settingsApi')>('../settings/settingsApi')
  return {
    ...actual,
    saveFullConfigSection: vi.fn().mockResolvedValue({}),
  }
})

describe('SessionsSettings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(saveFullConfigSection).mockResolvedValue({})
  })

  it('updates session storage and compaction settings', async () => {
    const user = userEvent.setup()
    const onSaved = vi.fn()

    render(
      <SessionsSettings
        config={{
          storage: 'file',
          base_dir: '/sessions',
          compaction: { enabled: true, threshold: 0.8, preserve_recent: 4 },
          cleanup: { max_age_days: 5 },
        }}
        onSaved={onSaved}
      />
    )

    await user.clear(screen.getByLabelText(/sessions directory/i))
    await user.type(screen.getByLabelText(/sessions directory/i), '/tmp/sessions')
    await user.click(screen.getByRole('switch', { name: /toggle context compaction/i }))
    await user.clear(screen.getByLabelText(/auto-delete after/i))
    await user.type(screen.getByLabelText(/auto-delete after/i), '30')
    await user.click(screen.getByRole('button', { name: /save changes/i }))

    await waitFor(() => {
      expect(saveFullConfigSection).toHaveBeenCalledWith('sessions', expect.objectContaining({
        storage: 'file',
        base_dir: '/tmp/sessions',
        compaction: expect.objectContaining({ enabled: false }),
        cleanup: { max_age_days: 30 },
      }))
    })
    expect(onSaved).toHaveBeenCalled()
  })
})

describe('MemorySettings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(saveFullConfigSection).mockResolvedValue({})
  })

  it('saves embedding and watcher settings', async () => {
    const user = userEvent.setup()
    const onSaved = vi.fn()

    render(
      <MemorySettings
        config={{
          enabled: true,
          memory_dir: '/memory',
          vector_dir: '/vectors',
          embedding: { provider: '', model: '', base_url: '', api_key: '' },
          chunking: { max_chars: 1600, overlap: 320 },
          search: { max_results: 6, min_score: 0.35 },
          sync: { watch: true, debounce_ms: 1500 },
        }}
        onSaved={onSaved}
      />
    )

    await user.click(screen.getByRole('combobox', { name: /provider/i }))
    await user.click(await screen.findByRole('option', { name: 'OpenAI' }))
    await user.type(screen.getByLabelText(/model/i), 'text-embedding-3-large')
    await user.click(screen.getByRole('switch', { name: /watch for changes/i }))
    await user.click(screen.getByRole('button', { name: /save changes/i }))

    await waitFor(() => {
      expect(saveFullConfigSection).toHaveBeenCalledWith('memory', expect.objectContaining({
        embedding: expect.objectContaining({ provider: 'openai', model: 'text-embedding-3-large' }),
        sync: expect.objectContaining({ watch: false }),
      }))
    })
    expect(onSaved).toHaveBeenCalled()
  })
})

describe('BrowserSettings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(saveFullConfigSection).mockResolvedValue({})
  })

  it('saves browser engine and display settings', async () => {
    const user = userEvent.setup()
    const onSaved = vi.fn()

    render(
      <BrowserSettings
        config={{
          headless: null,
          viewport_width: 1920,
          viewport_height: 1080,
          no_sandbox: null,
          chrome_path: '',
          user_data_dir: '',
          navigation_timeout: 30,
          proxy: '',
          remote_cdp_url: '',
          handoff_port: 9222,
        }}
        onSaved={onSaved}
      />
    )

    await user.click(screen.getByRole('combobox', { name: /browser engine/i }))
    await user.click(await screen.findByRole('option', { name: /custom chrome/i }))
    await user.type(screen.getByLabelText(/chrome binary path/i), '/usr/bin/chromium')
    await user.click(screen.getByRole('switch', { name: /headless mode/i }))
    fireEvent.change(screen.getByLabelText(/navigation timeout/i), { target: { value: '60' } })
    await user.click(screen.getByRole('button', { name: /save changes/i }))

    await waitFor(() => {
      expect(saveFullConfigSection).toHaveBeenCalledWith('browser', expect.objectContaining({
        chrome_path: '/usr/bin/chromium',
        headless: true,
        navigation_timeout: 60,
      }))
    })
    expect(onSaved).toHaveBeenCalled()
  })
})

describe('IdentitySettings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(saveFullConfigSection).mockResolvedValue({})
  })

  it('saves agent identity details', async () => {
    const user = userEvent.setup()
    const onSaved = vi.fn()

    render(
      <IdentitySettings
        config={{ name: 'Old Agent', username: 'oldagent', email: '', bio: '', website: '', locale: '', timezone: '' }}
        onSaved={onSaved}
      />
    )

    await user.clear(screen.getByLabelText(/display name/i))
    await user.type(screen.getByLabelText(/display name/i), 'Registration Agent')
    await user.type(screen.getByLabelText(/email/i), 'agent@example.com')
    await user.click(screen.getByRole('combobox', { name: /locale/i }))
    await user.click(await screen.findByRole('option', { name: /english \(us\)/i }))
    await user.click(screen.getByRole('button', { name: /save changes/i }))

    await waitFor(() => {
      expect(saveFullConfigSection).toHaveBeenCalledWith('agent_identity', expect.objectContaining({
        name: 'Registration Agent',
        email: 'agent@example.com',
        locale: 'en-US',
      }))
    })
    expect(onSaved).toHaveBeenCalled()
  })
})

describe('SubAgentsSettings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(saveFullConfigSection).mockResolvedValue({})
  })

  it('saves delegation limits and enabled state', async () => {
    const user = userEvent.setup()
    const onSaved = vi.fn()

    render(
      <SubAgentsSettings
        config={{ enabled: true, max_depth: 2, max_concurrent: 5, task_timeout_sec: 300 }}
        onSaved={onSaved}
      />
    )

    fireEvent.change(screen.getByLabelText(/max concurrent/i), { target: { value: '8' } })
    await user.click(screen.getByRole('switch', { name: /enable sub-agents/i }))
    await user.click(screen.getByRole('button', { name: /save changes/i }))

    await waitFor(() => {
      expect(saveFullConfigSection).toHaveBeenCalledWith('sub_agents', expect.objectContaining({
        enabled: false,
        max_concurrent: 8,
      }))
    })
    expect(onSaved).toHaveBeenCalled()
  })
})

describe('ChannelsSettings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(saveFullConfigSection).mockResolvedValue({})
  })

  it('saves channel switches and allow lists', async () => {
    const user = userEvent.setup()
    const onSaved = vi.fn()

    render(
      <ChannelsSettings
        config={{
          enabled: false,
          telegram: { enabled: false, bot_token: '', allow_from: [] },
          email: { enabled: true, address: 'agent@example.com', allow_from: ['old@example.com'] },
        }}
        onSaved={onSaved}
      />
    )

    await user.click(screen.getByRole('switch', { name: /enable channels/i }))
    await user.click(screen.getByRole('switch', { name: /enable telegram/i }))
    await user.click(screen.getByText('Telegram'))
    await user.type(screen.getByLabelText(/bot token/i), 'telegram-token')
    await user.type(screen.getByLabelText(/allowed user ids/i), '123, 456')
    await user.clear(screen.getByLabelText(/allowed senders/i))
    await user.type(screen.getByLabelText(/allowed senders/i), 'admin@example.com, ops@example.com')
    await user.click(screen.getByRole('button', { name: /save changes/i }))

    await waitFor(() => {
      expect(saveFullConfigSection).toHaveBeenCalledWith('channels', expect.objectContaining({
        enabled: true,
        telegram: expect.objectContaining({ enabled: true, bot_token: 'telegram-token', allow_from: ['123', '456'] }),
        email: expect.objectContaining({ allow_from: ['admin@example.com', 'ops@example.com'] }),
      }))
    })
    expect(onSaved).toHaveBeenCalled()
  })
})

describe('DaemonSettings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(saveFullConfigSection).mockResolvedValue({ restart_required: true })
  })

  it('saves daemon server and authentication settings', async () => {
    const user = userEvent.setup()
    const onSaved = vi.fn()

    render(
      <DaemonSettings
        config={{
          port: 9393,
          log_dir: '/logs',
          auth: { disabled: false, session_ttl_days: 90 },
        }}
        onSaved={onSaved}
      />
    )

    fireEvent.change(screen.getByLabelText(/http port/i), { target: { value: '9494' } })
    await user.click(screen.getByRole('switch', { name: /toggle studio authentication/i }))
    await user.click(screen.getByRole('button', { name: /save changes/i }))

    await waitFor(() => {
      expect(saveFullConfigSection).toHaveBeenCalledWith('daemon', expect.objectContaining({
        port: 9494,
        log_dir: '/logs',
        auth: expect.objectContaining({ disabled: true }),
      }))
    })
    expect(onSaved).toHaveBeenCalled()
    expect(await screen.findByText(/restart the daemon/i)).toBeInTheDocument()
  })
})
