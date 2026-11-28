import { botRunStatusLabel, type BotRunState } from '@/api/botRunState'
import { validBotProgress } from '@/api/botProgress'

/** Shared read projection; no clocks, resumptions, or trace subscriptions. */
export default function BotProgressDetails({ state }: { state: BotRunState }) {
  if (!validBotProgress(state.progress, state)) return null
  const progress = state.progress
  return <section aria-label={`Progress for run ${state.run_id}`} className="space-y-1 rounded border border-border p-3 text-sm">
    <h3>{botRunStatusLabel(state)}</h3>
    {progress.summary && <p className="break-words">{progress.summary.slice(0, 500)}</p>}
    <p>Checkpoint {progress.revision}{progress.evaluated_at ? ` · checked ${progress.evaluated_at}` : ''}</p>
    <p>Cumulative active time: {Math.floor(progress.active_duration_nanos / 1e9)} seconds</p>
    {progress.coalesced_occurrences !== undefined && <p>Coalesced schedule ticks: {progress.coalesced_occurrences}</p>}
    {progress.next_eligible_at && <p>Next eligible: {progress.next_eligible_at}</p>}
    <p className="text-muted-foreground">Progress is not a final result. Resource usage appears with the run; configured limits and current authorization still apply.</p>
  </section>
}
