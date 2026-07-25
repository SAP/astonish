import type { SetupStep } from '../../api/fleetChat'

interface SetupStepperProps {
  steps: SetupStep[]
  currentIndex: number
  collected: Record<string, Record<string, unknown>>
  stepActive: (step: SetupStep) => boolean
}

export default function SetupStepper({ steps, currentIndex, collected, stepActive }: SetupStepperProps) {
  return (
    <nav className="w-44 shrink-0 pr-4 border-r space-y-1" style={{ borderColor: 'var(--border-color)' }}>
      {steps.map((step, i) => {
        const active = stepActive(step)
        if (!active) return null
        const isCurrent = i === currentIndex
        const isComplete = i < currentIndex || Boolean(collected[step.id]?._ack) || (step.type !== 'info' && Object.keys(collected[step.id] || {}).length > 0 && i < currentIndex)
        return (
          <div
            key={step.id}
            className={`px-2 py-2 rounded-lg text-xs ${isCurrent ? 'bg-primary/15 border border-primary/30' : ''}`}
          >
            <div className="flex items-center gap-2">
              <span
                className={`w-5 h-5 rounded-full flex items-center justify-center text-[10px] font-mono shrink-0 ${
                  isComplete && !isCurrent ? 'bg-green-500/20 text-green-400' : isCurrent ? 'bg-primary/30 text-primary' : 'bg-white/5 text-gray-500'
                }`}
              >
                {isComplete && !isCurrent ? '✓' : i + 1}
              </span>
              <span style={{ color: isCurrent ? 'var(--brand)' : 'var(--text-secondary)' }} className="font-medium truncate">
                {step.title}
              </span>
            </div>
            {step.summary && (
              <p className="mt-0.5 pl-7 truncate" style={{ color: 'var(--text-muted)' }}>{step.summary}</p>
            )}
          </div>
        )
      })}
    </nav>
  )
}
