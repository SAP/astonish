import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { MAX_ANSWER_LENGTH, validTextAnswer } from '@/api/questions'

export interface TextAnswerInputProps {
  prompt: string
  disabled?: boolean
  onAnswer: (answer: string) => void
}
export default function TextAnswerInput({ prompt, disabled = false, onAnswer }: TextAnswerInputProps) {
  const [value, setValue] = useState('')
  const count = Array.from(value).length
  return <form className="flex flex-col gap-2" onSubmit={event => {
    event.preventDefault()
    if (!disabled && validTextAnswer(value)) onAnswer(value)
  }}>
    <Textarea aria-label={prompt} value={value} disabled={disabled} onChange={event => setValue(event.target.value)} />
    <span className="text-xs text-muted-foreground">{count}/{MAX_ANSWER_LENGTH}</span>
    {count > MAX_ANSWER_LENGTH && <p role="alert">Answer must be at most {MAX_ANSWER_LENGTH} characters.</p>}
    <Button type="submit" disabled={disabled || !validTextAnswer(value)}>Submit answer</Button>
  </form>
}
