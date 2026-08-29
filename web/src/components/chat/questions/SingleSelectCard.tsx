import * as React from 'react'

import { cn } from '@/lib/utils'

import ThumbnailFrame from './ThumbnailFrame'

interface SingleSelectOption {
  id: string
  label: string
  description?: string
  thumbnail?: React.ReactNode
}

interface SingleSelectCardProps {
  prompt: string
  options: SingleSelectOption[]
  disabled?: boolean
  onSelect: (optionId: string, label: string) => void
}

/** Tiles keep the thumbnail grid. Text-only options use a stacked row list. */
export function selectCardLayout(
  options: Array<{ thumbnail?: unknown; description?: string }>,
): 'tiles' | 'rows' {
  if (options.some((option) => option.thumbnail)) return 'tiles'
  return 'rows'
}

function SingleSelectCard({
  prompt,
  options,
  disabled = false,
  onSelect,
}: SingleSelectCardProps) {
  const [focusedIndex, setFocusedIndex] = React.useState(0)
  const tileRefs = React.useRef<Array<HTMLDivElement | null>>([])
  const layout = selectCardLayout(options)

  const handleSelect = React.useCallback(
    (option: SingleSelectOption) => {
      if (disabled) return
      onSelect(option.id, option.label)
    },
    [disabled, onSelect]
  )

  const moveFocus = React.useCallback(
    (nextIndex: number) => {
      if (options.length === 0) return
      const clamped = (nextIndex + options.length) % options.length
      setFocusedIndex(clamped)
      tileRefs.current[clamped]?.focus()
    },
    [options.length]
  )

  const handleKeyDown = React.useCallback(
    (event: React.KeyboardEvent<HTMLDivElement>, index: number, option: SingleSelectOption) => {
      if (disabled) return
      switch (event.key) {
        case 'ArrowRight':
        case 'ArrowDown':
          event.preventDefault()
          moveFocus(index + 1)
          break
        case 'ArrowLeft':
        case 'ArrowUp':
          event.preventDefault()
          moveFocus(index - 1)
          break
        case 'Enter':
        case ' ':
          event.preventDefault()
          handleSelect(option)
          break
        default:
          break
      }
    },
    [disabled, handleSelect, moveFocus]
  )

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border bg-card p-4 text-card-foreground">
      <p className="text-sm font-medium">{prompt}</p>
      <div
        role="radiogroup"
        aria-label={prompt}
        data-testid="ask-user-options"
        data-layout={layout}
        className={
          layout === 'tiles'
            ? 'grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4'
            : 'flex flex-col gap-2'
        }
      >
        {options.map((option, index) => {
          const isFocusable = index === focusedIndex
          return (
            <div
              key={`${option.id}-${index}`}
              ref={(el) => {
                tileRefs.current[index] = el
              }}
              role="radio"
              aria-checked={false}
              aria-disabled={disabled || undefined}
              tabIndex={disabled ? -1 : isFocusable ? 0 : -1}
              onClick={() => handleSelect(option)}
              onFocus={() => setFocusedIndex(index)}
              onKeyDown={(event) => handleKeyDown(event, index, option)}
              className={cn(
                'flex flex-col text-left outline-none transition-colors',
                'focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px]',
                layout === 'tiles'
                  ? 'gap-2 rounded-md border border-border bg-background p-2'
                  : 'w-full gap-0.5 rounded-md border border-border bg-background px-3 py-2.5',
                disabled
                  ? 'pointer-events-none opacity-50'
                  : 'cursor-pointer hover:border-ring hover:bg-accent'
              )}
            >
              {option.thumbnail ? (
                <ThumbnailFrame>{option.thumbnail}</ThumbnailFrame>
              ) : null}
              <span className="text-sm font-medium">{option.label}</span>
              {option.description ? (
                <span className="text-xs text-muted-foreground">
                  {option.description}
                </span>
              ) : null}
            </div>
          )
        })}
      </div>
    </div>
  )
}

export default SingleSelectCard
