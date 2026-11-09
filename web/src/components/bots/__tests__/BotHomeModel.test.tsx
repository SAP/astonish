import { beforeEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import BotHomeModel from '../BotHomeModel'
import { teamFetch } from '@/api/teamContext'
vi.mock('@/api/teamContext', () => ({ teamFetch: vi.fn() }))
vi.mock('../../ProviderModelSelector', () => ({ default: ({ isOpen, onSelect }: { isOpen: boolean; onSelect: (model: string) => void }) => isOpen ? <button onClick={() => onSelect('selected-model')}>Choose configured model</button> : null }))
const status = { pinnedProvider: 'configured-provider', pinnedModel: '', effectiveProvider: '', effectiveModel: '', availableProviders: ['configured-provider'] }
beforeEach(() => vi.resetAllMocks())
it('pins a chosen model through the existing retained-session PATCH, even without an effective default', async () => {
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(JSON.stringify(status))).mockResolvedValueOnce(new Response(JSON.stringify({ ...status, pinnedModel: 'selected-model' })))
  render(<BotHomeModel sessionId="retained/home" />)
  fireEvent.click(await screen.findByRole('button', { name: 'Model: configured-provider' }))
  fireEvent.click(screen.getByRole('button', { name: 'Click to browse models…' }))
  fireEvent.click(screen.getByRole('button', { name: 'Choose configured model' }))
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(teamFetch).toHaveBeenCalledWith('/api/studio/sessions/retained%2Fhome/model', expect.objectContaining({ method: 'PATCH', body: JSON.stringify({ provider: 'configured-provider', model: 'selected-model' }) })))
  expect(await screen.findByRole('button', { name: 'Model: configured-provider/selected-model' })).toBeInTheDocument()
  expect(teamFetch).toHaveBeenCalledTimes(2)
})
it('keeps failed model reads visible and retries explicitly', async () => {
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response('unavailable', { status: 503 })).mockResolvedValueOnce(new Response(JSON.stringify(status)))
  render(<BotHomeModel sessionId="home" />)
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not load retained home model')
  fireEvent.click(screen.getByRole('button', { name: 'Retry home model' }))
  expect(await screen.findByRole('button', { name: 'Model: configured-provider' })).toBeInTheDocument()
})
