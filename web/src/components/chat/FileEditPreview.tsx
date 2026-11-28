import type { ToolActivityStep } from './toolActivity'

/** Shared main-chat/delegation edit preview. Actual tool evidence wins over requested replacements. */
export default function FileEditPreview({ step }: { step: ToolActivityStep }) {
  if (!['edit_file', 'write_file'].includes(step.toolName) || step.status === 'error') return null
  const result = step.result && typeof step.result === 'object' ? step.result as Record<string, unknown> : undefined
  const args = step.args && typeof step.args === 'object' ? step.args as Record<string, unknown> : undefined
  const actual = typeof result?.diff === 'string' ? result.diff : typeof result?.verification_context === 'string' ? result.verification_context : undefined
  const requested = typeof args?.old_string === 'string' && typeof args?.new_string === 'string'
    ? [...args.old_string.split('\n').map(l => `-${l}`), ...args.new_string.split('\n').map(l => `+${l}`)].join('\n')
    : typeof args?.content === 'string' ? args.content.split('\n').map(l => `+${l}`).join('\n') : undefined
  const diff = actual ?? requested
  if (!diff) return null
  const path = typeof result?.path === 'string' ? result.path : typeof args?.path === 'string' ? args.path : ''
  return <figure className="rounded border border-border overflow-hidden" data-testid="file-edit-preview">
    <figcaption className="p-2 text-xs text-muted-foreground">{path} — {actual !== undefined ? 'Recorded edit' : 'Requested edit (not verified)'}</figcaption>
    <pre className="text-xs overflow-auto max-h-80 p-2 bg-background" aria-label="File edit diff">{diff.split('\n').map((line, index) => <span key={index} className="block whitespace-pre" style={{ color: line.startsWith('+') ? 'var(--success)' : line.startsWith('-') ? 'var(--danger)' : undefined }}>{line || ' '}</span>)}</pre>
  </figure>
}
