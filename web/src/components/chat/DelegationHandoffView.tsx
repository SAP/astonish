import type { DelegationHandoff } from '../../api/delegationHandoff'

function Field({ label, value }: { label: string; value?: string | number }) {
  return <div><dt className="font-medium text-muted-foreground">{label}</dt><dd className="whitespace-pre-wrap break-words">{value === undefined || value === '' ? 'Not recorded' : value}</dd></div>
}

/** Captured definitions only, rendered as inert text (no remote images or prompt execution). */
export default function DelegationHandoffView({ handoff }: { handoff: DelegationHandoff | null }) {
  if (!handoff) return <p role="status">Handoff unavailable. No supported captured record exists for this attempt.</p>
  const { plan } = handoff
  return <dl className="space-y-3 text-xs text-foreground">
    <Field label="Original task" value={handoff.original_task} />
    <Field label="Effective task" value={handoff.effective_task} />
    <Field label="Original instructions" value={handoff.original_instructions} />
    <Field label="Effective instructions" value={handoff.effective_instructions} />
    <Field label="Parent session" value={handoff.parent_session_id} />
    <Field label="Child session" value={handoff.child_session_id} />
    <Field label="Batch" value={handoff.batch_id} />
    <Field label="Task ID" value={handoff.task_id} />
    <Field label="Attempt" value={handoff.attempt} />
    <Field label="Working directory" value={handoff.workdir} />
    <Field label="Preferred tools" value={handoff.preferred_tools?.join(', ') || 'None requested'} />
    <Field label="Initially available tools" value={handoff.initial_tool_names?.join(', ') || 'None recorded'} />
    <Field label="Plan context status" value={plan.status} />
    <Field label="Plan source mirror (captured label)" value={plan.source_mirror} />
    <Field label="Plan step" value={handoff.plan_step} />
    {plan.status === 'matched' && <>
      <Field label="Plan ID" value={plan.plan_id} />
      <Field label="Captured revision" value={plan.revision} />
      <Field label="Captured sequence" value={plan.sequence} />
      <Field label="Goal" value={plan.goal} />
      <Field label="Plan context" value={plan.document?.context} />
      <Field label="Boundaries / what not to do" value={plan.document?.what_not_to_do} />
      <Field label="Acceptance verification" value={plan.document?.verification} />
      <Field label="Phase description" value={plan.step?.description} />
      <Field label="Phase summary" value={plan.step?.summary} />
      <Field label="Phase details" value={plan.step?.details} />
      <Field label="Phase files" value={plan.step?.files?.map(f => `${f.kind}: ${f.path}`).join('\n')} />
      <Field label="Phase outcome" value={plan.step?.outcome} />
      <Field label="Phase verify command" value={plan.step?.verify} />
      <Field label="Phase verify kind" value={plan.step?.verify_kind} />
      <Field label="Parallel group" value={plan.step?.parallel_group} />
    </>}
  </dl>
}
