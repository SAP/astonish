import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import McpEnvEditor from '../McpEnvEditor'

describe('McpEnvEditor', () => {
  it('adds a draft env row when Add variable is clicked', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<McpEnvEditor env={{}} onChange={onChange} />)

    await user.click(screen.getByRole('button', { name: /add variable/i }))

    expect(screen.getByPlaceholderText(/GITHUB_TOKEN/i)).toBeInTheDocument()
    // Empty draft keys are not pushed to parent until named.
    expect(onChange).not.toHaveBeenCalled()
  })

  it('commits env map once a key is entered', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<McpEnvEditor env={{}} onChange={onChange} />)

    await user.click(screen.getByRole('button', { name: /add variable/i }))
    const keyInput = screen.getByPlaceholderText(/GITHUB_TOKEN/i)
    await user.type(keyInput, 'API_KEY')

    expect(onChange).toHaveBeenCalled()
    const last = onChange.mock.calls.at(-1)?.[0]
    expect(last).toEqual({ API_KEY: '' })
  })
})
