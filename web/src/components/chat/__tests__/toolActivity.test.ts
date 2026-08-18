import { describe, it, expect } from 'vitest'
import type { ChatMsg } from '../chatTypes'
import {
  activityStats,
  activitySummary,
  buildActivityRenderIndex,
  categorizeTool,
  deriveLiveStreamStatus,
  extractPathHint,
  groupToolActivity,
  hasAppFence,
  isAppProgressAgent,
  isHardBreakType,
  isHousekeepingNote,
  isSoftNoteType,
  isStickyAgentBubble,
  isSubstantiveMessage,
  isToolResultError,
  latestProcessText,
  liveActivityHint,
  onlyHousekeepingToolsBetween,
  previewValue,
  stickyAgentBubbleKey,
  supersededAgentIndices,
} from '../toolActivity'

describe('isToolResultError', () => {
  it('detects success: false and error fields', () => {
    expect(isToolResultError({ success: false })).toBe(true)
    expect(isToolResultError({ error: 'boom' })).toBe(true)
    expect(isToolResultError({ ok: false })).toBe(true)
    expect(isToolResultError({ status: 'failed' })).toBe(true)
  })

  it('does not flag successful results', () => {
    expect(isToolResultError({ success: true, data: 1 })).toBe(false)
    expect(isToolResultError('ok')).toBe(false)
    expect(isToolResultError(null)).toBe(false)
  })
})

describe('previewValue', () => {
  it('stringifies and truncates', () => {
    expect(previewValue({ url: 'https://example.com' })).toContain('example.com')
    expect(previewValue('a'.repeat(100), 20).endsWith('…')).toBe(true)
  })
})

describe('groupToolActivity', () => {
  it('keeps ONE activity for all tools; agents never absorbed into notes', () => {
    const messages: ChatMsg[] = [
      { type: 'user', content: 'hi' },
      { type: 'tool_call', toolName: 'search_tools', toolArgs: { q: 'x' } },
      { type: 'tool_result', toolName: 'search_tools', toolResult: { ok: true } },
      { type: 'agent', content: 'checking next step' },
      { type: 'tool_call', toolName: 'http_request', toolArgs: { url: 'https://a' } },
      { type: 'tool_result', toolName: 'http_request', toolResult: 'body' },
      { type: 'agent', content: 'done' },
    ]

    const segs = groupToolActivity(messages)
    expect(segs.map(s => s.kind)).toEqual([
      'passthrough', // user
      'activity', // both tools in one fold
      'passthrough', // first agent
      'passthrough', // final agent
    ])

    const activity = segs[1]
    expect(activity.kind).toBe('activity')
    if (activity.kind !== 'activity') return
    expect(activity.steps).toHaveLength(2)
    expect(activity.notes).toHaveLength(0)
    expect(activity.coveredIndices).not.toContain(3)
    expect(activity.coveredIndices).not.toContain(6)
  })

  it('absorbs thinking between tools into the same activity', () => {
    const messages: ChatMsg[] = [
      { type: 'tool_call', toolName: 'search_tools', toolArgs: {} },
      { type: 'tool_result', toolName: 'search_tools', toolResult: {} },
      { type: 'thinking', content: 'next I will fetch' },
      { type: 'tool_call', toolName: 'http_request', toolArgs: {} },
      { type: 'tool_result', toolName: 'http_request', toolResult: 'ok' },
    ]
    const segs = groupToolActivity(messages)
    expect(segs.map(s => s.kind)).toEqual(['activity'])
    if (segs[0].kind !== 'activity') throw new Error('expected activity')
    expect(segs[0].notes.some(n => n.text === 'next I will fetch')).toBe(true)
    expect(segs[0].steps).toHaveLength(2)
  })

  it('marks unpaired call as running', () => {
    const messages: ChatMsg[] = [
      { type: 'tool_call', toolName: 'http_request', toolArgs: { url: 'https://a' } },
    ]
    const segs = groupToolActivity(messages)
    expect(segs).toHaveLength(1)
    expect(segs[0].kind).toBe('activity')
    if (segs[0].kind !== 'activity') return
    expect(segs[0].steps[0].status).toBe('running')
  })

  it('pairs parallel call/call/result/result by tool name FIFO', () => {
    const messages: ChatMsg[] = [
      { type: 'tool_call', toolName: 'web_search', toolArgs: { query: 'a' } },
      { type: 'tool_call', toolName: 'read_file', toolArgs: { path: '/x' } },
      { type: 'tool_result', toolName: 'web_search', toolResult: 'hits' },
      { type: 'tool_result', toolName: 'read_file', toolResult: 'body' },
    ]
    const segs = groupToolActivity(messages)
    expect(segs).toHaveLength(1)
    if (segs[0].kind !== 'activity') throw new Error('expected activity')
    expect(segs[0].steps).toHaveLength(2)
    expect(segs[0].steps[0]).toMatchObject({
      toolName: 'web_search',
      status: 'complete',
      result: 'hits',
    })
    expect(segs[0].steps[1]).toMatchObject({
      toolName: 'read_file',
      status: 'complete',
      result: 'body',
    })
  })

  it('breaks fold on subtask_execution and keeps panel outside', () => {
    const messages: ChatMsg[] = [
      { type: 'tool_call', toolName: 'shell_command', toolArgs: { command: 'a' } },
      { type: 'tool_result', toolName: 'shell_command', toolResult: 'ok' },
      {
        type: 'subtask_execution',
        tasks: [{ name: 't1', description: 'd' }],
        events: [],
        status: 'running',
      },
      { type: 'tool_call', toolName: 'read_file', toolArgs: { path: 'x' } },
      { type: 'tool_result', toolName: 'read_file', toolResult: 'y' },
    ]
    const segs = groupToolActivity(messages)
    expect(segs.map(s => s.kind)).toEqual(['activity', 'passthrough', 'activity'])
    const mid = segs[1]
    expect(mid.kind).toBe('passthrough')
    if (mid.kind === 'passthrough') expect(mid.index).toBe(2)
    if (segs[0].kind === 'activity') {
      expect(segs[0].coveredIndices).not.toContain(2)
    }
  })

  it('keeps approval and artifact outside the fold', () => {
    const messages: ChatMsg[] = [
      { type: 'tool_call', toolName: 'shell_command', toolArgs: { command: 'ls' } },
      { type: 'tool_result', toolName: 'shell_command', toolResult: 'ok' },
      { type: 'approval', toolName: 'shell_command', options: ['Allow', 'Deny'] },
      { type: 'tool_call', toolName: 'write_file', toolArgs: { path: 'a.md', content: 'x' } },
      { type: 'tool_result', toolName: 'write_file', toolResult: { ok: true } },
      { type: 'artifact', path: 'a.md', toolName: 'write_file' },
    ]
    const segs = groupToolActivity(messages)
    expect(segs.map(s => s.kind)).toEqual([
      'activity',
      'passthrough', // approval
      'activity',
      'passthrough', // artifact
    ])
  })

  it('breaks on browser_handoff', () => {
    const messages: ChatMsg[] = [
      { type: 'tool_call', toolName: 'browser_request_human', toolArgs: {} },
      {
        type: 'browser_handoff',
        vncProxyUrl: 'http://vnc',
        pageUrl: 'http://app',
        pageTitle: 'App',
        reason: 'captcha',
      },
    ]
    const segs = groupToolActivity(messages)
    expect(segs.map(s => s.kind)).toEqual(['activity', 'passthrough'])
  })

  it('does not create activity for soft-only or text-only turns', () => {
    expect(groupToolActivity([
      { type: 'user', content: 'hi' },
      { type: 'agent', content: 'hello' },
    ]).every(s => s.kind === 'passthrough')).toBe(true)

    expect(groupToolActivity([
      { type: 'thinking', content: '…' },
      { type: 'auto_approved', toolName: 'shell_command' },
    ]).every(s => s.kind === 'passthrough')).toBe(true)
  })

  it('defaults unknown types to hard break', () => {
    expect(isHardBreakType('brand_new_panel')).toBe(true)
    expect(isSoftNoteType('brand_new_panel')).toBe(false)
  })

  it('never absorbs agent text (sticky bubble owns display)', () => {
    const messages: ChatMsg[] = [
      { type: 'tool_call', toolName: 'shell_command', toolArgs: { command: 'ls' } },
      { type: 'tool_result', toolName: 'shell_command', toolResult: 'ok' },
      { type: 'agent', content: 'Here is your answer.', _streaming: true },
    ]
    const segs = groupToolActivity(messages, { absorbTrailingSoft: true })
    expect(segs.map(s => s.kind)).toEqual(['activity', 'passthrough'])
    if (segs[0].kind === 'activity') {
      expect(segs[0].coveredIndices).not.toContain(2)
      expect(segs[0].notes).toHaveLength(0)
    }
  })

  it('never absorbs astonish-app agent messages (AppCodeIndicator must stay visible)', () => {
    const trailing: ChatMsg[] = [
      { type: 'tool_call', toolName: 'search_tools', toolArgs: {} },
      { type: 'tool_result', toolName: 'search_tools', toolResult: {} },
      {
        type: 'agent',
        content: 'Building it now.\n\n```astonish-app\nfunction App() { return <div/> }\n',
        _streaming: true,
      },
    ]
    const live = groupToolActivity(trailing)
    expect(live.map(s => s.kind)).toEqual(['activity', 'passthrough'])
    if (live[0].kind !== 'activity') throw new Error('expected activity')
    expect(live[0].coveredIndices).not.toContain(2)

    const between: ChatMsg[] = [
      { type: 'tool_call', toolName: 'read_file', toolArgs: { path: 'a' } },
      { type: 'tool_result', toolName: 'read_file', toolResult: 'x' },
      {
        type: 'agent',
        content: '```astonish-app\nconst App = () => null\n```',
      },
      { type: 'tool_call', toolName: 'write_file', toolArgs: { path: 'b' } },
      { type: 'tool_result', toolName: 'write_file', toolResult: { ok: true } },
    ]
    const mid = groupToolActivity(between)
    expect(mid.filter(s => s.kind === 'activity')).toHaveLength(1)
    const activity = mid.find(s => s.kind === 'activity')
    if (!activity || activity.kind !== 'activity') throw new Error('expected activity')
    expect(activity.coveredIndices).not.toContain(2)
    expect(isAppProgressAgent(between[2])).toBe(true)
    expect(hasAppFence('plain text')).toBe(false)
  })
})

describe('sticky agent bubble', () => {
  it('only the latest agent in a run is sticky; earlier are superseded', () => {
    const messages: ChatMsg[] = [
      { type: 'user', content: 'hi' },
      { type: 'tool_call', toolName: 'a', toolArgs: {} },
      { type: 'tool_result', toolName: 'a', toolResult: {} },
      { type: 'agent', content: 'first' },
      { type: 'tool_call', toolName: 'b', toolArgs: {} },
      { type: 'tool_result', toolName: 'b', toolResult: {} },
      { type: 'agent', content: 'final', _streaming: true },
    ]
    // While only first agent exists (indices 0-3): it is sticky
    const mid: ChatMsg[] = messages.slice(0, 4)
    expect(isStickyAgentBubble(mid, 3)).toBe(true)
    expect(supersededAgentIndices(mid).has(3)).toBe(false)

    // After second agent: first superseded, second sticky
    expect(isStickyAgentBubble(messages, 3)).toBe(false)
    expect(isStickyAgentBubble(messages, 6)).toBe(true)
    expect(supersededAgentIndices(messages).has(3)).toBe(true)
    expect(supersededAgentIndices(messages).has(6)).toBe(false)
    expect(stickyAgentBubbleKey(messages, 6)).toBe(stickyAgentBubbleKey(messages, 3))
  })

  it('user hard break starts a new sticky run', () => {
    const messages: ChatMsg[] = [
      { type: 'user', content: '1' },
      { type: 'agent', content: 'answer 1' },
      { type: 'user', content: '2' },
      { type: 'agent', content: 'answer 2' },
    ]
    expect(isStickyAgentBubble(messages, 1)).toBe(true)
    expect(isStickyAgentBubble(messages, 3)).toBe(true)
    expect(stickyAgentBubbleKey(messages, 1)).not.toBe(stickyAgentBubbleKey(messages, 3))
  })

  it('marks error results', () => {
    const messages: ChatMsg[] = [
      { type: 'tool_call', toolName: 'http_request', toolArgs: {} },
      { type: 'tool_result', toolName: 'http_request', toolResult: { error: 'timeout' } },
    ]
    const segs = groupToolActivity(messages)
    if (segs[0].kind !== 'activity') throw new Error('expected activity')
    expect(segs[0].steps[0].status).toBe('error')
  })
})

describe('categorizeTool', () => {
  it('maps built-ins and prefixes', () => {
    expect(categorizeTool('write_file')).toBe('edit')
    expect(categorizeTool('read_file')).toBe('explore')
    expect(categorizeTool('grep_search')).toBe('search')
    expect(categorizeTool('web_search')).toBe('search')
    expect(categorizeTool('http_request')).toBe('request')
    expect(categorizeTool('shell_command')).toBe('command')
    expect(categorizeTool('browser_navigate')).toBe('browser')
    expect(categorizeTool('mcp_github_search')).toBe('other')
  })
})

describe('activitySummary', () => {
  it('summarizes mixed complete steps', () => {
    const steps = [
      { toolName: 'write_file', status: 'complete' as const, callIndex: 0 },
      { toolName: 'write_file', status: 'complete' as const, callIndex: 1 },
      { toolName: 'read_file', status: 'complete' as const, callIndex: 2 },
      { toolName: 'grep_search', status: 'complete' as const, callIndex: 3 },
      { toolName: 'grep_search', status: 'complete' as const, callIndex: 4 },
    ]
    const s = activitySummary(steps)
    expect(s.variant).toBe('complete')
    expect(s.text).toMatch(/Edited 2 files/)
    expect(s.text).toMatch(/explored/)
    expect(s.text).toMatch(/2 searches/)
  })

  it('uses live hint while a step is running', () => {
    const steps = [
      {
        toolName: 'shell_command',
        status: 'running' as const,
        callIndex: 0,
        args: { command: 'ls -la' },
      },
    ]
    const s = activitySummary(steps, { streaming: true })
    expect(s.variant).toBe('running')
    expect(s.text).toContain('ls -la')
  })

  it('appends error suffix', () => {
    const steps = [
      { toolName: 'http_request', status: 'error' as const, callIndex: 0 },
      { toolName: 'read_file', status: 'complete' as const, callIndex: 1 },
    ]
    const s = activitySummary(steps)
    expect(s.variant).toBe('error')
    expect(s.text).toMatch(/http_request failed/)
  })
})

describe('liveActivityHint', () => {
  it('formats shell and path tools', () => {
    expect(
      liveActivityHint({
        toolName: 'shell_command',
        status: 'running',
        callIndex: 0,
        args: { command: 'pwd' },
      }),
    ).toContain('pwd')
    expect(
      liveActivityHint({
        toolName: 'read_file',
        status: 'running',
        callIndex: 0,
        args: { path: '/tmp/x' },
      }),
    ).toContain('/tmp/x')
  })
})

describe('extractPathHint / latestProcessText', () => {
  it('extracts path-like args', () => {
    expect(extractPathHint({ path: 'a/b.ts' })).toBe('a/b.ts')
    expect(extractPathHint({ file: '/abs/c' })).toBe('/abs/c')
  })

  it('returns latest agent/thinking note', () => {
    expect(
      latestProcessText([
        { index: 0, kind: 'thinking', text: 'old' },
        { index: 1, kind: 'agent', text: 'new plan' },
      ]),
    ).toBe('new plan')
  })
})

describe('splitActivitySummary / activityStats', () => {
  it('splits lead/rest for mixed categories', () => {
    const summary = activitySummary([
      { toolName: 'write_file', status: 'complete', callIndex: 0, args: { path: 'a.md' } },
      { toolName: 'grep_search', status: 'complete', callIndex: 1 },
    ])
    expect(summary.lead).toBe('Edited')
    expect(summary.rest).toBe(' 1 file, 1 search')
  })

  it('infers changed +/- lines from edit_file args; badge otherwise', () => {
    // Only the two appended lines actually change; a/b/c are unchanged and must
    // not be counted as churn (line-level diff, not raw line totals).
    expect(
      activityStats([
        {
          toolName: 'edit_file',
          status: 'complete',
          callIndex: 0,
          args: { path: 'a.ts', old_string: 'a\nb\nc', new_string: 'a\nb\nc\nd\ne' },
        },
      ]),
    ).toEqual({ kind: 'diff', added: 2, removed: 0 })
    // A replacement where only one line differs counts exactly one +/-.
    expect(
      activityStats([
        {
          toolName: 'edit_file',
          status: 'complete',
          callIndex: 0,
          args: { path: 'a.ts', old_string: 'a\nb\nc', new_string: 'a\nX\nc' },
        },
      ]),
    ).toEqual({ kind: 'diff', added: 1, removed: 1 })
    expect(
      activityStats([
        { toolName: 'shell_command', status: 'complete', callIndex: 0 },
        { toolName: 'http_request', status: 'complete', callIndex: 1 },
      ]),
    ).toEqual({ kind: 'badge', count: 2 })
  })
})

describe('buildActivityRenderIndex', () => {
  it('maps start indices and skips covered ones', () => {
    const messages: ChatMsg[] = [
      { type: 'tool_call', toolName: 'a', toolArgs: {} },
      { type: 'tool_result', toolName: 'a', toolResult: {} },
      { type: 'agent', content: 'x' },
    ]
    const { activityByStart, skipIndices, lastActivityStart } = buildActivityRenderIndex(messages)
    expect(activityByStart.has(0)).toBe(true)
    expect(skipIndices.has(1)).toBe(true)
    expect(skipIndices.has(0)).toBe(false)
    expect(skipIndices.has(2)).toBe(false)
    expect(lastActivityStart).toBe(0)
  })

  it('skips superseded agents; keeps sticky agent and activity tools', () => {
    const messages: ChatMsg[] = [
      { type: 'tool_call', toolName: 'a', toolArgs: {} },
      { type: 'tool_result', toolName: 'a', toolResult: {} },
      { type: 'agent', content: 'first' },
      { type: 'tool_call', toolName: 'b', toolArgs: {} },
      { type: 'tool_result', toolName: 'b', toolResult: {} },
      { type: 'agent', content: 'second', _streaming: true },
    ]
    const { skipIndices, activityByStart } = buildActivityRenderIndex(messages)
    expect(activityByStart.has(0)).toBe(true)
    expect(activityByStart.get(0)?.steps).toHaveLength(2)
    expect(skipIndices.has(2)).toBe(true) // first agent superseded
    expect(skipIndices.has(5)).toBe(false) // sticky second agent
  })
})

describe('deriveLiveStreamStatus', () => {
  it('returns tool hint when last covered message is a running tool', () => {
    const messages: ChatMsg[] = [
      { type: 'tool_call', toolName: 'shell_command', toolArgs: { command: 'ls' } },
    ]
    expect(deriveLiveStreamStatus(messages)).toContain('ls')
  })

  it('returns Thinking… when trailing agent is streaming', () => {
    const messages: ChatMsg[] = [
      { type: 'tool_call', toolName: 'shell_command', toolArgs: { command: 'ls' } },
      { type: 'tool_result', toolName: 'shell_command', toolResult: 'ok' },
      { type: 'agent', content: 'Hello', _streaming: true },
    ]
    expect(deriveLiveStreamStatus(messages)).toBe('Thinking…')
  })
})

describe('supersededAgentIndices - housekeeping preservation', () => {
  it('preserves substantive answer when followed by memory_save + trivial note', () => {
    const messages: ChatMsg[] = [
      { type: 'user', content: 'list VMs' },
      {
        type: 'agent',
        content:
          'Here are the available VMs:\n\n| Name | Status | IP |\n|---|---|---|\n| vm-1 | ACTIVE | 10.0.0.1 |\n| vm-2 | ACTIVE | 10.0.0.2 |\n| vm-3 | SHUTOFF | 10.0.0.3 |',
      },
      { type: 'tool_call', toolName: 'memory_save', toolArgs: {} },
      { type: 'tool_result', toolName: 'memory_save', toolResult: { status: 'saved' } },
      { type: 'agent', content: "I've saved the endpoint details to memory." },
    ]
    const superseded = supersededAgentIndices(messages)
    expect(superseded.has(1)).toBe(false) // VM list is visible
    expect(superseded.has(4)).toBe(false) // trivial note is also visible
  })

  it('normal superseding still works for intermediate thinking', () => {
    const messages: ChatMsg[] = [
      { type: 'user', content: 'find the VMs' },
      { type: 'agent', content: 'Let me check...' },
      { type: 'tool_call', toolName: 'shell_command', toolArgs: { command: 'openstack server list' } },
      { type: 'tool_result', toolName: 'shell_command', toolResult: 'vm-1 ACTIVE\nvm-2 ACTIVE' },
      {
        type: 'agent',
        content:
          'Here are the results:\n\n| Name | Status |\n|---|---|\n| vm-1 | ACTIVE |\n| vm-2 | ACTIVE |',
      },
    ]
    const superseded = supersededAgentIndices(messages)
    expect(superseded.has(1)).toBe(true) // 'Let me check...' is superseded
    expect(superseded.has(4)).toBe(false) // real answer is visible
  })

  it('multiple substantive messages - only last substantive before housekeeping survives', () => {
    const messages: ChatMsg[] = [
      { type: 'user', content: 'do stuff' },
      { type: 'agent', content: 'First result with lots of content...'.repeat(20) },
      { type: 'tool_call', toolName: 'edit_file', toolArgs: {} },
      { type: 'tool_result', toolName: 'edit_file', toolResult: {} },
      { type: 'agent', content: 'Updated result with even more content and details...'.repeat(20) },
      { type: 'tool_call', toolName: 'memory_save', toolArgs: {} },
      { type: 'tool_result', toolName: 'memory_save', toolResult: {} },
      { type: 'agent', content: 'Done.' },
    ]
    const superseded = supersededAgentIndices(messages)
    expect(superseded.has(1)).toBe(true) // first agent superseded (edit_file is not housekeeping)
    expect(superseded.has(4)).toBe(false) // last substantive before housekeeping survives
    expect(superseded.has(7)).toBe(false) // 'Done.' is visible
  })

  it('short final message after non-housekeeping tool still supersedes normally', () => {
    const messages: ChatMsg[] = [
      { type: 'user', content: 'run it' },
      {
        type: 'agent',
        content: 'Here is a very long detailed response with tables and code blocks...'.repeat(10),
      },
      { type: 'tool_call', toolName: 'shell_command', toolArgs: {} },
      { type: 'tool_result', toolName: 'shell_command', toolResult: {} },
      { type: 'agent', content: 'Done.' },
    ]
    const superseded = supersededAgentIndices(messages)
    expect(superseded.has(1)).toBe(true) // superseded (shell_command is not housekeeping)
    expect(superseded.has(4)).toBe(false) // final message is visible
  })

  it('isHousekeepingNote helper', () => {
    expect(isHousekeepingNote('Saved to memory.')).toBe(true)
    expect(isHousekeepingNote('Done.')).toBe(true)
    expect(isHousekeepingNote("I've noted that for next time.")).toBe(true)
    expect(
      isHousekeepingNote('Here are the VMs:\n| Name | Status |\n|---|---|\n| vm-1 | ACTIVE |'),
    ).toBe(false) // has markdown table
    expect(isHousekeepingNote('a'.repeat(200))).toBe(false) // too long
  })

  it('isSubstantiveMessage helper', () => {
    expect(isSubstantiveMessage('Let me check...')).toBe(false) // short, no formatting
    expect(isSubstantiveMessage('a'.repeat(250))).toBe(true) // long enough
    expect(isSubstantiveMessage('Results:\n```\ncode here\n```')).toBe(true) // has code block
    expect(isSubstantiveMessage('Items:\n- one\n- two\n- three')).toBe(true) // has list
  })

  it('onlyHousekeepingToolsBetween helper', () => {
    const messages: ChatMsg[] = [
      { type: 'agent', content: 'answer' },
      { type: 'tool_call', toolName: 'memory_save', toolArgs: {} },
      { type: 'tool_result', toolName: 'memory_save', toolResult: {} },
      { type: 'agent', content: 'Done.' },
    ]
    expect(onlyHousekeepingToolsBetween(messages, 0, 3)).toBe(true)

    const mixed: ChatMsg[] = [
      { type: 'agent', content: 'answer' },
      { type: 'tool_call', toolName: 'shell_command', toolArgs: {} },
      { type: 'tool_result', toolName: 'shell_command', toolResult: {} },
      { type: 'agent', content: 'Done.' },
    ]
    expect(onlyHousekeepingToolsBetween(mixed, 0, 3)).toBe(false)
  })
})
