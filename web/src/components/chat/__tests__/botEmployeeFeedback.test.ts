import { expect, it } from 'vitest'
import { botFailureMessage, botLocalFailure, BotFailureKind } from '../botEmployeeFeedback'

it.each([undefined, new Error('GET /api/private: secret stack'), '<html>HTTP 500</html>', { status: 500, body: 'secret' }])('never repeats diagnostics or infers execution failure', error => {
  const failure = botLocalFailure(error)
  expect(failure).toMatchObject({ source: 'local', outcome: 'unknown', kind: BotFailureKind.Unavailable })
  expect(failure.message).toContain('may still be running')
  expect(failure.message).not.toMatch(/secret|HTTP|failed|\/api/)
  expect(botFailureMessage(error)).toMatchObject({ type: 'agent', _streaming: false })
})
it.each([401, 403])('explains access denial %s without a fabricated result', status => {
  expect(botLocalFailure({ status, message: 'secret' }).message).toContain('can’t access')
})
it.each([409, 412])('requires current review after conflict %s', status => {
  expect(botLocalFailure({ status }).kind).toBe(BotFailureKind.Changed)
})
