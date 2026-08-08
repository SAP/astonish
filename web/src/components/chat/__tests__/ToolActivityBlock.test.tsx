import { describe, it, expect } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import ToolActivityBlock from '../ToolActivityBlock'
import type { ToolActivityStep } from '../toolActivity'

const completeSteps: ToolActivityStep[] = [
  {
    toolName: 'search_tools',
    args: { query: 'http' },
    result: { tools: ['http_request'] },
    status: 'complete',
    callIndex: 0,
    resultIndex: 1,
  },
  {
    toolName: 'http_request',
    args: { url: 'https://example.com' },
    result: { status: 200 },
    status: 'complete',
    callIndex: 2,
    resultIndex: 3,
  },
]

describe('ToolActivityBlock', () => {
  it('renders a two-tone collapsed summary with step badge', () => {
    render(
      <ToolActivityBlock
        blockId="activity-0"
        steps={completeSteps}
      />,
    )
    expect(screen.getByTestId('tool-activity-block')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /1 search, 1 request/ })).toBeInTheDocument()
    const badge = screen.getByTestId('activity-badge')
    expect(badge).toHaveTextContent('2')
    // Solid brand text (not translucent --accent) so light + dark stay readable.
    expect(badge).toHaveStyle({ color: 'var(--brand)' })
    expect(screen.queryByText('Arguments')).not.toBeInTheDocument()
  })

  it('expands to show step rows and nested payloads', () => {
    render(
      <ToolActivityBlock
        blockId="activity-0"
        steps={completeSteps}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: /1 search, 1 request/ }))
    expect(screen.getByText('search_tools')).toBeInTheDocument()
    expect(screen.getByText('http_request')).toBeInTheDocument()

    fireEvent.click(screen.getByText('http_request'))
    expect(screen.getByText('Arguments')).toBeInTheDocument()
    expect(screen.getByText('Result')).toBeInTheDocument()
    expect(screen.getByText(/example\.com/)).toBeInTheDocument()
  })

  it('shows a live running hint while streaming', () => {
    render(
      <ToolActivityBlock
        blockId="activity-1"
        streaming
        steps={[
          {
            toolName: 'http_request',
            args: { url: 'https://example.com' },
            status: 'running',
            callIndex: 0,
          },
        ]}
      />,
    )
    expect(screen.getByRole('button', { name: /Fetching https:\/\/example\.com with http_request/ })).toBeInTheDocument()
  })

  it('surfaces failed tools and keeps expand affordance', () => {
    render(
      <ToolActivityBlock
        blockId="activity-2"
        steps={[
          {
            toolName: 'http_request',
            args: {},
            result: { error: 'timeout' },
            status: 'error',
            callIndex: 0,
            resultIndex: 1,
          },
        ]}
      />,
    )
    const header = screen.getByRole('button', { name: /1 request · http_request failed/ })
    expect(header).toBeInTheDocument()
    expect(screen.getByTestId('activity-badge')).toHaveTextContent('1')
  })

  it('shows green/red diff stats for edit_file content', () => {
    render(
      <ToolActivityBlock
        blockId="activity-3"
        steps={[
          {
            toolName: 'edit_file',
            status: 'complete',
            callIndex: 0,
            args: {
              path: 'a.ts',
              old_string: 'a\nb',
              new_string: 'a\nb\nc\nd',
            },
          },
        ]}
      />,
    )
    expect(screen.getByRole('button', { name: /Edited 1 file/ })).toBeInTheDocument()
    const diff = screen.getByTestId('activity-diff')
    // Only the two appended lines changed; a/b are unchanged (line-level diff).
    expect(diff).toHaveTextContent('+2')
    expect(diff).not.toHaveTextContent('-2')
  })

  it('keeps badge beside the summary and hides the chevron until hover', () => {
    render(
      <ToolActivityBlock
        blockId="activity-0"
        steps={completeSteps}
      />,
    )
    const header = screen.getByRole('button', { name: /1 search, 1 request/ })
    expect(header).toHaveClass('inline-flex')
    expect(screen.getByTestId('activity-badge')).toHaveTextContent('2')
    const chevron = screen.getByTestId('activity-chevron')
    expect(chevron).toHaveClass('opacity-0')
    expect(chevron).toHaveClass('group-hover:opacity-100')
  })

  it('shows absorbed notes when expanded', () => {
    render(
      <ToolActivityBlock
        blockId="activity-notes"
        steps={completeSteps}
        notes={[{ index: 1, kind: 'agent', text: 'checking credentials next' }]}
      />,
    )
    expect(screen.getByRole('button', { name: /1 note/ })).toBeInTheDocument()
    // Always-visible dimmed process line (not only after expand)
    expect(screen.getByTestId('activity-process-text')).toHaveTextContent('checking credentials next')
    fireEvent.click(screen.getByRole('button', { name: /1 note/ }))
    expect(screen.getByTestId('activity-notes')).toBeInTheDocument()
    expect(screen.getAllByText('checking credentials next').length).toBeGreaterThanOrEqual(1)
  })

  it('shows process text without expanding', () => {
    render(
      <ToolActivityBlock
        blockId="activity-process"
        steps={completeSteps}
        notes={[
          { index: 1, kind: 'agent', text: 'first thought' },
          { index: 3, kind: 'agent', text: 'latest thought' },
        ]}
      />,
    )
    expect(screen.getByTestId('activity-process-text')).toHaveTextContent('latest thought')
    expect(screen.queryByTestId('activity-notes')).not.toBeInTheDocument()
  })
})
