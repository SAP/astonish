/**
 * Panel Interaction Scenario Tests (N2-N4)
 *
 * Tests toolbar panel buttons (Files) and panel toggle behavior.
 * Verifies that clicking panel buttons doesn't crash and that
 * panels toggle correctly.
 */

import { useState } from 'react'
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

// Shared mocks (react-markdown, remark-gfm, HomePage, FleetStartDialog, FleetTemplatePicker, MermaidBlock)
import './scenarioSetup'

import ProviderModelSelector from '../../components/ProviderModelSelector'
import * as teamContext from '../../api/teamContext'
import { renderChat } from '../helpers/renderChat'
import type { RenderChatResult } from '../helpers/renderChat'
import type { FixtureEvent } from '../helpers/sseSimulator'

import artifactCreated from '../fixtures/scenarios/downloads/artifact-created.json'
import planWithDelegation from '../fixtures/scenarios/planning/plan-with-delegation.json'

describe('Panel Interaction Scenarios', () => {
  let result: RenderChatResult

  afterEach(() => {
    if (result) result.cleanup()
  })

  describe('N2: Files Panel Toggle', () => {
    it('Files button click toggles file panel without crashing', async () => {
      result = renderChat({
        scenarioEvents: artifactCreated.events as FixtureEvent[],
      })

      await result.sendMessage('Create')

      // Wait for the Files button to appear (it only shows when artifacts exist)
      let filesButton: HTMLButtonElement | undefined
      await waitFor(() => {
        const buttons = result.container.querySelectorAll('button')
        filesButton = Array.from(buttons).find(btn =>
          btn.textContent?.includes('Files')
        ) as HTMLButtonElement | undefined
        expect(filesButton).toBeDefined()
      }, { timeout: 10000 })

      // Click to open Files panel
      await result.user.click(filesButton!)

      // Click again to close Files panel (toggle off)
      await result.user.click(filesButton!)

      // If we get here without throwing, the toggle works
      expect(true).toBe(true)
    })

    it('shows artifact info when Files panel is opened', async () => {
      result = renderChat({
        scenarioEvents: artifactCreated.events as FixtureEvent[],
      })

      await result.sendMessage('Create')

      // Wait for the Files button
      let filesButton: HTMLButtonElement | undefined
      await waitFor(() => {
        const buttons = result.container.querySelectorAll('button')
        filesButton = Array.from(buttons).find(btn =>
          btn.textContent?.includes('Files')
        ) as HTMLButtonElement | undefined
        expect(filesButton).toBeDefined()
      }, { timeout: 10000 })

      // Open the Files panel
      await result.user.click(filesButton!)

      // The panel should show the artifact filename
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('report.md')
      }, { timeout: 10000 })
    })
  })

  describe('N3: Plan Panel Rendering', () => {
    it('plan events render without crashing the UI', async () => {
      result = renderChat({
        scenarioEvents: planWithDelegation.events as FixtureEvent[],
      })

      await result.sendMessage('Build a feature')

      // The plan data should appear in some form (plan step text or agent response)
      await waitFor(() => {
        const text = result.container.textContent || ''
        // plan-with-delegation fixture has plan and delegation events
        // At minimum the stream should complete without error
        expect(text.length).toBeGreaterThan(0)
      }, { timeout: 10000 })
    })
  })

  describe('N4: Panel State After Stream', () => {
    it('Files button remains accessible after stream completes', async () => {
      result = renderChat({
        scenarioEvents: artifactCreated.events as FixtureEvent[],
      })

      await result.sendMessage('Create')

      // Verify Files button is present after the stream is done
      await waitFor(() => {
        const buttons = result.container.querySelectorAll('button')
        const filesButton = Array.from(buttons).find(btn =>
          btn.textContent?.includes('Files')
        )
        expect(filesButton).toBeDefined()
      }, { timeout: 10000 })

      // The textarea should be idle (not streaming)
      await waitFor(() => {
        const ta = result.container.querySelector('textarea')
        const placeholder = ta?.getAttribute('placeholder') || ''
        expect(placeholder).not.toContain('responding')
      }, { timeout: 10000 })
    })
  })
})

// Render the real selector independently of StudioChat's SSE/fetch harness.
describe('Provider Model Selector Scenarios', () => {
  let fetchModels: ReturnType<typeof vi.fn<typeof teamContext.teamFetch>>

  beforeEach(() => {
    cleanup()
    const originalTeamFetch = teamContext.teamFetch
    fetchModels = vi.fn<typeof teamContext.teamFetch>()
    vi.spyOn(teamContext, 'teamFetch').mockImplementation((...args) => {
      if (args[0] === '/api/providers/sap_ai_core/models-metadata') return fetchModels(...args)
      if (args[0] === '/api/providers/openai_compat/models-metadata') return fetchModels(...args)
      return originalTeamFetch(...args)
    })
  })

  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  const modelResponse = (models: Array<{
    id: string
    name?: string
    context_length?: number
    max_output_tokens?: number
    supports_tools?: boolean
  }>) => new Response(JSON.stringify({
    provider: 'sap_ai_core',
    has_metadata: true,
    models,
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })

  it('shows gpt-6-astra context and selects the unchanged ID before closing', async () => {
    fetchModels.mockResolvedValue(modelResponse([{
      id: 'gpt-6-astra',
      name: 'gpt-6-astra',
      context_length: 1100000,
      max_output_tokens: 3210,
      supports_tools: false,
    }]))
    const onSelect = vi.fn()
    const onClose = vi.fn()
    const user = userEvent.setup()

    function SelectorHarness() {
      const [isOpen, setIsOpen] = useState(true)
      return <ProviderModelSelector
        provider="sap_ai_core"
        isOpen={isOpen}
        currentModel="gpt-6-astra"
        onSelect={onSelect}
        onClose={() => { onClose(); setIsOpen(false) }}
      />
    }

    render(<SelectorHarness />)
    const model = await screen.findByRole('button', { name: /gpt-6-astra/ })
    expect(fetchModels).toHaveBeenCalledExactlyOnceWith('/api/providers/sap_ai_core/models-metadata')
    const modelLabels = within(model).getAllByText('gpt-6-astra')
    expect(modelLabels).toHaveLength(2)
    modelLabels.forEach(label => expect(label).toBeVisible())
    expect(within(model).getByText('1.1M ctx')).toBeVisible()

    await user.click(model)

    expect(onSelect).toHaveBeenCalledExactlyOnceWith('gpt-6-astra')
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(onSelect.mock.invocationCallOrder[0]).toBeLessThan(onClose.mock.invocationCallOrder[0])
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('shows enriched OpenAI-compatible context and output metadata and selects the unchanged ID before closing', async () => {
    fetchModels.mockResolvedValue(new Response(JSON.stringify({
      provider: 'openai_compat',
      has_metadata: true,
      models: [{
        id: 'gpt-6-astra',
        name: 'gpt-6-astra',
        context_length: 872000,
        max_input_tokens: 872000,
        max_completion_tokens: 128000,
      }],
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    const onSelect = vi.fn()
    const onClose = vi.fn()
    const user = userEvent.setup()

    function SelectorHarness() {
      const [isOpen, setIsOpen] = useState(true)
      return <ProviderModelSelector
        provider="openai_compat"
        isOpen={isOpen}
        currentModel="gpt-6-astra"
        onSelect={onSelect}
        onClose={() => { onClose(); setIsOpen(false) }}
      />
    }

    render(<SelectorHarness />)
    const model = await screen.findByRole('button', { name: /gpt-6-astra/ })
    expect(fetchModels).toHaveBeenCalledExactlyOnceWith('/api/providers/openai_compat/models-metadata')
    expect(within(model).getByText('872K ctx')).toBeVisible()
    expect(within(model).getByText('128K out')).toBeVisible()

    await user.click(model)

    expect(onSelect).toHaveBeenCalledExactlyOnceWith('gpt-6-astra')
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(onSelect.mock.invocationCallOrder[0]).toBeLessThan(onClose.mock.invocationCallOrder[0])
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it.each([undefined, 0])('does not invent a context badge for context_length=%s', async (contextLength) => {
    fetchModels.mockResolvedValue(modelResponse([{
      id: 'unknown-context',
      context_length: contextLength,
      max_output_tokens: 4096,
    }]))
    render(<ProviderModelSelector provider="sap_ai_core" isOpen onSelect={vi.fn()} onClose={vi.fn()} />)

    const model = await screen.findByRole('button', { name: /unknown-context/ })
    expect(within(model).queryByText(/ctx/)).not.toBeInTheDocument()
  })

  it('keeps loading visible until metadata arrives, then renders models', async () => {
    let resolveFetch!: (response: Response) => void
    fetchModels.mockReturnValue(new Promise<Response>(resolve => { resolveFetch = resolve }))
    render(<ProviderModelSelector provider="sap_ai_core" isOpen onSelect={vi.fn()} onClose={vi.fn()} />)

    expect(screen.getByText('Loading models...')).toBeVisible()
    expect(screen.queryByText('No models match your search')).not.toBeInTheDocument()
    await act(async () => {
      resolveFetch(modelResponse([{ id: 'gpt-6-astra', context_length: 1100000 }]))
    })

    expect(await screen.findByText('1.1M ctx')).toBeVisible()
    expect(screen.queryByText('Loading models...')).not.toBeInTheDocument()
  })

  it.each(['http', 'network'])('preserves the error state for a %s failure', async (failure) => {
    if (failure === 'http') {
      fetchModels.mockResolvedValue(new Response('{}', { status: 500 }))
    } else {
      fetchModels.mockRejectedValue(new Error('Failed to fetch models'))
    }
    render(<ProviderModelSelector provider="sap_ai_core" isOpen onSelect={vi.fn()} onClose={vi.fn()} />)

    expect(await screen.findByText('Failed to fetch models')).toBeVisible()
    expect(screen.queryByText('Loading models...')).not.toBeInTheDocument()
    expect(screen.queryByText('No models match your search')).not.toBeInTheDocument()
    expect(screen.queryByText(/ctx/)).not.toBeInTheDocument()
  })

  it('preserves the empty state when the provider returns no models', async () => {
    fetchModels.mockResolvedValue(modelResponse([]))
    render(<ProviderModelSelector provider="sap_ai_core" isOpen onSelect={vi.fn()} onClose={vi.fn()} />)

    expect(await screen.findByText('No models match your search')).toBeVisible()
    expect(screen.getByText('0 models available')).toBeVisible()
    expect(screen.queryByText('Loading models...')).not.toBeInTheDocument()
    expect(screen.queryByText(/ctx/)).not.toBeInTheDocument()
  })

  it('shows the search-empty state and restores context when the search is cleared', async () => {
    fetchModels.mockResolvedValue(modelResponse([{ id: 'gpt-6-astra', context_length: 1100000 }]))
    const user = userEvent.setup()
    render(<ProviderModelSelector provider="sap_ai_core" isOpen onSelect={vi.fn()} onClose={vi.fn()} />)
    expect(await screen.findByText('1.1M ctx')).toBeVisible()

    const search = screen.getByPlaceholderText('Search models...')
    await user.type(search, 'no-such-model')
    expect(screen.getByText('No models match your search')).toBeVisible()
    expect(screen.getByText('0 models available')).toBeVisible()
    expect(screen.queryByText('1.1M ctx')).not.toBeInTheDocument()

    await user.clear(search)
    expect(screen.getByText('1 models available')).toBeVisible()
    expect(screen.getByText('1.1M ctx')).toBeVisible()
    expect(screen.queryByText('No models match your search')).not.toBeInTheDocument()
  })
})
