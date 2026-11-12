import type { SubtaskEventItem } from './studioChat'

export interface DelegationHandoff {
  version: 1
  parent_session_id: string
  batch_id: string
  task_id: string
  child_session_id: string
  attempt: number
  name: string
  original_task: string
  effective_task: string
  original_instructions: string
  effective_instructions: string
  plan_step: string
  workdir: string
  preferred_tools: string[] | null
  initial_tool_names: string[] | null
  plan: {
    status: 'matched' | 'no_scoped_plan' | 'no_plan_step'
    plan_id?: string
    revision?: number
    sequence?: number
    goal?: string
    source_mirror?: string
    document?: { context: string; what_not_to_do: string; verification: string }
    step?: {
      name: string; description: string; details: string; summary: string
      files: { path: string; kind: string }[] | null
      outcome: string; verify: string; verify_kind: string; parallel_group?: string
    }
  }
}

const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v)
const strings = (v: Record<string, unknown>, keys: string[]) => keys.every(k => typeof v[k] === 'string')
const stringList = (v: unknown) => v === null || (Array.isArray(v) && v.every(s => typeof s === 'string'))

/** Strict at the display boundary: do not interpret malformed or future records as v1. */
export function parseDelegationHandoff(event?: SubtaskEventItem): DelegationHandoff | null {
  if (event?.type !== 'handoff' || !event.text) return null
  try {
    const h: unknown = JSON.parse(event.text)
    if (!object(h) || h.version !== 1 || !strings(h, ['parent_session_id', 'batch_id', 'task_id', 'child_session_id', 'name', 'original_task', 'effective_task', 'original_instructions', 'effective_instructions', 'plan_step', 'workdir']) || !Number.isInteger(h.attempt) || Number(h.attempt) < 1 || !stringList(h.preferred_tools) || !stringList(h.initial_tool_names)) return null
    if ((event.child_session_id && event.child_session_id !== h.child_session_id) || (event.task_id && event.task_id !== h.task_id) || (event.batch_id && event.batch_id !== h.batch_id) || (event.attempt != null && event.attempt !== h.attempt)) return null
    const p = h.plan
    if (!object(p) || !['matched', 'no_scoped_plan', 'no_plan_step'].includes(String(p.status))) return null
    if (['plan_id', 'goal', 'source_mirror'].some(k => p[k] != null && typeof p[k] !== 'string')) return null
    if (['revision', 'sequence'].some(k => p[k] != null && typeof p[k] !== 'number')) return null
    if (p.status === 'matched') {
      if (!object(p.document) || !strings(p.document, ['context', 'what_not_to_do', 'verification'])) return null
      if (!object(p.step) || !strings(p.step, ['name', 'description', 'details', 'summary', 'outcome', 'verify', 'verify_kind']) || p.step.name !== h.plan_step) return null
      if (p.step.parallel_group != null && typeof p.step.parallel_group !== 'string') return null
      if (p.step.files !== null && (!Array.isArray(p.step.files) || !p.step.files.every(f => object(f) && strings(f, ['path', 'kind'])))) return null
    }
    return h as unknown as DelegationHandoff
  } catch { return null }
}
