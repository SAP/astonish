import { describe, expect, it } from 'vitest'

import { isChatQuestionAnswered } from '../chatQuestions'

describe('isChatQuestionAnswered', () => {
  it('is false for an unanswered last question', () => {
    const messages = [
      { type: 'chat_question' },
    ]
    expect(isChatQuestionAnswered(messages, 0)).toBe(false)
  })

  it('is true when the message is already marked answered', () => {
    const messages = [
      { type: 'chat_question', answered: true },
    ]
    expect(isChatQuestionAnswered(messages, 0)).toBe(true)
  })

  it('is true when a later user message exists (reload / typed reply)', () => {
    const messages = [
      { type: 'chat_question' },
      { type: 'user' },
    ]
    expect(isChatQuestionAnswered(messages, 0)).toBe(true)
  })

  it('treats only questions before the latest user reply as answered', () => {
    const messages = [
      { type: 'chat_question' },
      { type: 'user' },
      { type: 'chat_question' },
    ]
    expect(isChatQuestionAnswered(messages, 0)).toBe(true)
    expect(isChatQuestionAnswered(messages, 2)).toBe(false)
  })

  it('is false for non-question indices', () => {
    const messages = [
      { type: 'user' },
      { type: 'chat_question' },
    ]
    expect(isChatQuestionAnswered(messages, 0)).toBe(false)
  })
})
