import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import PreChatModelPicker from './PreChatModelPicker'

// Mock ProviderModelSelector since it does network calls
vi.mock('../ProviderModelSelector', () => ({
  default: ({ isOpen, onClose, onSelect, currentModel }: { isOpen: boolean; onClose: () => void; onSelect: (id: string) => void; currentModel?: string }) => {
    if (!isOpen) return null
    return (
      <div data-testid="model-selector-modal">
        <span data-testid="current-model">{currentModel}</span>
        <button onClick={() => onSelect('claude-4')}>Select claude-4</button>
        <button onClick={onClose}>Close modal</button>
      </div>
    )
  },
}))

function renderPicker(overrides?: {
  availableProviders?: string[]
  provider?: string
  model?: string
}) {
  const onChange = vi.fn()
  render(
    <PreChatModelPicker
      availableProviders={overrides?.availableProviders ?? ['openai', 'anthropic', 'google']}
      provider={overrides?.provider ?? ''}
      model={overrides?.model ?? ''}
      onChange={onChange}
    />
  )
  return { onChange }
}

async function chooseProvider(user: ReturnType<typeof userEvent.setup>, name: string) {
  await user.click(screen.getByRole('combobox'))
  await user.click(await screen.findByRole('option', { name }))
}

describe('PreChatModelPicker', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders with default label when provider/model empty', () => {
    renderPicker()
    expect(screen.getByText(/Model:/)).toBeInTheDocument()
    expect(screen.getByText(/default/)).toBeInTheDocument()
  })

  it('renders with provider/model label when provided', () => {
    renderPicker({ provider: 'openai', model: 'gpt-4o' })
    expect(screen.getByRole('button', { name: /Model: openai\/gpt-4o/ })).toBeInTheDocument()
  })

  it('opens dropdown on button click', async () => {
    const user = userEvent.setup()
    renderPicker()
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Model:/ }))
    expect(screen.getByRole('combobox')).toBeInTheDocument()
  })

  it('opens an opaque popover panel (bg-popover) so content does not bleed through', async () => {
    const user = userEvent.setup()
    renderPicker()
    await user.click(screen.getByRole('button', { name: /Model:/ }))
    const label = screen.getByText('Provider')
    const panel = label.closest('div.absolute')
    expect(panel).toBeTruthy()
    expect(panel?.className).toMatch(/bg-popover/)
    expect(panel?.className).toMatch(/text-popover-foreground/)
  })

  it('calls onChange with selected provider+model on Apply', async () => {
    const user = userEvent.setup()
    const { onChange } = renderPicker()
    await user.click(screen.getByRole('button', { name: /Model:/ }))
    await chooseProvider(user, 'anthropic')
    await user.click(screen.getByText(/Click to browse models/))
    await user.click(screen.getByText('Select claude-4'))
    await user.click(screen.getByRole('button', { name: /apply/i }))
    expect(onChange).toHaveBeenCalledWith('anthropic', 'claude-4')
    expect(onChange).toHaveBeenCalledTimes(1)
  })

  it('calls onChange with empty strings on Reset', async () => {
    const user = userEvent.setup()
    const { onChange } = renderPicker({ provider: 'openai', model: 'gpt-4o' })
    await user.click(screen.getByRole('button', { name: /Model:/ }))
    await user.click(screen.getByRole('button', { name: /reset/i }))
    expect(onChange).toHaveBeenCalledWith('', '')
    expect(onChange).toHaveBeenCalledTimes(1)
  })

  it('closes dropdown after Apply', async () => {
    const user = userEvent.setup()
    renderPicker()
    await user.click(screen.getByRole('button', { name: /Model:/ }))
    expect(screen.getByRole('combobox')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /apply/i }))
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
  })

  it('closes dropdown on click outside', async () => {
    const user = userEvent.setup()
    renderPicker()
    await user.click(screen.getByRole('button', { name: /Model:/ }))
    expect(screen.getByRole('combobox')).toBeInTheDocument()
    fireEvent.mouseDown(document.body)
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
  })

  it('populates select from availableProviders prop', async () => {
    const user = userEvent.setup()
    renderPicker({ availableProviders: ['openai', 'anthropic'] })
    await user.click(screen.getByRole('button', { name: /Model:/ }))
    await user.click(screen.getByRole('combobox'))
    expect(await screen.findByRole('option', { name: 'openai' })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'anthropic' })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: '(default — cascade)' })).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'google' })).not.toBeInTheDocument()
  })

  it('syncs local state when props change', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    const { rerender } = render(
      <PreChatModelPicker
        availableProviders={['openai', 'anthropic']}
        provider=""
        model=""
        onChange={onChange}
      />
    )
    await user.click(screen.getByRole('button', { name: /Model:/ }))
    rerender(
      <PreChatModelPicker
        availableProviders={['openai', 'anthropic']}
        provider="openai"
        model="gpt-4o"
        onChange={onChange}
      />
    )
    expect(screen.getByRole('combobox')).toHaveTextContent('openai')
    expect(screen.getByText('gpt-4o')).toBeInTheDocument()
  })

  it('does not call onChange without user action', async () => {
    const user = userEvent.setup()
    const { onChange } = renderPicker({ provider: 'openai', model: 'gpt-4o' })
    await user.click(screen.getByRole('button', { name: /Model:/ }))
    await user.click(screen.getByTitle('Browse models'))
    await user.click(screen.getByText('Select claude-4'))
    expect(onChange).not.toHaveBeenCalled()
  })

  it('disables model browse button when no provider selected', async () => {
    const user = userEvent.setup()
    renderPicker()
    await user.click(screen.getByRole('button', { name: /Model:/ }))
    const browseBtn = screen.getByTitle('Select a provider first')
    expect(browseBtn).toBeDisabled()
  })

  it('clears model when provider changes', async () => {
    const user = userEvent.setup()
    const { onChange } = renderPicker({ provider: 'openai', model: 'gpt-4o' })
    await user.click(screen.getByRole('button', { name: /Model:/ }))
    await chooseProvider(user, 'anthropic')
    await user.click(screen.getByRole('button', { name: /apply/i }))
    expect(onChange).toHaveBeenCalledWith('anthropic', '')
  })
})
