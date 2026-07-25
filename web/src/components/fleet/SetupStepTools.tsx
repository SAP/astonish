import type { SetupStep } from '../../api/fleetChat'

interface SetupStepToolsProps {
  step: SetupStep
}

export default function SetupStepTools({ step }: SetupStepToolsProps) {
  const tools = step.tools || []
  if (tools.length === 0) return null
  return (
    <div className="flex flex-wrap gap-1.5">
      {tools.map(tool => (
        <span
          key={tool}
          className="text-[10px] px-2 py-0.5 rounded-full font-mono"
          style={{ background: 'var(--brand-muted)', color: 'var(--brand)', border: '1px solid color-mix(in oklab, var(--brand) 25%, transparent)' }}
        >
          {tool}
        </span>
      ))}
    </div>
  )
}
