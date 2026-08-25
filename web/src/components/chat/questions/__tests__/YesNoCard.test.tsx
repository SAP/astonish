import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'

import YesNoCard from '../YesNoCard'

describe('YesNoCard', () => {
  afterEach(() => {
    cleanup()
  })

  it('calls onAnswer(true) when Yes is clicked', () => {
    const onAnswer = vi.fn()
    render(<YesNoCard prompt="Continue?" onAnswer={onAnswer} />)

    fireEvent.click(screen.getByRole('button', { name: 'Yes' }))

    expect(onAnswer).toHaveBeenCalledTimes(1)
    expect(onAnswer).toHaveBeenCalledWith(true)
  })

  it('calls onAnswer(false) when No is clicked', () => {
    const onAnswer = vi.fn()
    render(<YesNoCard prompt="Continue?" onAnswer={onAnswer} />)

    fireEvent.click(screen.getByRole('button', { name: 'No' }))

    expect(onAnswer).toHaveBeenCalledTimes(1)
    expect(onAnswer).toHaveBeenCalledWith(false)
  })

  it('does not fire callbacks when disabled', () => {
    const onAnswer = vi.fn()
    render(<YesNoCard prompt="Continue?" disabled onAnswer={onAnswer} />)

    const yes = screen.getByRole('button', { name: 'Yes' })
    const no = screen.getByRole('button', { name: 'No' })

    expect(yes).toBeDisabled()
    expect(no).toBeDisabled()

    fireEvent.click(yes)
    fireEvent.click(no)

    expect(onAnswer).not.toHaveBeenCalled()
  })
})
