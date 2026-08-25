import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup, act } from '@testing-library/react'

import SingleSelectCard from '../SingleSelectCard'

const options = [
  { id: 'a', label: 'Option A', description: 'First option' },
  { id: 'b', label: 'Option B' },
  { id: 'c', label: 'Option C' },
]

describe('SingleSelectCard', () => {
  afterEach(() => {
    cleanup()
  })

  it('calls onSelect with the right id and label when a tile is clicked', () => {
    const onSelect = vi.fn()
    render(<SingleSelectCard prompt="Pick one" options={options} onSelect={onSelect} />)

    fireEvent.click(screen.getByText('Option B'))

    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onSelect).toHaveBeenCalledWith('b', 'Option B')
  })

  it('selects a focused tile with the Enter key', () => {
    const onSelect = vi.fn()
    render(<SingleSelectCard prompt="Pick one" options={options} onSelect={onSelect} />)

    const tiles = screen.getAllByRole('radio')
    tiles[2].focus()
    act(() => {
      fireEvent.keyDown(tiles[2], { key: 'Enter' })
    })

    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onSelect).toHaveBeenCalledWith('c', 'Option C')
  })

  it('selects a focused tile with the Space key', () => {
    const onSelect = vi.fn()
    render(<SingleSelectCard prompt="Pick one" options={options} onSelect={onSelect} />)

    const tiles = screen.getAllByRole('radio')
    tiles[0].focus()
    act(() => {
      fireEvent.keyDown(tiles[0], { key: ' ' })
    })

    expect(onSelect).toHaveBeenCalledWith('a', 'Option A')
  })

  it('moves focus with arrow keys', () => {
    const onSelect = vi.fn()
    render(<SingleSelectCard prompt="Pick one" options={options} onSelect={onSelect} />)

    const tiles = screen.getAllByRole('radio')
    tiles[0].focus()
    act(() => {
      fireEvent.keyDown(tiles[0], { key: 'ArrowRight' })
    })

    expect(tiles[1]).toHaveFocus()
  })

  it('does not fire onSelect when disabled', () => {
    const onSelect = vi.fn()
    render(<SingleSelectCard prompt="Pick one" options={options} disabled onSelect={onSelect} />)

    const tiles = screen.getAllByRole('radio')
    fireEvent.click(tiles[0])
    fireEvent.keyDown(tiles[0], { key: 'Enter' })

    expect(onSelect).not.toHaveBeenCalled()
  })
})
