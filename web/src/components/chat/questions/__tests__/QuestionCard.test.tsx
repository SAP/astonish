import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import QuestionCard from '../QuestionCard'
import { validTextAnswer } from '@/api/questions'

describe('shared question controls', () => {
  it('validates Unicode code points and blank answers', () => {
    expect(validTextAnswer('🙂'.repeat(4096))).toBe(true)
    expect(validTextAnswer('🙂'.repeat(4097))).toBe(false)
    expect(validTextAnswer(' \n')).toBe(false)
  })
  it('submits text once and never as permission', async () => {
    const answer = vi.fn()
    render(<QuestionCard question={{ kind: 'text', prompt: 'Details?' }} onAnswer={answer} />)
    expect(screen.getByRole('button', { name: 'Submit answer' })).toBeDisabled()
    expect(screen.getByRole('textbox', { name: 'Details?' })).not.toHaveAttribute('placeholder')
    fireEvent.change(screen.getByRole('textbox'), { target: { value: '🙂 Details' } })
    fireEvent.click(screen.getByRole('button', { name: 'Submit answer' }))
    await waitFor(() => expect(answer).toHaveBeenCalledWith({ answer: '🙂 Details', label: '🙂 Details' }))
    await screen.findByText('Answered')
    expect(screen.getByRole('textbox')).toBeDisabled()
  })
  it('retries failed custom answers and preserves closed selects', async () => {
    const answer = vi.fn().mockRejectedValueOnce(new Error('Offline')).mockResolvedValue(undefined)
    const { rerender } = render(<QuestionCard question={{ kind: 'select', prompt: 'Pick', options: [{ id: 'x', label: 'X' }] }} onAnswer={answer} />)
    expect(screen.queryByRole('textbox')).toBeNull()
    rerender(<QuestionCard question={{ kind: 'select', prompt: 'Pick', options: [{ id: 'x', label: 'X' }], allow_custom_answer: true }} onAnswer={answer} />)
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Other' } })
    fireEvent.click(screen.getByRole('button', { name: 'Submit answer' }))
    await screen.findByText('Offline')
    fireEvent.click(screen.getByRole('button', { name: 'Submit answer' }))
    await screen.findByText('Answered')
    expect(answer).toHaveBeenCalledTimes(2)
  })
})

const titleOptions = [
  { id: 'compare', label: 'Compare current weather forecasts for Boston' },
  { id: 'explain', label: 'Explain Boston seasonal weather patterns' },
  { id: 'plan', label: 'Plan outdoor activities around Boston weather' },
  { id: 'pack', label: 'Suggest what to pack for a Boston trip' },
]

describe('initial-title shared question controls', () => {
  it.each([3, 4])('shows %i interpretations plus custom input and locks an in-flight option', async count => {
    let finish!: () => void
    const answer = vi.fn(() => new Promise<void>(resolve => { finish = resolve }))
    render(<QuestionCard question={{ questionId: 'title-q', kind: 'select', prompt: 'What should I do about Boston weather?', options: titleOptions.slice(0, count), allow_custom_answer: true }} onAnswer={answer} />)
    expect(screen.getAllByRole('radio')).toHaveLength(count)
    const custom = screen.getByRole('textbox', { name: 'Custom answer' })
    expect(custom).toHaveAttribute('placeholder', 'Something else…')
    fireEvent.change(custom, { target: { value: ' \n ' } })
    expect(screen.getByRole('button', { name: 'Submit answer' })).toBeDisabled()
    const option = screen.getByRole('radio', { name: titleOptions[1].label })
    fireEvent.click(option)
    fireEvent.click(option)
    expect(answer).toHaveBeenCalledExactlyOnceWith({ answer: 'explain', label: titleOptions[1].label })
    expect(screen.getByRole('status')).toHaveTextContent('Submitting answer…')
    screen.getAllByRole('radio').forEach(control => expect(control).toHaveAttribute('aria-disabled', 'true'))
    expect(custom).toBeDisabled()
    await act(async () => { finish() })
    expect(screen.getByRole('status')).toHaveTextContent('Answered')
    screen.getAllByRole('radio').forEach(control => expect(control).toHaveAttribute('aria-disabled', 'true'))
  })

  it('keeps initial-title custom text on error and retries the identical answer', async () => {
    const answer = vi.fn().mockRejectedValueOnce(new Error('Offline')).mockResolvedValue(undefined)
    render(<QuestionCard question={{ kind: 'select', prompt: 'What should I do about Boston weather?', options: titleOptions, allow_custom_answer: true }} onAnswer={answer} />)
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Compare rain forecasts for my weekend hike 🙂' } })
    fireEvent.click(screen.getByRole('button', { name: 'Submit answer' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Offline')
    expect(screen.getByRole('textbox')).toHaveValue('Compare rain forecasts for my weekend hike 🙂')
    expect(screen.getAllByRole('radio')).toHaveLength(4)
    fireEvent.click(screen.getByRole('button', { name: 'Submit answer' }))
    await screen.findByText('Answered')
    expect(answer).toHaveBeenCalledTimes(2)
    expect(answer.mock.calls[0]).toEqual(answer.mock.calls[1])
  })
})
