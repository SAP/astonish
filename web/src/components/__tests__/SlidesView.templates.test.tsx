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
    importSlidesTemplate: vi.fn().mockResolvedValue({ template: { name: 'imported', label: 'Imported Theme' } }),
  }
})

import {
  listSlidesTemplates,
  deleteSlidesTemplate,
  duplicateSlidesTemplate,
  recolorSlidesTemplate,
  importSlidesTemplate,
  type SlidesTemplate,
} from '@/api/slides'

if (typeof (globalThis as { ResizeObserver?: unknown }).ResizeObserver === 'undefined') {
  ;(globalThis as { ResizeObserver?: unknown }).ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

const builtin: SlidesTemplate = {
  name: 'midnight',
  label: 'Midnight',
  scope: 'builtin',
  tokens: { surface: '#0b1020', ink: '#e6e9ef', accent: '#7c5cff' },
  cover: {
    kind: 'title',
    markup: '<ast-slide id="s0"><ast-text id="h" x="0" y="0" w="1920" h="200">Midnight cover</ast-text></ast-slide>',
  },
}

const personal: SlidesTemplate = {
  name: 'corp',
  label: 'Corp Deck',
  scope: 'personal',
  tokens: { surface: '#ffffff', ink: '#101010', accent: '#ff8800' },
  cover: { kind: 'title', thumbnailRef: 'thumb/title' },
  // Catalog variants still arrive on the list DTO but must not be listed as
  // chips on the library card (imported templates can have dozens of layouts).
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

  it('shows Platform and Organization badges and hides delete on inherited templates', async () => {
    ;(listSlidesTemplates as ReturnType<typeof vi.fn>).mockResolvedValue({
      templates: [
        builtin,
        personal,
        { name: 'acme', label: 'Acme', scope: 'org', cover: { kind: 'title', thumbnailRef: 'thumb/title' } },
        { name: 'global', label: 'Global', scope: 'platform', cover: { kind: 'title', thumbnailRef: 'thumb/title' } },
      ],
    })
    renderArea()
    await waitFor(() => expect(screen.getAllByTestId('template-card')).toHaveLength(4))
    const badges = screen.getAllByTestId('template-scope-badge').map(el => el.textContent)
    expect(badges).toContain('Organization')
    expect(badges).toContain('Platform')
    expect(screen.getAllByTestId('template-delete')).toHaveLength(1)
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

  it('renders a cover thumbnail and the template name, not layout chips or swatches', async () => {
    const { container } = renderArea()
    await waitFor(() => expect(screen.getAllByTestId('template-card')).toHaveLength(2))

    expect(screen.getByText('Midnight')).toBeInTheDocument()
    expect(screen.getByText('Corp Deck')).toBeInTheDocument()
    expect(screen.getAllByTestId('template-cover')).toHaveLength(2)

    const baked = screen.getByAltText('Corp Deck') as HTMLImageElement
    expect(baked.getAttribute('src')).toContain('/slides/templates/corp/thumbnails/title')

    await waitFor(() => {
      expect(container.querySelector('ast-deck')?.textContent).toContain('Midnight cover')
    })

    expect(screen.queryByText('Blue cover, anvil and image')).not.toBeInTheDocument()
    expect(screen.queryByText('Pink cover with anvil')).not.toBeInTheDocument()
    expect(screen.queryByText('Title and Content')).not.toBeInTheDocument()
    expect(screen.queryByTestId('template-swatches')).not.toBeInTheDocument()
  })

  it('shows the Import .pptx button in the templates header', async () => {
    renderArea()
    await waitFor(() => expect(screen.getAllByTestId('template-card')).toHaveLength(2))
    expect(screen.getByTestId('template-import-button')).toBeInTheDocument()
    expect(screen.getByTestId('template-import-button')).toHaveTextContent('Import .pptx')
  })

  it('imports a .pptx template via the hidden input and refetches the list', async () => {
    renderArea()
    await waitFor(() => expect(screen.getAllByTestId('template-card')).toHaveLength(2))

    const input = screen.getByTestId('template-import-input') as HTMLInputElement
    const file = new File(['pptx'], 'deck.pptx', { type: 'application/vnd.openxmlformats-officedocument.presentationml.presentation' })
    fireEvent.change(input, { target: { files: [file] } })

    await waitFor(() => expect(importSlidesTemplate).toHaveBeenCalledWith(file))
    // Refetch after import (once on mount + once after import).
    await waitFor(() => expect((listSlidesTemplates as ReturnType<typeof vi.fn>).mock.calls.length).toBeGreaterThanOrEqual(2))
  })
})
