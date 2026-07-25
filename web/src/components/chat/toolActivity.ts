import type { ChatMsg, ToolCallMessage, ToolResultMessage } from './chatTypes'

export type ToolStepStatus = 'running' | 'complete' | 'error'

export interface ToolActivityStep {
  toolName: string
  args?: unknown
  result?: unknown
  status: ToolStepStatus
  callIndex: number
  resultIndex?: number
}

export interface ActivityNote {
  index: number
  kind: 'agent' | 'thinking' | 'auto_approved' | 'retry'
  text: string
}

export type MessageSegment =
  | { kind: 'passthrough'; index: number }
  | {
      kind: 'activity'
      start: number
      end: number
      steps: ToolActivityStep[]
      notes: ActivityNote[]
      /** Indices rendered by this block (tools + absorbed soft notes). */
      coveredIndices: number[]
    }

export type ActivitySummaryVariant = 'running' | 'complete' | 'error'

export interface ActivitySummary {
  text: string
  variant: ActivitySummaryVariant
  /** Accent-colored lead (verb or first clause). */
  lead: string
  /** Muted remainder of the categorized phrase. */
  rest: string
  /** Error fragment including leading " · ", rendered in danger color. */
  errorSuffix?: string
}

export type ActivityStats =
  | { kind: 'diff'; added: number; removed: number }
  | { kind: 'badge'; count: number }

function isToolMessage(msg: ChatMsg): msg is ToolCallMessage | ToolResultMessage {
  return msg.type === 'tool_call' || msg.type === 'tool_result'
}

function toolNameOf(msg: ToolCallMessage | ToolResultMessage): string {
  const name = msg.toolName
  if (typeof name === 'string' && name) return name
  return 'unknown'
}

/** Detect common error shapes in tool results. */
export function isToolResultError(result: unknown): boolean {
  if (result == null) return false
  if (typeof result === 'string') {
    const head = result.slice(0, 300).toLowerCase()
    return (
      head.startsWith('error:') ||
      head.startsWith('error ') ||
      head.includes('\nerror:') ||
      /^failed\b/i.test(result.slice(0, 80))
    )
  }
  if (typeof result === 'object') {
    const r = result as Record<string, unknown>
    if (r.success === false) return true
    if (r.ok === false) return true
    if (r.status === 'error' || r.status === 'failed') return true
    if (typeof r.error === 'string' && r.error.trim() !== '') return true
    if (r.error && typeof r.error === 'object') return true
  }
  return false
}

/** One-line preview of args/result for collapsed step rows. */
export function previewValue(data: unknown, maxLen = 80): string {
  if (data == null) return ''
  let text: string
  if (typeof data === 'string') {
    text = data
  } else {
    try {
      text = JSON.stringify(data)
    } catch {
      text = String(data)
    }
  }
  text = text.replace(/\s+/g, ' ').trim()
  if (text.length <= maxLen) return text
  return text.slice(0, maxLen - 1) + '…'
}

function formatToolResult(data: unknown): string {
  if (typeof data === 'string') return data
  try {
    return JSON.stringify(data, null, 2)
  } catch {
    return String(data)
  }
}

/** Full pretty-printed payload for expanded step detail. */
export function formatToolPayload(data: unknown): string {
  return formatToolResult(data)
}

function pairSteps(messages: ChatMsg[], start: number, end: number): ToolActivityStep[] {
  const steps: ToolActivityStep[] = []
  // FIFO queues of step indices awaiting a result, keyed by tool name.
  // Handles parallel call/call/result/result ordering.
  const pendingByName = new Map<string, number[]>()

  for (let i = start; i <= end; i++) {
    const msg = messages[i]
    if (!isToolMessage(msg)) continue

    if (msg.type === 'tool_call') {
      const name = toolNameOf(msg)
      const stepIndex = steps.length
      steps.push({
        toolName: name,
        args: msg.toolArgs,
        status: 'running',
        callIndex: i,
      })
      const queue = pendingByName.get(name) || []
      queue.push(stepIndex)
      pendingByName.set(name, queue)
    } else {
      const name = toolNameOf(msg)
      const result = msg.toolResult
      const queue = pendingByName.get(name)
      const pendingIdx = queue?.shift()
      if (pendingIdx !== undefined) {
        const step = steps[pendingIdx]
        step.result = result
        step.resultIndex = i
        step.status = isToolResultError(result) ? 'error' : 'complete'
      } else {
        steps.push({
          toolName: name,
          result,
          status: isToolResultError(result) ? 'error' : 'complete',
          callIndex: i,
          resultIndex: i,
        })
      }
    }
  }
  return steps
}

/** Soft notes that may be absorbed into an activity fold (allowlist). */
export function isSoftNoteType(type: string): boolean {
  return type === 'agent' || type === 'thinking' || type === 'auto_approved' || type === 'retry'
}

/**
 * Hard breaks always passthrough and split folds.
 * Unknown / future types default to hard break for safety.
 */
export function isHardBreakType(type: string): boolean {
  if (type === 'tool_call' || type === 'tool_result') return false
  if (isSoftNoteType(type)) return false
  return true
}

export function isHardBreak(msg: ChatMsg): boolean {
  return isHardBreakType(msg.type)
}

export function isSoftNote(msg: ChatMsg): boolean {
  return isSoftNoteType(msg.type)
}

/** True when content hosts (or is streaming into) an astonish-app fence. */
export function hasAppFence(content: string): boolean {
  return /```astonish-app\b/.test(content)
}

/**
 * Agent messages with an astonish-app fence must stay passthrough so
 * AppCodeIndicator ("Generating app...") can render during streaming.
 */
export function isAppProgressAgent(msg: ChatMsg): boolean {
  if (msg.type !== 'agent') return false
  const content = typeof msg.content === 'string' ? msg.content : String(msg.content ?? '')
  return hasAppFence(content)
}

function noteTextFromMessage(msg: ChatMsg): string {
  switch (msg.type) {
    case 'agent':
      return typeof msg.content === 'string' ? msg.content : String(msg.content ?? '')
    case 'thinking':
      return typeof msg.content === 'string' ? msg.content : String(msg.content ?? '')
    case 'auto_approved':
      return `Auto-approved ${String(msg.toolName ?? 'tool')}`
    case 'retry':
      return `Retry ${String(msg.attempt ?? '')}/${String(msg.maxRetries ?? '')}${msg.reason ? `: ${String(msg.reason)}` : ''}`
    default:
      return ''
  }
}

function noteKindFromMessage(msg: ChatMsg): ActivityNote['kind'] {
  if (msg.type === 'agent' || msg.type === 'thinking' || msg.type === 'auto_approved' || msg.type === 'retry') {
    return msg.type
  }
  return 'agent'
}

export interface GroupToolActivityOptions {
  /**
   * @deprecated Unused. Agent text is never absorbed into activity notes; a sticky
   * latest-agent bubble per run is selected at render time.
   */
  absorbTrailingSoft?: boolean
}

/** Soft notes that fold into activity process lines (not user-facing bubbles). */
function isAbsorbableProcessNote(msg: ChatMsg): boolean {
  return msg.type === 'thinking' || msg.type === 'auto_approved' || msg.type === 'retry'
}

/**
 * Fold tools + process notes into **one** activity segment per soft+tool run.
 * Agent messages are never absorbed (they use a sticky bubble — see isStickyAgentBubble).
 * Hard-break types always passthrough and split folds.
 */
export function groupToolActivity(
  messages: ChatMsg[],
  _opts: GroupToolActivityOptions = {},
): MessageSegment[] {
  const segments: MessageSegment[] = []
  let i = 0
  while (i < messages.length) {
    const msg = messages[i]
    if (isHardBreak(msg)) {
      segments.push({ kind: 'passthrough', index: i })
      i += 1
      continue
    }

    // Collect contiguous soft notes + tools until a hard break (single fold).
    const runStart = i
    while (i < messages.length && !isHardBreak(messages[i])) {
      i += 1
    }
    const runEnd = i - 1

    const toolIndices: number[] = []
    for (let j = runStart; j <= runEnd; j++) {
      if (isToolMessage(messages[j])) toolIndices.push(j)
    }

    if (toolIndices.length === 0) {
      // Soft-only run with no tools → all passthrough (sticky agent picks latest)
      for (let j = runStart; j <= runEnd; j++) {
        segments.push({ kind: 'passthrough', index: j })
      }
      continue
    }

    const lastToolIndex = toolIndices[toolIndices.length - 1]
    const notes: ActivityNote[] = []
    const covered = new Set<number>()

    for (const ti of toolIndices) covered.add(ti)

    for (let j = runStart; j <= runEnd; j++) {
      const m = messages[j]
      // Never absorb agent text into process notes (sticky bubble instead).
      if (!isAbsorbableProcessNote(m)) continue
      if (j > lastToolIndex) continue
      const text = noteTextFromMessage(m).trim()
      if (text) {
        notes.push({ index: j, kind: noteKindFromMessage(m), text })
      }
      covered.add(j)
    }

    const coveredIndices = [...covered].sort((a, b) => a - b)
    const start = coveredIndices[0]
    const end = lastToolIndex

    segments.push({
      kind: 'activity',
      start,
      end,
      steps: pairSteps(messages, start, end),
      notes,
      coveredIndices,
    })

    // Agents and other non-covered soft notes → passthrough (sticky filter at render)
    for (let j = runStart; j <= runEnd; j++) {
      if (!covered.has(j) && isSoftNote(messages[j])) {
        segments.push({ kind: 'passthrough', index: j })
      }
    }
  }
  return segments
}

/**
 * Soft+tool run bounds containing index (exclusive of hard breaks).
 * Returns [runStart, runEnd] inclusive.
 */
export function softRunBounds(messages: ChatMsg[], index: number): [number, number] {
  let runStart = index
  while (runStart > 0 && !isHardBreak(messages[runStart - 1])) {
    runStart -= 1
  }
  let runEnd = index
  while (runEnd < messages.length - 1 && !isHardBreak(messages[runEnd + 1])) {
    runEnd += 1
  }
  return [runStart, runEnd]
}

/**
 * Within each soft+tool run, only the latest agent message is shown as a bubble.
 * Earlier agents are "replaced" (not rendered) so mid-turn tools never remove the bubble.
 */
export function supersededAgentIndices(messages: ChatMsg[]): Set<number> {
  const skip = new Set<number>()
  let i = 0
  while (i < messages.length) {
    if (isHardBreak(messages[i])) {
      i += 1
      continue
    }
    const runStart = i
    while (i < messages.length && !isHardBreak(messages[i])) {
      i += 1
    }
    const runEnd = i - 1
    let lastAgent = -1
    for (let j = runStart; j <= runEnd; j++) {
      if (messages[j].type === 'agent') lastAgent = j
    }
    if (lastAgent < 0) continue
    for (let j = runStart; j <= runEnd; j++) {
      if (messages[j].type === 'agent' && j !== lastAgent) {
        skip.add(j)
      }
    }
  }
  return skip
}

/** True if this agent index is the sticky bubble for its soft+tool run. */
export function isStickyAgentBubble(messages: ChatMsg[], index: number): boolean {
  if (messages[index]?.type !== 'agent') return false
  return !supersededAgentIndices(messages).has(index)
}

/** Stable React key for the sticky agent bubble in a run (avoids remount on replace). */
export function stickyAgentBubbleKey(messages: ChatMsg[], index: number): string {
  const [runStart] = softRunBounds(messages, index)
  return `agent-sticky-${runStart}`
}

/** Latest agent/thinking note text for the always-visible process line. */
export function latestProcessText(notes: ActivityNote[]): string {
  for (let i = notes.length - 1; i >= 0; i--) {
    const n = notes[i]
    if ((n.kind === 'agent' || n.kind === 'thinking') && n.text.trim()) {
      return n.text.trim()
    }
  }
  return ''
}

/** @deprecated Alias — use groupToolActivity (now turn/work-segment aware). */
export const groupTurnActivity = groupToolActivity


export type ToolActivityCategory =
  | 'edit'
  | 'explore'
  | 'search'
  | 'request'
  | 'command'
  | 'browser'
  | 'memory'
  | 'other'

const CATEGORY_ORDER: ToolActivityCategory[] = [
  'edit',
  'explore',
  'search',
  'request',
  'command',
  'browser',
  'memory',
  'other',
]

const EXACT_CATEGORY: Record<string, ToolActivityCategory> = {
  write_file: 'edit',
  edit_file: 'edit',
  read_file: 'explore',
  file_tree: 'explore',
  find_files: 'explore',
  repo_map: 'explore',
  code_definition: 'explore',
  code_references: 'explore',
  read_pdf: 'explore',
  grep_search: 'search',
  search_tools: 'search',
  search_flows: 'search',
  email_search: 'search',
  web_search: 'search',
  http_request: 'request',
  web_fetch: 'request',
  shell_command: 'command',
  process_read: 'command',
  process_write: 'command',
  process_list: 'command',
  process_kill: 'command',
}

/** Map a tool name to a human activity category. */
export function categorizeTool(name: string): ToolActivityCategory {
  const key = (name || '').toLowerCase()
  if (EXACT_CATEGORY[key]) return EXACT_CATEGORY[key]
  if (key.startsWith('browser_')) return 'browser'
  if (key.startsWith('memory_')) return 'memory'
  if (key.startsWith('process_')) return 'command'
  return 'other'
}

/** Pull a path/url hint from common tool arg shapes. */
export function extractPathHint(args: unknown): string | undefined {
  if (!args || typeof args !== 'object') return undefined
  const a = args as Record<string, unknown>
  for (const key of ['path', 'file', 'filename', 'glob', 'url']) {
    const v = a[key]
    if (typeof v === 'string' && v.trim()) return v.trim()
  }
  return undefined
}

function plural(n: number, singular: string, pluralForm?: string): string {
  return n === 1 ? singular : (pluralForm ?? `${singular}s`)
}

function countForCategory(steps: ToolActivityStep[], category: ToolActivityCategory): number {
  const inCat = steps.filter(s => categorizeTool(s.toolName) === category)
  if (inCat.length === 0) return 0

  if (category === 'edit' || category === 'explore') {
    const paths = new Set<string>()
    let missing = 0
    for (const s of inCat) {
      const hint = extractPathHint(s.args)
      if (hint) paths.add(hint)
      else missing += 1
    }
    return paths.size + missing
  }

  return inCat.length
}

function phraseForCategory(category: ToolActivityCategory, count: number, otherNames: string[]): string | null {
  if (count <= 0) return null
  switch (category) {
    case 'edit':
      return `Edited ${count} ${plural(count, 'file')}`
    case 'explore':
      return `explored ${count} ${plural(count, 'file')}`
    case 'search':
      return `${count} ${plural(count, 'search', 'searches')}`
    case 'request':
      return `${count} ${plural(count, 'request')}`
    case 'command':
      return `ran ${count} ${plural(count, 'command')}`
    case 'browser':
      return `${count} browser ${plural(count, 'action')}`
    case 'memory':
      return `${count} memory ${plural(count, 'lookup')}`
    case 'other':
      if (count === 1 && otherNames[0]) return `used ${otherNames[0]}`
      return `used ${count} other ${plural(count, 'tool')}`
  }
}

function buildCategorizedBody(steps: ToolActivityStep[]): string {
  const otherNames: string[] = []
  for (const s of steps) {
    if (categorizeTool(s.toolName) === 'other') otherNames.push(s.toolName)
  }

  const parts: string[] = []
  for (const cat of CATEGORY_ORDER) {
    const count = countForCategory(steps, cat)
    const phrase = phraseForCategory(cat, count, otherNames)
    if (phrase) parts.push(phrase)
  }

  if (parts.length === 0) return 'Done'
  // Capitalize first phrase only (Cursor style: "Edited 2 files, explored 3 files")
  const [first, ...rest] = parts
  return [first.charAt(0).toUpperCase() + first.slice(1), ...rest].join(', ')
}

function truncateDetail(s: string, maxLen = 60): string {
  const t = s.replace(/\s+/g, ' ').trim()
  if (t.length <= maxLen) return t
  return t.slice(0, maxLen - 1) + '…'
}

function argString(args: unknown, ...keys: string[]): string | undefined {
  if (!args || typeof args !== 'object') return undefined
  const a = args as Record<string, unknown>
  for (const key of keys) {
    const v = a[key]
    if (typeof v === 'string' && v.trim()) return v.trim()
  }
  return undefined
}

/**
 * Informative in-flight status for a running tool step.
 * e.g. `Running \`ls -lah\` with shell_command`
 */
export function liveActivityHint(step: ToolActivityStep): string {
  const name = step.toolName || 'tool'
  const key = name.toLowerCase()
  const args = step.args
  const path = extractPathHint(args)
  const command = argString(args, 'command', 'cmd')
  const query = argString(args, 'query', 'pattern', 'regex', 'search')
  const credName = argString(args, 'name', 'credential', 'credential_name')

  const withTool = (action: string, detail?: string) => {
    if (detail) return `${action} ${detail} with ${name}`
    return `${action} with ${name}`
  }

  if (key === 'shell_command' || key === 'process_write') {
    if (command) return withTool('Running', `\`${truncateDetail(command)}\``)
    return withTool('Running command')
  }
  if (key.startsWith('process_')) {
    return withTool('Running process')
  }
  if (key === 'write_file' || key === 'edit_file') {
    if (path) return withTool('Editing', truncateDetail(path))
    return withTool('Editing')
  }
  if (key === 'read_file' || key === 'read_pdf') {
    if (path) return withTool('Reading', truncateDetail(path))
    return withTool('Reading')
  }
  if (key === 'file_tree' || key === 'find_files' || key === 'repo_map' || key === 'code_definition' || key === 'code_references') {
    if (path) return withTool('Exploring', truncateDetail(path))
    return withTool('Exploring')
  }
  if (key === 'grep_search' || key === 'search_tools' || key === 'search_flows' || key === 'email_search') {
    if (query) return withTool('Searching for', `"${truncateDetail(query, 40)}"`)
    return withTool('Searching')
  }
  if (key === 'http_request' || key === 'web_fetch') {
    if (path) return withTool('Fetching', truncateDetail(path))
    return withTool('Fetching')
  }
  if (key === 'list_credentials') {
    return withTool('Listing credentials')
  }
  if (key === 'save_credential') {
    if (credName) return withTool('Saving credential', truncateDetail(credName))
    return withTool('Saving credential')
  }
  if (key === 'remove_credential') {
    if (credName) return withTool('Removing credential', truncateDetail(credName))
    return withTool('Removing credential')
  }
  if (key === 'test_credential' || key === 'resolve_credential') {
    if (credName) return withTool('Using credential', truncateDetail(credName))
    return withTool('Using credentials')
  }
  if (key.startsWith('credential') || key.includes('credential')) {
    return withTool('Working with credentials')
  }
  if (key === 'browser_navigate') {
    if (path) return withTool('Navigating to', truncateDetail(path))
    return withTool('Navigating')
  }
  if (key.startsWith('browser_')) {
    return withTool('Browsing')
  }
  if (key.startsWith('memory_')) {
    if (query) return withTool('Looking up memory for', `"${truncateDetail(query, 40)}"`)
    return withTool('Looking up memory')
  }

  if (command) return withTool('Running', `\`${truncateDetail(command)}\``)
  if (path) return withTool('Running', truncateDetail(path))
  if (query) return withTool('Running', `"${truncateDetail(query, 40)}"`)
  return `Running ${name}`
}

/**
 * Status for the bottom streaming pill: live tool hint, or Thinking… when idle between tools.
 */
export function deriveLiveStreamStatus(messages: ChatMsg[]): string {
  const segs = groupToolActivity(messages)
  if (messages.length === 0) return 'Thinking…'
  const last = messages.length - 1
  // Final answer streaming in an Agent bubble — not a tool status line.
  const lastMsg = messages[last]
  if (lastMsg?.type === 'agent' && (lastMsg as { _streaming?: boolean })._streaming) {
    return 'Thinking…'
  }
  for (let i = segs.length - 1; i >= 0; i--) {
    const seg = segs[i]
    if (seg.kind !== 'activity') continue
    // Only mirror status when this activity covers the latest message.
    if (!seg.coveredIndices.includes(last)) break
    const running = [...seg.steps].reverse().find(s => s.status === 'running')
    if (running) return liveActivityHint(running)
    break
  }
  return 'Thinking…'
}

const LIVE_LEAD_VERBS =
  /^(Running|Editing|Reading|Exploring|Searching|Fetching|Listing|Saving|Removing|Using|Navigating|Browsing|Looking)\b/

const WHOLE_LEAD_PHRASES = [
  'Looking up memory…',
  'Running command…',
  'Editing…',
  'Exploring…',
  'Searching…',
  'Fetching…',
  'Browsing…',
  'Thinking…',
  'No tools',
  'Done',
]

/** Split summary text into accent lead + muted rest (+ optional error suffix). */
export function splitActivitySummary(
  text: string,
  variant: ActivitySummaryVariant,
): Pick<ActivitySummary, 'lead' | 'rest' | 'errorSuffix'> {
  let body = text
  let errorSuffix: string | undefined
  if (variant === 'error') {
    const idx = text.indexOf(' · ')
    if (idx >= 0) {
      body = text.slice(0, idx)
      errorSuffix = text.slice(idx)
    }
  }

  for (const phrase of WHOLE_LEAD_PHRASES) {
    if (body === phrase) {
      return { lead: phrase, rest: '', errorSuffix }
    }
    if (body.startsWith(phrase)) {
      return { lead: phrase, rest: body.slice(phrase.length), errorSuffix }
    }
  }

  const liveLead = body.match(LIVE_LEAD_VERBS)
  if (liveLead) {
    return {
      lead: liveLead[1],
      rest: body.slice(liveLead[1].length),
      errorSuffix,
    }
  }

  const verbMatch = body.match(/^(Edited|Explored|Ran|Used|Using)\b/)
  if (verbMatch) {
    return {
      lead: verbMatch[1],
      rest: body.slice(verbMatch[1].length),
      errorSuffix,
    }
  }

  const comma = body.indexOf(', ')
  if (comma >= 0) {
    return {
      lead: body.slice(0, comma),
      rest: body.slice(comma),
      errorSuffix,
    }
  }

  return { lead: body, rest: '', errorSuffix }
}

function withSplit(text: string, variant: ActivitySummaryVariant): ActivitySummary {
  return { text, variant, ...splitActivitySummary(text, variant) }
}

function countLines(s: string): number {
  if (!s) return 0
  const parts = s.split('\n')
  if (parts.length === 1 && parts[0] === '') return 0
  // Ignore a single trailing newline so "a\n" counts as 1 line.
  if (parts.length > 1 && parts[parts.length - 1] === '') {
    return parts.length - 1
  }
  return parts.length
}

/**
 * Trailing metric for the collapsed activity line.
 * Prefer approximate +/− line counts from edit/write args; else a step badge.
 */
export function activityStats(steps: ToolActivityStep[]): ActivityStats {
  let added = 0
  let removed = 0
  for (const s of steps) {
    const name = (s.toolName || '').toLowerCase()
    if (name !== 'write_file' && name !== 'edit_file') continue
    if (!s.args || typeof s.args !== 'object') continue
    const args = s.args as Record<string, unknown>
    if (typeof args.content === 'string') added += countLines(args.content)
    if (typeof args.new_string === 'string') added += countLines(args.new_string)
    if (typeof args.old_string === 'string') removed += countLines(args.old_string)
  }
  if (added > 0 || removed > 0) {
    return { kind: 'diff', added, removed }
  }
  return { kind: 'badge', count: Math.max(steps.length, 1) }
}

/** Compact header copy for a tool activity block. */
export function activitySummary(
  steps: ToolActivityStep[],
  opts: { streaming?: boolean; noteCount?: number } = {},
): ActivitySummary {
  if (steps.length === 0) {
    return withSplit('No tools', 'complete')
  }

  const failed = steps.filter(s => s.status === 'error')
  const running = steps.find(s => s.status === 'running')

  if (opts.streaming && running) {
    return withSplit(liveActivityHint(running), 'running')
  }

  let body = buildCategorizedBody(steps)
  if (opts.noteCount && opts.noteCount > 0) {
    body = `${body} · ${opts.noteCount} ${opts.noteCount === 1 ? 'note' : 'notes'}`
  }

  if (failed.length > 0) {
    if (failed.length === 1) {
      return withSplit(`${body} · ${failed[0].toolName} failed`, 'error')
    }
    return withSplit(`${body} · ${failed.length} failed`, 'error')
  }

  return withSplit(body, 'complete')
}

/** Index lookup helpers for the StudioChat render loop. */
export function buildActivityRenderIndex(
  messages: ChatMsg[],
  opts: GroupToolActivityOptions = {},
): {
  activityByStart: Map<number, Extract<MessageSegment, { kind: 'activity' }>>
  skipIndices: Set<number>
  lastActivityStart: number
} {
  const activityByStart = new Map<number, Extract<MessageSegment, { kind: 'activity' }>>()
  const skipIndices = new Set<number>()
  let lastActivityStart = -1
  for (const seg of groupToolActivity(messages, opts)) {
    if (seg.kind !== 'activity') continue
    activityByStart.set(seg.start, seg)
    if (seg.start > lastActivityStart) lastActivityStart = seg.start
    for (const idx of seg.coveredIndices) {
      if (idx !== seg.start) skipIndices.add(idx)
    }
  }
  // Superseded agents in a run: only the latest agent bubble is sticky.
  for (const idx of supersededAgentIndices(messages)) {
    skipIndices.add(idx)
  }
  return { activityByStart, skipIndices, lastActivityStart }
}
