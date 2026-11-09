import { useState } from 'react'
export { default as BotMemoryCharterPanel } from './BotMemoryCharterPanel'
export type { BotMemoryCharterPanelProps } from './BotMemoryCharterPanel'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { publishBotTemplate } from '@/api/bots'

export { default as BotGoals } from './BotGoalsPanel'
export { default as BotSchedule } from './BotSchedulePanel'
export function BotTemplatePublication() {
  const [name, setName] = useState(''), [charter, setCharter] = useState(''), [reviewed, setReviewed] = useState(false), [busy, setBusy] = useState(false), [result, setResult] = useState('')
  return <form className="space-y-2 border-t border-border pt-4" onSubmit={async e => { e.preventDefault(); if (!reviewed || busy) return; setBusy(true); try { await publishBotTemplate({ schema_version: 1, name, charter, language: '', suggested_goals: [], suggested_schedules: [] }); setResult('Team template published. No Bot was activated.'); setReviewed(false) } catch (e) { setResult(e instanceof Error ? e.message : String(e)) } finally { setBusy(false) } }}><h3>Publish a redacted team template (team admins)</h3><p className="text-sm text-muted-foreground">Write a separate public-to-team copy. Private charter, memory, history, bindings and credentials are not automatically copied.</p><Input aria-label="Template name" value={name} onChange={e => { setName(e.target.value); setReviewed(false) }} /><Textarea aria-label="Redacted template charter" value={charter} onChange={e => { setCharter(e.target.value); setReviewed(false) }} /><label className="flex gap-2"><input type="checkbox" checked={reviewed} onChange={e => setReviewed(e.target.checked)} />I redacted this copy for the team.</label><Button disabled={busy || !reviewed || !name.trim() || !charter.trim()}>Publish reviewed template</Button>{result && <p role="status">{result}</p>}</form>
}
