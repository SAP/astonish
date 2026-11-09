import { beforeEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import BotDraftDialog from '../BotDraftDialog'
import BotSidebar from '../BotSidebar'
import { BotIntakeWriter, intakeNamespace, readIntakePointer, type BotIntakeContent } from '@/api/botIntake'

const id = '10000000-0000-4000-8000-000000000001'
const namespace = intakeNamespace('owner', null, 'personal')
const content: BotIntakeContent = { mode: 'blank', name: 'Secret name', charter: 'Observe', language: 'de', timezone: 'UTC', identity: 'Observer', working_style: 'Precise', owner_preferences: 'Private', carryover: { summary: '', memories: [], goals: [], artifacts: [] } }
let stored: { id: string; owner_id: string; scope: string; revision: number; content: BotIntakeContent } | null
let failPut = false, failDelete = false, posts = 0
const requests: { method: string; body: Record<string, unknown> }[] = []
beforeEach(() => {
  localStorage.clear(); stored = null; failPut = false; failDelete = false; posts = 0; requests.length = 0
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (url === '/api/auth/me') return Response.json({ user: { id: 'owner' } })
    if (!url.includes('/intake/')) {
      if (init?.method === 'POST') { posts++; return Response.json({ id: 'created', scope: 'personal' }) }
      return Response.json({ bots: [] })
    }
    const method = init?.method || 'GET', body = JSON.parse(String(init?.body || '{}'))
    requests.push({ method, body })
    if (method === 'GET') return stored ? Response.json(stored) : new Response('Missing', { status: 404 })
    if (method === 'PUT') {
      if (failPut || body.expected_revision !== (stored?.revision || 0)) return new Response('Conflict', { status: 409 })
      stored = { id: url.split('/intake/')[1].split('?')[0], owner_id: 'owner', scope: 'personal', revision: (stored?.revision || 0) + 1, content: body.content }
      return Response.json(stored)
    }
    if (failDelete) return new Response('Offline', { status: 503 })
    stored = null; return new Response(null, { status: 204 })
  }))
})
it('saves on Escape, resumes after remount through Sidebar pointer, renews consent and never activates', async () => {
  const close = vi.fn()
  const first = render(<BotDraftDialog open onClose={close} />)
  fireEvent.change(screen.getByLabelText('Bot name'), { target: { value: 'Secret name' } })
  fireEvent.change(screen.getByLabelText('Bot charter'), { target: { value: 'Observe' } })
  fireEvent.change(screen.getByLabelText('Bot identity'), { target: { value: 'Observer' } })
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
  await waitFor(() => expect(close).toHaveBeenCalledOnce())
  expect(posts).toBe(0)
  const pointer = readIntakePointer(namespace)!
  expect(JSON.parse(localStorage.getItem(namespace)!)).toEqual({ id: pointer.id, scope: 'personal' })
  expect(localStorage.getItem(namespace)).not.toContain('Secret')
  first.unmount()
  const open = vi.fn()
  window.addEventListener('astonish:source-intake', open)
  render(<BotSidebar />)
  fireEvent.click(await screen.findByRole('button', { name: 'Resume intake' }))
  expect(open).toHaveBeenCalledOnce()
  expect((open.mock.calls[0][0] as CustomEvent).detail).toEqual({ scope: 'personal', resumeId: pointer.id })
  window.removeEventListener('astonish:source-intake', open)
  render(<BotDraftDialog inline open scope="personal" resumeId={pointer.id} onClose={vi.fn()} />)
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  await waitFor(() => expect(screen.getByLabelText('Bot name')).toHaveValue('Secret name'))
  expect(screen.getByLabelText('Bot identity')).toHaveValue('Observer')
  fireEvent.click(screen.getByRole('button', { name: 'Review draft' }))
  expect(screen.getByRole('checkbox')).not.toBeChecked()
  expect(posts).toBe(0)
})
it('preserves local text on CAS conflict and explicitly compares and reloads server text', async () => {
  stored = { id, owner_id: 'owner', scope: 'personal', revision: 2, content }
  render(<BotDraftDialog open resumeId={id} onClose={vi.fn()} />)
  await waitFor(() => expect(screen.getByLabelText('Bot name')).toHaveValue('Secret name'))
  fireEvent.change(screen.getByLabelText('Bot name'), { target: { value: 'Local edit' } })
  failPut = true
  fireEvent.click(screen.getByRole('button', { name: 'Save intake and close' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Your text is still here')
  expect(screen.getByLabelText('Bot name')).toHaveValue('Local edit')
  fireEvent.click(screen.getByRole('button', { name: 'Compare saved intake' }))
  await screen.findByLabelText('Saved intake comparison')
  expect(screen.getByLabelText('Bot name')).toHaveValue('Local edit')
  fireEvent.click(screen.getByRole('button', { name: 'Replace local text with saved copy' }))
  expect(screen.getByLabelText('Bot name')).toHaveValue('Secret name')
})
it('retains intake on failed discard and retries cleanup without duplicate reviewed creation', async () => {
  stored = { id, owner_id: 'owner', scope: 'personal', revision: 2, content }
  const close = vi.fn()
  render(<BotDraftDialog open resumeId={id} onClose={close} />)
  await waitFor(() => expect(screen.getByLabelText('Bot name')).toHaveValue('Secret name'))
  failDelete = true
  fireEvent.click(screen.getByRole('button', { name: 'Cancel intake' }))
  fireEvent.click(screen.getByRole('button', { name: 'Discard intake' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Intake retained')
  expect(close).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Keep editing' }))
  fireEvent.click(screen.getByRole('button', { name: 'Review draft' }))
  fireEvent.click(screen.getByRole('checkbox'))
  fireEvent.click(screen.getByRole('button', { name: 'Save reviewed draft' }))
  await screen.findByRole('button', { name: 'Retry intake cleanup' })
  expect(posts).toBe(1)
  failDelete = false
  fireEvent.click(screen.getByRole('button', { name: 'Retry intake cleanup' }))
  await waitFor(() => expect(close).toHaveBeenCalledOnce())
  expect(posts).toBe(1)
  expect(stored).toBeNull()
})
it('orders saves, suppresses queued stale saves after discard and isolates pointer namespaces', async () => {
  const writer = new BotIntakeWriter(id, 'personal', namespace, null)
  await writer.save(content)
  const queued = writer.save({ ...content, name: 'Stale' })
  const discarded = writer.discard()
  await Promise.all([queued, discarded])
  expect(requests.map(r => r.method)).toEqual(['PUT', 'DELETE'])
  expect(stored).toBeNull()
  await expect(writer.save(content)).rejects.toThrow('closed')
  expect(readIntakePointer(intakeNamespace('other', null, 'personal'))).toBeNull()
  expect(readIntakePointer(intakeNamespace('owner', 'other-team', 'personal'))).toBeNull()
  expect(readIntakePointer(intakeNamespace('owner', null, 'team'))).toBeNull()
})
it('waits for an in-flight PUT before DELETE and uses the resulting revision', async () => {
  const realFetch = globalThis.fetch
  let release!: () => void
  let started!: () => void
  const startedPromise = new Promise<void>(resolve => { started = resolve })
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'PUT') { started(); await new Promise<void>(resolve => { release = resolve }) }
    return realFetch(url, init)
  }))
  const writer = new BotIntakeWriter(id, 'personal', namespace, null)
  const save = writer.save(content)
  await startedPromise
  const discard = writer.discard()
  expect(requests).toHaveLength(0)
  release()
  await Promise.all([save, discard])
  expect(requests.map(r => r.method)).toEqual(['PUT', 'DELETE'])
  expect(requests[1].body.expected_revision).toBe(1)
  expect(readIntakePointer(namespace)).toBeNull()
})

it('resumes Chat carryover and preserves adaptive mode across saves', async () => {
  stored = { id, owner_id: 'owner', scope: 'personal', revision: 1, content: { ...content, mode: 'chat', source_id: id, carryover: { ...content.carryover, summary: 'Selected summary', memories: [{ content: 'Fact', category: 'fact' }] } } }
  const first = render(<BotDraftDialog open resumeId={id} onClose={vi.fn()} />)
  await waitFor(() => expect(screen.getByLabelText('Carryover summary')).toHaveValue('Selected summary'))
  expect(screen.getByLabelText('Memory content 1')).toHaveValue('Fact')
  expect(posts).toBe(0)
  first.unmount()
  stored.content = { ...content, mode: 'adaptive', source_id: id }
  render(<BotDraftDialog open resumeId={id} onClose={vi.fn()} />)
  await waitFor(() => expect(screen.getByLabelText('Bot name')).toHaveValue('Secret name'))
  fireEvent.change(screen.getByLabelText('Bot name'), { target: { value: 'Adaptive edit' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save intake and close' }))
  await waitFor(() => expect(stored?.content.name).toBe('Adaptive edit'))
  expect(stored?.content.mode).toBe('adaptive')
  expect(stored?.content.source_id).toBe(id)
})

it('does not offer another owner, team or scope pointer in Sidebar', async () => {
  localStorage.setItem(intakeNamespace('other', null, 'personal'), JSON.stringify({ id, scope: 'personal' }))
  localStorage.setItem(intakeNamespace('owner', 'another-team', 'personal'), JSON.stringify({ id, scope: 'personal' }))
  localStorage.setItem(intakeNamespace('owner', null, 'team'), JSON.stringify({ id, scope: 'team' }))
  render(<BotSidebar />)
  await screen.findByText('No Bots yet. Create a reviewed draft.')
  expect(screen.queryByRole('button', { name: 'Resume intake' })).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Team' }))
  expect(await screen.findByRole('button', { name: 'Resume intake' })).toBeInTheDocument()
})

it('blocks silently changing a resumed template revision and retains its source on save', async () => {
  stored = { id, owner_id: 'owner', scope: 'personal', revision: 1, content: { ...content, mode: 'template', source_id: id, template_revision: 3 } }
  const realFetch = globalThis.fetch
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => url.includes('/templates/')
    ? Response.json({ id, revision: 4, definition: { name: 'Changed', charter: 'Changed charter' } }) : realFetch(url, init)))
  render(<BotDraftDialog open resumeId={id} onClose={vi.fn()} />)
  expect(await screen.findByRole('alert')).toHaveTextContent('Template changed')
  expect(screen.getByLabelText('Bot name')).toHaveValue('Secret name')
  expect(screen.getByRole('button', { name: 'Review draft' })).toBeDisabled()
  fireEvent.change(screen.getByLabelText('Bot name'), { target: { value: 'Preserved' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save intake and close' }))
  await waitFor(() => expect(stored?.content.name).toBe('Preserved'))
  expect(stored?.content.template_revision).toBe(3)
  expect(stored?.content.mode).toBe('template')
  expect(posts).toBe(0)
})

it('uses exact idempotent retries and rejects oversized input before sending', async () => {
  const writer = new BotIntakeWriter(id, 'personal', namespace, null)
  failPut = true
  await expect(writer.save(content)).rejects.toThrow('Conflict')
  failPut = false
  await writer.save(content)
  expect(requests[0].body).toEqual(requests[1].body)
  await expect(writer.save({ ...content, charter: '😀'.repeat(20000) })).rejects.toThrow('64 KiB')
  expect(requests).toHaveLength(2)
})
