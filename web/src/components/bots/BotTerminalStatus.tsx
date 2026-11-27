import { botNeutralTerminalLabel, type BotRunStates } from '@/api/botRunState'

// Product state only: no assistant avatar, markdown, raw report or retry action.
export default function BotTerminalStatus({ states }: { states: BotRunStates }) {
  const outcomes = Object.values(states).filter(state => botNeutralTerminalLabel(state))
  if (!outcomes.length) return null
  return <div role="status" aria-live="polite" className="text-sm text-muted-foreground">
    {outcomes.map(state => <div key={state.run_id} data-bot-terminal-run={state.run_id}>{botNeutralTerminalLabel(state)}</div>)}
  </div>
}
