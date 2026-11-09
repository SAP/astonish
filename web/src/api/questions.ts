export const QUESTION_KINDS = { yesno: 'yesno', select: 'select', text: 'text' } as const
export type QuestionKind = typeof QUESTION_KINDS[keyof typeof QUESTION_KINDS]
export const MAX_ANSWER_LENGTH = 4096
export interface QuestionOption {
  id: string
  label: string
  description?: string
  thumbnail?: {
    kind: 'image' | 'slides-archetype'
    markup?: string
    assetRef?: string
    theme?: Record<string, string>
    template?: string
    templateScope?: 'builtin' | 'org' | 'personal' | 'platform' | 'team'
  }
}
export interface Question {
  questionId?: string
  kind: QuestionKind
  prompt: string
  options?: QuestionOption[]
  allow_custom_answer?: boolean
}
export interface QuestionAnswer { answer: string; label: string }
export function validTextAnswer(value: string): boolean {
  return value.trim().length > 0 && Array.from(value).length <= MAX_ANSWER_LENGTH
}
