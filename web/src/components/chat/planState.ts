import type { ChatMsg, PlanMessage, PlanSnapshot, PlanStepDefinition, PlanStepInfo } from './chatTypes'

export const PlanEvent = { Snapshot: 'plan_snapshot', Announced: 'plan_announced', StepUpdate: 'plan_step_update' } as const
export function isPlanEvent(type: unknown): boolean {
  return Object.values(PlanEvent).includes(type as typeof PlanEvent[keyof typeof PlanEvent])
}

export function planFromSnapshot(snapshot: PlanSnapshot): PlanMessage {
  return {
    type: 'plan', snapshot, revision: snapshot.revision, goal: snapshot.goal,
    context: snapshot.document.context, whatNotToDo: snapshot.document.what_not_to_do,
    verification: snapshot.document.verification,
    steps: snapshot.steps.map(step => ({ ...step, files: step.files ?? [], status: step.status || 'pending', parallelGroup: step.parallel_group })),
  }
}

/** One authority for send, reconnect and history. Sequence orders runtime changes
 * within an identity; revisions alone cannot distinguish approval/status updates. */
export function upsertPlanSnapshot(messages: ChatMsg[], snapshot: PlanSnapshot): ChatMsg[] {
  if (!snapshot?.plan_id || !Number.isSafeInteger(snapshot.sequence) || !Array.isArray(snapshot.steps) || !snapshot.document) return messages
  const index = messages.findIndex(m => m.type === 'plan' && m.snapshot?.plan_id === snapshot.plan_id)
  if (index < 0) return [...messages, planFromSnapshot(snapshot)]
  const current = messages[index] as PlanMessage
  if (snapshot.sequence <= current.snapshot!.sequence) return messages
  return messages.map((message, i) => i === index ? { ...planFromSnapshot(snapshot), invocationId: message.invocationId } : message)
}

export function reducePlanProgress(messages: ChatMsg[], data: Record<string, unknown>): ChatMsg[] {
  if (data.event_type === PlanEvent.Snapshot) return upsertPlanSnapshot(messages, data.snapshot as PlanSnapshot)
  // Legacy unversioned events remain readable but cannot overwrite an authority.
  if (messages.some(m => m.type === 'plan' && m.snapshot)) return messages
  if (data.event_type === PlanEvent.Announced) {
    const steps = (data.plan_steps as (PlanStepDefinition & { status?: PlanStepInfo['status'] })[] | undefined) ?? []
    return [...messages, {
      type: 'plan', goal: String(data.plan_goal || ''),
      context: data.plan_context as string, whatNotToDo: data.plan_what_not_to_do as string, verification: data.plan_verification as string,
      steps: steps.map(step => ({ ...step, files: step.files ?? [], status: step.status || 'pending', parallelGroup: step.parallel_group })),
    }]
  }
  if (data.event_type === PlanEvent.StepUpdate) {
    const index = messages.findLastIndex(m => m.type === 'plan')
    const plan = messages[index] as PlanMessage | undefined
    const status = data.step_status as PlanStepInfo['status']
    if (!plan || !['pending', 'running', 'complete', 'failed'].includes(status)) return messages
    return messages.map((m, i) => i === index ? { ...plan, steps: plan.steps.map(step => step.name === data.step_name ? { ...step, status } : step) } : m)
  }
  return messages
}

export function normalizePlanHistory(messages: ChatMsg[]): ChatMsg[] {
  return messages.reduce<ChatMsg[]>((result, message) => {
    if (message.type === 'plan' && message.snapshot) return upsertPlanSnapshot(result, message.snapshot)
    return [...result, message]
  }, [])
}
