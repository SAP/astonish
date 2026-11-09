import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { reviewAdaptiveJob, type BotAdaptiveReview } from '@/api/botAdaptive'
import type { BotScope } from '@/api/bots'

export default function BotAdaptiveConversion({ sourceId, scope, value, onSourceChange, onReview, reviewOnly = false }: {
  sourceId: string; scope: BotScope; value: BotAdaptiveReview | null
  onSourceChange: (id: string) => void; onReview: (review: BotAdaptiveReview | null) => void; reviewOnly?: boolean
}) {
  const [loading, setLoading] = useState(false), [error, setError] = useState('')
  const lookup = useRef<AbortController | null>(null)
  useEffect(() => { setLoading(false); setError(''); return () => lookup.current?.abort() }, [sourceId, scope])
  const load = async () => {
    lookup.current?.abort()
    const controller = new AbortController(); lookup.current = controller
    onReview(null); setLoading(true); setError('')
    try {
      const review = await reviewAdaptiveJob(sourceId.trim(), scope, controller.signal)
      if (!controller.signal.aborted) {
        if (review.source_job_id !== sourceId.trim() || !review.updated_at || !review.schedule?.cron || !review.instructions?.trim()) throw new Error('Source is not eligible for adaptive conversion.')
        onReview(review)
      }
    } catch (e) { if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e)) }
    finally { if (!controller.signal.aborted) setLoading(false) }
  }
  return <section aria-label="Adaptive schedule conversion" className="space-y-2 rounded-md border p-3">
    <h3 className="font-semibold">Convert an adaptive schedule</h3>
    {!reviewOnly && <>
      <label>Existing adaptive job ID<Input aria-label="Adaptive job ID" value={sourceId} onChange={e => { lookup.current?.abort(); onReview(null); onSourceChange(e.target.value) }} /></label>
      <Button variant="outline" disabled={!sourceId.trim() || loading} onClick={() => void load()}>Fetch source preview</Button>
      {!value && !loading && !error && <p>Enter an existing job ID and fetch its authorized preview before reviewing conversion.</p>}
    </>}
    {loading && <p role="status">Loading source preview…</p>}
    {error && <p role="alert">Source was not changed. {error}</p>}
    {value && <section aria-label="Source schedule preview" className="whitespace-pre-wrap break-words">
      <p>Source job: {value.source_job_id}</p>
      <p>Reviewed revision: {value.updated_at}</p>
      <p>Schedule: {value.schedule.cron} · {value.schedule.timezone || 'UTC'}</p>
      <p>{value.instructions}</p>
      <p>Replacement schedule: disabled. Only this schedule and its instructions are copied; credentials, delivery settings, parameters and execution history stay behind.</p>
    </section>}
    <p>The original job is retained, not deleted. Its schedule is paused only when conversion commits. If the transaction fails, rollback retains the original unchanged.</p>
    <p>The new Bot remains a supervised draft. Enabling its schedule and activating the Bot require separate review and consent after creation.</p>
  </section>
}
