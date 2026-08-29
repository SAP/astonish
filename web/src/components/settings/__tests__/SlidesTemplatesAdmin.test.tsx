import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import * as slidesApi from '@/api/slides'
import SlidesTemplatesAdmin from '../SlidesTemplatesAdmin'

vi.mock('@/components/chat/questions/QuestionOptionThumb', () => ({ default: () => <div /> }))
vi.mock('@/components/chat/questions/ThumbnailFrame', () => ({ default: ({ children }: { children: React.ReactNode }) => <div>{children}</div> }))

const template: slidesApi.SlidesTemplate = {
  name: 'quarterly',
  label: 'Quarterly',
  scope: 'team',
}

describe('SlidesTemplatesAdmin', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.spyOn(slidesApi, 'listSlidesTemplates').mockResolvedValue({ templates: [template] })
    vi.spyOn(slidesApi, 'deleteSlidesTemplate').mockResolvedValue(undefined)
  })

  it('provides an accessible, keyboard-contained delete dialog', async () => {
    const user = userEvent.setup()
    render(<SlidesTemplatesAdmin scope="team" />)

    const trigger = await screen.findByRole('button', { name: /delete/i })
    await user.click(trigger)

    const dialog = screen.getByRole('dialog', { name: 'Delete template?' })
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveFocus()

    await user.tab({ shift: true })
    expect(screen.getByTestId('slides-templates-admin-delete-confirm')).toHaveFocus()
    await user.tab()
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveFocus()

    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await waitFor(() => expect(trigger).toHaveFocus())
  })
})
