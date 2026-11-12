import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { Button } from '../ui/button'
import { parseDelegationHandoff } from '../../api/delegationHandoff'
import type { SubTaskEvent } from './chatTypes'
import { delegationActivity, selectDelegationActivity } from './delegationActivity'
import DelegationHandoffView from './DelegationHandoffView'
import ToolActivityBlock from './ToolActivityBlock'
import { markdownComponents } from './markdownComponents'

export default function DelegationDetail({ live, persisted, running, loading, error, retry, identity }: {
  live: SubTaskEvent[]; persisted?: SubTaskEvent[]; running: boolean
  loading: boolean; error?: string; retry: () => void; identity: string
}) {
  const [view, setView] = useState<'activity' | 'handoff'>('activity')
  const handoff = parseDelegationHandoff((persisted ?? live).find(e => e.type === 'handoff'))
  const entries = delegationActivity(selectDelegationActivity(live, persisted, running))
  return <section className="space-y-2 text-xs">
    <div className="flex gap-1" aria-label="Delegation detail views">
      <Button size="sm" variant={view === 'activity' ? 'secondary' : 'ghost'} aria-pressed={view === 'activity'} onClick={() => setView('activity')}>Activity</Button>
      <Button size="sm" variant={view === 'handoff' ? 'secondary' : 'ghost'} aria-pressed={view === 'handoff'} onClick={() => setView('handoff')}>Handoff</Button>
    </div>
    {loading && <p role="status">Loading {view === 'handoff' ? 'handoff' : 'execution detail'}...</p>}
    {error && <div role="alert">{error} <Button variant="ghost" size="sm" onClick={retry}>Retry</Button></div>}
    {view === 'handoff' ? (!loading && !error && <DelegationHandoffView handoff={handoff} />) : <>
      {!entries.length && !loading && !error && <p>No execution detail recorded.</p>}
      {entries.some(e => e.kind === 'tool') ? <ToolActivityBlock
        entries={entries} blockId={identity} streaming={running}
        renderMessage={message => <div className="markdown-body text-xs"><ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>{message.content}</ReactMarkdown></div>}
      /> : entries.map(entry => entry.kind === 'message' && <div key={entry.index} className="markdown-body text-xs"><ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>{entry.message.content}</ReactMarkdown></div>)}
    </>}
  </section>
}
