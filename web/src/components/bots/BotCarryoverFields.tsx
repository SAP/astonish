import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import type { BotCarryover } from '@/api/bots'

export default function BotCarryoverFields({ value, onChange, review = false }: { value: BotCarryover; onChange: (value: BotCarryover) => void; review?: boolean }) {
  if (review) return <section aria-label="Reviewed carryover" className="space-y-2 whitespace-pre-wrap break-words">
    <h3 className="font-semibold">Selected carryover</h3><p>{value.summary || 'No summary selected.'}</p>
    {value.memories.map((m, i) => <article key={i}><p>{m.content}</p><p className="text-sm text-muted-foreground">Memory · {m.category}</p></article>)}
    {value.goals.map((g, i) => <article key={i}><h4>{g.title}</h4><p>{g.completion_criteria}</p></article>)}
    {value.artifacts.map((a, i) => <article key={i}><h4>{a.title}</h4><p>{a.content}</p></article>)}
    {!value.memories.length && !value.goals.length && !value.artifacts.length && <p>No memories, goals or text snapshots selected.</p>}
  </section>
  return <section aria-label="Select carryover" className="space-y-3">
    <p className="text-sm text-muted-foreground">Enter only text you selected and redacted. Nothing is fetched from the transcript. Snapshots are text, not file paths or binary files.</p>
    <label>Reviewed carryover summary<Textarea aria-label="Carryover summary" value={value.summary} onChange={e => onChange({ ...value, summary: e.target.value })} /></label>
    {value.memories.map((m, i) => <fieldset key={i} className="space-y-2 border border-border p-2"><legend>Memory {i + 1}</legend>
      <Textarea aria-label={`Memory content ${i + 1}`} value={m.content} onChange={e => onChange({ ...value, memories: value.memories.map((row, n) => n === i ? { ...row, content: e.target.value } : row) })} />
      <Input aria-label={`Memory category ${i + 1}`} value={m.category} onChange={e => onChange({ ...value, memories: value.memories.map((row, n) => n === i ? { ...row, category: e.target.value } : row) })} />
      <Button variant="ghost" onClick={() => onChange({ ...value, memories: value.memories.filter((_, n) => n !== i) })}>Remove memory {i + 1}</Button>
    </fieldset>)}
    <Button variant="outline" onClick={() => onChange({ ...value, memories: [...value.memories, { content: '', category: 'fact' }] })}>Add memory</Button>
    {value.goals.map((g, i) => <fieldset key={i} className="space-y-2 border border-border p-2"><legend>Goal {i + 1}</legend>
      <Input aria-label={`Goal title ${i + 1}`} value={g.title} onChange={e => onChange({ ...value, goals: value.goals.map((row, n) => n === i ? { ...row, title: e.target.value } : row) })} />
      <Textarea aria-label={`Goal criteria ${i + 1}`} value={g.completion_criteria} onChange={e => onChange({ ...value, goals: value.goals.map((row, n) => n === i ? { ...row, completion_criteria: e.target.value } : row) })} />
      <Button variant="ghost" onClick={() => onChange({ ...value, goals: value.goals.filter((_, n) => n !== i) })}>Remove goal {i + 1}</Button>
    </fieldset>)}
    <Button variant="outline" onClick={() => onChange({ ...value, goals: [...value.goals, { title: '', completion_criteria: '' }] })}>Add goal</Button>
    {value.artifacts.map((a, i) => <fieldset key={i} className="space-y-2 border border-border p-2"><legend>Text snapshot {i + 1}</legend>
      <Input aria-label={`Snapshot title ${i + 1}`} value={a.title} onChange={e => onChange({ ...value, artifacts: value.artifacts.map((row, n) => n === i ? { ...row, title: e.target.value } : row) })} />
      <Textarea aria-label={`Snapshot content ${i + 1}`} value={a.content} onChange={e => onChange({ ...value, artifacts: value.artifacts.map((row, n) => n === i ? { ...row, content: e.target.value } : row) })} />
      <Button variant="ghost" onClick={() => onChange({ ...value, artifacts: value.artifacts.filter((_, n) => n !== i) })}>Remove snapshot {i + 1}</Button>
    </fieldset>)}
    <Button variant="outline" onClick={() => onChange({ ...value, artifacts: [...value.artifacts, { title: '', content: '' }] })}>Add text snapshot</Button>
  </section>
}
