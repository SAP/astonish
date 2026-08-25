import * as React from 'react'

import { cn } from '@/lib/utils'

interface ThumbnailFrameProps {
  children: React.ReactNode
  className?: string
}

function ThumbnailFrame({ children, className }: ThumbnailFrameProps) {
  return (
    <div
      className={cn(
        'aspect-video w-full overflow-hidden rounded-md border border-border bg-muted',
        className
      )}
    >
      {children}
    </div>
  )
}

export default ThumbnailFrame
