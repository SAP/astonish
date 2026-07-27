import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import KnowledgeBrowser from '../KnowledgeBrowser'
import {
  fetchMemoryMap,
  listPersonalMemories,
  listTeamMemories,
  listOrgMemories,
  previewMemoryConsolidation,
  applyMemoryConsolidation,
} from '../../api/platform'

vi.mock('../../api/platform', () => ({
  searchMemories: vi.fn(),
  listPersonalMemories: vi.fn(),
  listTeamMemories: vi.fn(),
  listOrgMemories: vi.fn(),
  saveTeamMemory: vi.fn(),
  savePersonalMemory: vi.fn(),
  saveOrgMemory: vi.fn(),
  deleteTeamMemory: vi.fn(),
  deleteOrgMemory: vi.fn(),
  deletePersonalMemory: vi.fn(),
  promoteMemoryToOrg: vi.fn(),
  promotePersonalToTeam: vi.fn(),
  updateMemory: vi.fn(),
  fetchMemoryMap: vi.fn(),
  previewMemoryConsolidation: vi.fn(),
  applyMemoryConsolidation: vi.fn(),
}))

describe('KnowledgeBrowser Memory Map', () => {
  const user = { id: 'user-1', email: 'u@example.com', display_name: 'User', role: 'admin' }

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(listPersonalMemories).mockResolvedValue([])
    vi.mocked(listTeamMemories).mockResolvedValue([])
    vi.mocked(listOrgMemories).mockResolvedValue([])
    vi.mocked(fetchMemoryMap).mockResolvedValue({
      stats: {
        total_memories: 2,
        group_count: 1,
        duplicate_risk_count: 1,
        scattered_topic_count: 1,
        transient_risk_count: 0,
        trial_error_risk_count: 1,
      },
      groups: [
        {
          key: 'proxmox-console-access',
          title: 'proxmox-console-access',
          memory_count: 2,
          scopes: ['personal', 'team'],
          categories: ['proxmox'],
          session_ids: ['session-1'],
          created_by: ['user-1'],
          flags: [
            { type: 'duplicate_risk', severity: 'info', description: 'Multiple memories share a likely topic.' },
            { type: 'scattered_topic', severity: 'warning', description: 'Related memories are spread across scopes.' },
            { type: 'trial_error_risk', severity: 'info', description: 'Exploratory failed attempts.' },
          ],
          representative: {
            id: 'm1',
            snippet: 'Use the noVNC ticket endpoint for Proxmox console access.',
            category: 'proxmox',
            scope: 'team',
            session_id: 'session-1',
          },
          memories: [
            {
              id: 'm1',
              snippet: 'Use the noVNC ticket endpoint for Proxmox console access.',
              category: 'proxmox',
              scope: 'team',
              session_id: 'session-1',
            },
            {
              id: 'm2',
              snippet: 'Earlier exploration tried scraping before the efficient noVNC path.',
              category: 'proxmox',
              scope: 'personal',
            },
          ],
        },
      ],
    })
    vi.mocked(previewMemoryConsolidation).mockResolvedValue({
      card: {
        canonical_key: 'proxmox-console-access',
        scope: 'team',
        title: 'Proxmox Console Access',
        recommended_recipe: ['Use the noVNC ticket endpoint for Proxmox console access.'],
        conditions: ['Requires Proxmox console permission.'],
        cautions_or_conditional_failures: ['Treat scraping attempts as historical only.'],
        verification: ['Drafted from existing memories; verify on next successful run.'],
        source_memory_ids: ['m1', 'm2'],
        status: 'draft',
      },
      content: '---\nastonish_memory_type: scenario_card\n---\n',
      sources: ['m1', 'm2'],
    })
    vi.mocked(applyMemoryConsolidation).mockResolvedValue({
      applied: true,
      scope: 'team',
      action: 'created',
      card: {
        canonical_key: 'proxmox-console-access',
        scope: 'team',
        title: 'Proxmox Console Access',
        recommended_recipe: ['Use the noVNC ticket endpoint for Proxmox console access.'],
        status: 'draft',
      },
    })
  })

  it('loads and renders the memory map diagnostics tab', async () => {
    const actor = userEvent.setup()

    render(<KnowledgeBrowser theme="light" user={user} activeTeam="core" />)

    await actor.click(await screen.findByRole('button', { name: /memory map/i }))

    await waitFor(() => {
      expect(fetchMemoryMap).toHaveBeenCalledWith(500, 'core')
    })
    expect(await screen.findByRole('heading', { name: /memory map/i })).toBeInTheDocument()
    expect(screen.getByText('Total memories')).toBeInTheDocument()
    expect(screen.getByText('2')).toBeInTheDocument()
    expect(screen.getByText('Duplicate risk')).toBeInTheDocument()
    expect(screen.getByText('Scattered topic')).toBeInTheDocument()
    expect(screen.getAllByText('Trial/error wording').length).toBeGreaterThan(0)
    expect(screen.getAllByText(/noVNC ticket endpoint/i).length).toBeGreaterThan(0)
  })

  it('drafts, edits, and saves a scenario card from a memory map group', async () => {
    const actor = userEvent.setup()

    render(<KnowledgeBrowser theme="light" user={user} activeTeam="core" />)

    await actor.click(await screen.findByRole('button', { name: /memory map/i }))
    await actor.click(await screen.findByRole('button', { name: /draft card/i }))

    await waitFor(() => {
      expect(previewMemoryConsolidation).toHaveBeenCalledWith(
        'proxmox-console-access',
        'team',
        ['m1', 'm2'],
        'core',
      )
    })
    expect(await screen.findByText(/consolidated scenario card preview/i)).toBeInTheDocument()

    const title = screen.getByLabelText(/title/i)
    await actor.clear(title)
    await actor.type(title, 'Fast Proxmox Console Access')

    await actor.click(screen.getByRole('button', { name: /save scenario card/i }))

    await waitFor(() => {
      expect(applyMemoryConsolidation).toHaveBeenCalledWith(
        expect.objectContaining({
          canonical_key: 'proxmox-console-access',
          scope: 'team',
          title: 'Fast Proxmox Console Access',
          recommended_recipe: ['Use the noVNC ticket endpoint for Proxmox console access.'],
        }),
        'team',
        'core',
      )
    })
    expect(await screen.findByText(/scenario card created/i)).toBeInTheDocument()
  })
})
