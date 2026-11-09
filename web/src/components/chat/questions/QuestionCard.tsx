import { useRef, useState } from 'react'
import type { Question, QuestionAnswer } from '@/api/questions'
import YesNoCard from './YesNoCard'
import SingleSelectCard from './SingleSelectCard'
import TextAnswerInput from './TextAnswerInput'
import QuestionOptionThumb from './QuestionOptionThumb'

export interface QuestionCardProps {
  question: Question
  disabled?: boolean
  answered?: boolean
  onAnswer: (answer: QuestionAnswer) => void | Promise<void>
}
// Transport-free: the owner decides how a user answer is delivered. An answer
// is never an execution approval. Key this component by question identity.
export default function QuestionCard({ question, disabled = false, answered = false, onAnswer }: QuestionCardProps) {
  const lock = useRef(false)
  const [pending, setPending] = useState(false)
  const [sent, setSent] = useState(false)
  const [error, setError] = useState('')
  const inactive = disabled || answered || pending || sent
  const submit = async (answer: string, label = answer) => {
    if (inactive || lock.current) return
    lock.current = true
    setPending(true)
    setError('')
    try { await onAnswer({ answer, label }); setSent(true) }
    catch (err) { setError(err instanceof Error ? err.message : 'Could not submit answer. Try again.') }
    finally { lock.current = false; setPending(false) }
  }
  return <div className="flex flex-col gap-3">
    {question.kind === 'yesno' && <YesNoCard prompt={question.prompt} disabled={inactive} onAnswer={yes => { void submit(yes ? 'yes' : 'no', yes ? 'Yes' : 'No') }} />}
    {question.kind === 'select' && <>
      <SingleSelectCard prompt={question.prompt} disabled={inactive} options={(question.options ?? []).map(option => ({ ...option, thumbnail: option.thumbnail ? <QuestionOptionThumb thumbnail={option.thumbnail} label={option.label} /> : undefined }))} onSelect={(id, label) => { void submit(id, label) }} />
      {question.allow_custom_answer && <TextAnswerInput prompt="Custom answer" disabled={inactive} onAnswer={answer => { void submit(answer) }} />}
    </>}
    {question.kind === 'text' && <div className="rounded-lg border border-border bg-card p-4 text-card-foreground"><p className="mb-3 text-sm font-medium">{question.prompt}</p><TextAnswerInput prompt={question.prompt} disabled={inactive} onAnswer={answer => { void submit(answer) }} /></div>}
    {error && <p role="alert">{error}</p>}
    {pending && <p role="status">Submitting answer…</p>}
    {(answered || sent) && <p role="status">Answered</p>}
  </div>
}
