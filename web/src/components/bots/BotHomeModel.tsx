import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { fetchSessionModelStatus, type SessionModelStatus } from '@/api/studioChat'
import SessionModelPicker from '../chat/SessionModelPicker'

/** Uses only the retained home session, never a selected run or generic chat execution. */
export default function BotHomeModel({ sessionId }: { sessionId: string }) {
  const [status, setStatus] = useState<SessionModelStatus>()
  const [error, setError] = useState(''), [retry, setRetry] = useState(0)
  useEffect(() => {
    let alive = true
    setError(''); setStatus(undefined)
    fetchSessionModelStatus(sessionId).then(value => { if (alive) setStatus(value) }).catch(e => { if (alive) setError((e as Error).message) })
    return () => { alive = false }
  }, [sessionId, retry])
  return <section aria-label="Retained home model" className="space-y-2">
    <p className="text-sm">Retained home prepared. Choose and save a model pin for this Bot before Test once or activation. Model selection does not start execution.</p>
    {status ? <SessionModelPicker sessionId={sessionId} modelStatus={status} onUpdate={setStatus} /> : !error && <p role="status">Loading retained home model…</p>}
    {error && <p role="alert">Could not load retained home model: {error} <Button variant="outline" onClick={() => setRetry(v => v + 1)}>Retry home model</Button></p>}
  </section>
}
