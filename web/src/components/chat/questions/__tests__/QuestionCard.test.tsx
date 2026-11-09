import { fireEvent, render, screen, waitFor } from '@testing-library/react'
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
