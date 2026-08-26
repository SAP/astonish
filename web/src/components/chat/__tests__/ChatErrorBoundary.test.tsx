import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import ChatErrorBoundary from '../ChatErrorBoundary'

function Boom(): never {
  throw new Error('boom')
}

describe('ChatErrorBoundary', () => {
  it('renders children when they do not throw', () => {
    render(
      <ChatErrorBoundary>
        <span>safe child</span>
      </ChatErrorBoundary>
    )
    expect(screen.getByText('safe child')).toBeInTheDocument()
  })

  it('renders the default fallback when a child throws', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    render(
      <ChatErrorBoundary>
        <Boom />
      </ChatErrorBoundary>
    )
    expect(screen.getByText('This message could not be displayed.')).toBeInTheDocument()
    spy.mockRestore()
  })

  it('renders a custom fallback when provided', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    render(
      <ChatErrorBoundary fallback={<span>custom fallback</span>}>
        <Boom />
      </ChatErrorBoundary>
    )
    expect(screen.getByText('custom fallback')).toBeInTheDocument()
    spy.mockRestore()
  })
})
