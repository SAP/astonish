/**
 * A chat_question is answered once the user has replied. Live clicks set
 * `answered` immediately so the picker unmounts before the user bubble is
 * appended; reload infers the same from any later `user` message (the answer
 * is a normal chat turn — there is no dedicated endpoint).
 */
export function isChatQuestionAnswered(
  messages: ReadonlyArray<{ type: string; answered?: boolean }>,
  index: number,
): boolean {
  const msg = messages[index]
  if (!msg || msg.type !== 'chat_question') return false
  if (msg.answered) return true
  for (let i = index + 1; i < messages.length; i++) {
    if (messages[i].type === 'user') return true
  }
  return false
}
