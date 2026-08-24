import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import SlidesView from '../SlidesView'

// Mock the entire slides API module so the Templates area uses controlled data.
vi.mock('@/api/slides', async () => {
  const actual = await vi.importActual<typeof import('@/api/slides')>('@/api/slides')
  return {
    ...actual,
    listSlidesDecks: vi.fn().mockResolvedValue({ decks: [] }),
    listSlidesTemplates: vi.fn(),
    deleteSlidesTemplate: vi.fn().mockResolvedValue(undefined),
    duplicateSlidesTemplate: vi.fn().mockResolvedValue({ template: { name: 'corp-copy', label: 'Corp (copy)' } }),
    recolorSlidesTemplate: vi.fn().mockResolvedValue(undefined),
  }
})

import {
  listSlidesTemplates,
  deleteSlidesTemplate,
  duplicateSlidesTemplate,
  recolorSlidesTemplate,
  type SlidesTemplate,
} from '@/api/slides'

const builtin: SlidesTemplate = {
  name: 'midnight',
  label: 'Midnight',
  scope: 'builtin',
  tokens: { surface: '#0b1020', ink: '#e6e9ef', accent: '#7c5cff' },
  archetypeKinds: ['title', 'section', 'content'],
}

const personal: SlidesTemplate = {
  name: 'corp',
  label: 'Corp Deck',
  scope: 'personal',
  tokens: { surface: '#ffffff', ink: '#101010', accent: '#ff8800' },
  archetypeKinds: ['title', 'title-2', 'content'],
  // Rich {kind,label} variants (imported template): ONE master + many layouts,
  // so the same role can have multiple variants each labeled with the real
  // PowerPoint layout name. The UI must render every label as its own chip.
  archetypes: [
    { kind: 'title', label: 'Blue cover, anvil and image', tier: 'fixed', fillSlots: ['ph-title'] },
    { kind: 'title-2', label: 'Pink cover with anvil', tier: 'fixed', fillSlots: ['ph-title'] },
    { kind: 'content', label: 'Title and Content', tier: 'flexible' },
  ],
}

describe('SlidesView templates area', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    ;(listSlidesTemplates as ReturnType<typeof vi.fn>).mockResolvedValue({ templates: [builtin, personal] })
  })

  const renderArea = () => render(<SlidesView theme="dark" templatesView onNavigate={() => {}} />)

  it('renders scope badges for each template', async () => {
    renderArea()
    await waitFor(() => expect(screen.getAllByTestId('template-card')).toHaveLength(2))
    const scopeBadges = screen.getAllByTestId('template-scope-badge').map(el => el.textContent)
    expect(scopeBadges).toContain('Built-in')
    expect(scopeBadges).toContain('Personal')
  })

  it('shows Delete only for the scoped template, not the built-in', async () => {
    renderArea()
    await waitFor(() => expect(screen.getAllByTestId('template-card')).toHaveLength(2))
    // One personal (deletable) template -> exactly one Delete button.
    expect(screen.getAllByTestId('template-delete')).toHaveLength(1)
    // Built-in has no recolor either.
    expect(screen.getAllByTestId('template-recolor')).toHaveLength(1)
  })

  it('calls duplicateSlidesTemplate when Duplicate is clicked', async () => {
    renderArea()
    await waitFor(() => expect(screen.getAllByTestId('template-card')).toHaveLength(2))
    fireEvent.click(screen.getAllByTestId('template-duplicate')[0])
    await waitFor(() => expect(duplicateSlidesTemplate).toHaveBeenCalledTimes(1))
    expect((duplicateSlidesTemplate as ReturnType<typeof vi.fn>).mock.calls[0][0]).toBe('midnight')
  })

  it('calls recolorSlidesTemplate when submitting the color form', async () => {
    renderArea()
    await waitFor(() => expect(screen.getAllByTestId('template-card')).toHaveLength(2))
    fireEvent.click(screen.getByTestId('template-recolor'))
    await waitFor(() => expect(screen.getByTestId('template-recolor-save')).toBeInTheDocument())
    fireEvent.click(screen.getByTestId('template-recolor-save'))
    await waitFor(() => expect(recolorSlidesTemplate).toHaveBeenCalledTimes(1))
    expect((recolorSlidesTemplate as ReturnType<typeof vi.fn>).mock.calls[0][0]).toBe('corp')
  })

  it('calls deleteSlidesTemplate after confirming the modal', async () => {
    renderArea()
    await waitFor(() => expect(screen.getAllByTestId('template-card')).toHaveLength(2))
    fireEvent.click(screen.getByTestId('template-delete'))
    await waitFor(() => expect(screen.getByTestId('template-delete-confirm')).toBeInTheDocument())
    fireEvent.click(screen.getByTestId('template-delete-confirm'))
    await waitFor(() => expect(deleteSlidesTemplate).toHaveBeenCalledTimes(1))
    expect((deleteSlidesTemplate as ReturnType<typeof vi.fn>).mock.calls[0][0]).toBe('corp')
  })

  it('renders human-friendly variant labels when archetypes carry labels', async () => {
    renderArea()
    await waitFor(() => expect(screen.getAllByTestId('template-card')).toHaveLength(2))
    // The imported (personal) template exposes {kind,label} variants; the UI
    // should prefer the label over the bare kind.
    expect(screen.getByText('Blue cover, anvil and image')).toBeInTheDocument()
    expect(screen.getByText('Title and Content')).toBeInTheDocument()
    // The built-in template has no labels, so its bare kinds still render.
    expect(screen.getByText('section')).toBeInTheDocument()
  })

  it('renders multiple same-role variant chips with their real layout-name labels', async () => {
    renderArea()
    await waitFor(() => expect(screen.getAllByTestId('template-card')).toHaveLength(2))
    // ONE .pptx template exposes many role-classified variants; two distinct
    // "title"-role covers must BOTH render as chips by their real PowerPoint
    // layout names (not collapsed to a single "title" chip).
    expect(screen.getByText('Blue cover, anvil and image')).toBeInTheDocument()
    expect(screen.getByText('Pink cover with anvil')).toBeInTheDocument()
  })
})
