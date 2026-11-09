import { beforeEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import NewBotDialog from '../NewBotDialog'

beforeEach(() => { localStorage.clear(); window.location.hash = '#/chat' })
it('creates a private conversation with server defaults, stable retries and no execution request', async () => {
  let fail = true
  const requests: Record<string, unknown>[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    expect(url).toBe('/api/studio/bots?scope=personal')
    expect(init?.method).toBe('POST')
    requests.push(JSON.parse(String(init?.body)))
    return fail ? new Response('Offline', { status: 503 }) : Response.json({ id: 'new', scope: 'personal' })
  }))
  const close = vi.fn()
  render(<NewBotDialog onClose={close} onSources={vi.fn()} />)
  expect(screen.getAllByRole('textbox')).toHaveLength(1)
  expect(screen.getByRole('button', { name: 'Start conversation' })).toBeDisabled()
  fireEvent.change(screen.getByLabelText('Bot name'), { target: { value: '  Watcher  ' } })
  fireEvent.click(screen.getByRole('button', { name: 'Start conversation' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Offline')
  expect(screen.getByLabelText('Bot name')).toHaveValue('  Watcher  ')
  expect(close).not.toHaveBeenCalled()
  fail = false
  fireEvent.click(screen.getByRole('button', { name: 'Start conversation' }))
  await waitFor(() => expect(close).toHaveBeenCalledOnce())
  expect(requests).toHaveLength(2)
  expect(requests[0]).toEqual({ name: 'Watcher', start_from_title: true, idempotency_key: expect.any(String) })
  expect(requests[1]).toEqual(requests[0])
  expect(window.location.hash).toBe('#/chat/bots/personal/new')
})
it('opens source intake only on explicit selection and never creates a Bot', () => {
  const sources = vi.fn(), fetch = vi.fn()
  vi.stubGlobal('fetch', fetch)
  render(<NewBotDialog onClose={vi.fn()} onSources={sources} />)
  fireEvent.click(screen.getByRole('button', { name: 'Use a template or existing schedule' }))
  expect(sources).toHaveBeenCalledOnce()
  expect(fetch).not.toHaveBeenCalled()
})
