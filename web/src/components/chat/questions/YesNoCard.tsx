import * as React from 'react'

import { Button } from '@/components/ui/button'

interface YesNoCardProps {
  prompt: string
  disabled?: boolean
  onAnswer: (yes: boolean) => void
}

function YesNoCard({ prompt, disabled = false, onAnswer }: YesNoCardProps) {
  const handleAnswer = React.useCallback(
    (yes: boolean) => {
      if (disabled) return
      onAnswer(yes)
    },
    [disabled, onAnswer]
  )

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border bg-card p-4 text-card-foreground">
      <p className="text-sm font-medium">{prompt}</p>
      <div className="flex gap-2">
        <Button
          type="button"
          variant="default"
          disabled={disabled}
          onClick={() => handleAnswer(true)}
        >
          Yes
        </Button>
        <Button
          type="button"
          variant="outline"
          disabled={disabled}
          onClick={() => handleAnswer(false)}
        >
          No
        </Button>
      </div>
    </div>
  )
}

export default YesNoCard
