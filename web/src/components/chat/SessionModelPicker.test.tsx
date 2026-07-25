import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import SessionModelPicker from './SessionModelPicker'
import type { SessionModelStatus } from '../../api/studioChat'
import { patchSessionModel } from '../../api/studioChat'

vi.mock('../../api/studioChat', () => ({
  patchSessionModel: vi.fn(),
}))

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

const baseStatus: SessionModelStatus = {
  availableProviders: ['openai', 'anthropic', 'google'],
  effectiveProvider: 'openai',
  effectiveModel: 'gpt-4o',
  pinnedProvider: '',
  pinnedModel: '',
  credentialsAvailable: true,
}

function openPicker(statusOverrides?: Partial<SessionModelStatus>, availableProviders?: string[]) {
  const onUpdate = vi.fn()
  const status = { ...baseStatus, ...statusOverrides }
  render(
    <SessionModelPicker
      sessionId="ses-1"
      modelStatus={status}
      availableProviders={availableProviders}
      onUpdate={onUpdate}
    />
  )
  fireEvent.click(screen.getByRole('button', { name: /Model:/ }))
  return { onUpdate }
}

async function chooseProvider(user: ReturnType<typeof userEvent.setup>, name: string) {
  await user.click(screen.getByRole('combobox'))
  await user.click(await screen.findByRole('option', { name }))
}

describe('SessionModelPicker', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders trigger with default label when unpinned', () => {
    render(
      <SessionModelPicker sessionId="ses-1" modelStatus={baseStatus} onUpdate={vi.fn()} />
    )
    expect(screen.getByRole('button', { name: /Model: default/ })).toBeInTheDocument()
  })

  it('opens an opaque popover panel (bg-popover) so chat content does not bleed through', () => {
    openPicker()
    const currently = screen.getByText(/Currently:/)
    const panel = currently.closest('div.absolute')
    expect(panel).toBeTruthy()
    expect(panel?.className).toMatch(/bg-popover/)
    expect(panel?.className).toMatch(/text-popover-foreground/)
  })

  it('renders trigger with pinned provider/model label', () => {
    render(
      <SessionModelPicker
        sessionId="ses-1"
        modelStatus={{ ...baseStatus, pinnedProvider: 'anthropic', pinnedModel: 'claude-4' }}
        onUpdate={vi.fn()}
      />
    )
    expect(screen.getByRole('button', { name: /Model: anthropic\/claude-4/ })).toBeInTheDocument()
  })

  it('lists availableProviders in the select', async () => {
    const user = userEvent.setup()
    openPicker()
    await user.click(screen.getByRole('combobox'))
    expect(await screen.findByRole('option', { name: 'openai' })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'anthropic' })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'google' })).toBeInTheDocument()
  })

  it('unions extra availableProviders prop with modelStatus list', async () => {
    const user = userEvent.setup()
    openPicker({ availableProviders: ['openai'] }, ['anthropic', 'custom-llm'])
    await user.click(screen.getByRole('combobox'))
    expect(await screen.findByRole('option', { name: 'openai' })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'anthropic' })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'custom-llm' })).toBeInTheDocument()
  })

  it('shows pinned provider as selected in the combobox', () => {
    openPicker({ pinnedProvider: 'anthropic', pinnedModel: 'claude-4' })
    expect(screen.getByRole('combobox')).toHaveTextContent('anthropic')
  })

  it('shows current effective model at top', () => {
    openPicker()
    expect(screen.getByText('Currently: openai/gpt-4o')).toBeInTheDocument()
  })

  it('calls patchSessionModel with selected provider+model on Save', async () => {
    const user = userEvent.setup()
    vi.mocked(patchSessionModel).mockResolvedValue({
      pinnedProvider: 'anthropic',
      pinnedModel: 'claude-4',
      effectiveProvider: 'anthropic',
      effectiveModel: 'claude-4',
      credentialsAvailable: true,
      availableProviders: [],
    })
    openPicker()
    await chooseProvider(user, 'anthropic')
    await user.click(screen.getByTitle('Browse models'))
    await user.click(screen.getByText('Select claude-4'))
    await user.click(screen.getByRole('button', { name: /save/i }))
    await waitFor(() => expect(patchSessionModel).toHaveBeenCalledWith('ses-1', 'anthropic', 'claude-4'))
  })

  it('merges response preserving availableProviders (D5)', async () => {
    const user = userEvent.setup()
    vi.mocked(patchSessionModel).mockResolvedValue({
      pinnedProvider: 'anthropic',
      pinnedModel: 'claude-4',
      effectiveProvider: 'anthropic',
      effectiveModel: 'claude-4',
      credentialsAvailable: true,
    } as unknown as SessionModelStatus)
    const { onUpdate } = openPicker()
    await chooseProvider(user, 'anthropic')
    await user.click(screen.getByRole('button', { name: /save/i }))
    await waitFor(() =>
      expect(onUpdate).toHaveBeenCalledWith(
        expect.objectContaining({ availableProviders: ['openai', 'anthropic', 'google'] })
      )
    )
  })

  it('shows error toast on save failure', async () => {
    vi.mocked(patchSessionModel).mockRejectedValue(new Error('boom'))
    openPicker()
    fireEvent.click(screen.getByRole('button', { name: /save/i }))
    await waitFor(() => expect(screen.getByText(/boom/)).toBeInTheDocument())
  })

  it('closes popover after successful Save', async () => {
    vi.mocked(patchSessionModel).mockResolvedValue({
      pinnedProvider: 'openai',
      pinnedModel: 'gpt-4o',
      effectiveProvider: 'openai',
      effectiveModel: 'gpt-4o',
      credentialsAvailable: true,
      availableProviders: [],
    })
    openPicker()
    fireEvent.click(screen.getByRole('button', { name: /save/i }))
    await waitFor(() => expect(screen.queryByRole('combobox')).not.toBeInTheDocument())
  })

  it('does not render Reset button when no pin is set', () => {
    openPicker({ pinnedProvider: '', pinnedModel: '' })
    expect(screen.queryByText(/reset/i)).not.toBeInTheDocument()
  })

  it('renders Reset when pinned and calls patchSessionModel with empty strings', async () => {
    vi.mocked(patchSessionModel).mockResolvedValue({
      pinnedProvider: '',
      pinnedModel: '',
      effectiveProvider: 'openai',
      effectiveModel: 'gpt-4o',
      credentialsAvailable: true,
      availableProviders: [],
    })
    openPicker({ pinnedProvider: 'anthropic', pinnedModel: 'claude-4' })
    fireEvent.click(screen.getByText(/reset/i))
    await waitFor(() => expect(patchSessionModel).toHaveBeenCalledWith('ses-1', '', ''))
  })

  it('dismisses on click outside', () => {
    openPicker()
    expect(screen.getByRole('combobox')).toBeInTheDocument()
    fireEvent.mouseDown(document.body)
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
  })

  it('disables Save when availableProviders is empty', () => {
    openPicker({ availableProviders: [] })
    const saveBtn = screen.getByRole('button', { name: /save/i })
    expect(saveBtn).toBeDisabled()
    expect(saveBtn).toHaveAttribute('title', 'No providers configured')
  })

  it('opens ProviderModelSelector when browsing models', () => {
    openPicker({ pinnedProvider: 'anthropic', pinnedModel: 'claude-3' })
    fireEvent.click(screen.getByTitle('Browse models'))
    expect(screen.getByTestId('model-selector-modal')).toBeInTheDocument()
  })
})
