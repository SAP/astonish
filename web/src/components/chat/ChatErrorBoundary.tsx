import * as React from 'react'

interface ChatErrorBoundaryProps {
  /** Rendered when a child throws during render/commit. */
  fallback?: React.ReactNode
  children: React.ReactNode
}

interface ChatErrorBoundaryState {
  hasError: boolean
}

/**
 * ChatErrorBoundary isolates a single chat message (or any subtree) so that a
 * render-time exception in one card degrades to a small inline notice instead
 * of unmounting the entire Studio SPA. React has no hook equivalent for error
 * boundaries, so this is a class component by necessity.
 *
 * Wrap individual message renderers (e.g. the interactive question card, which
 * mounts the slides runtime) so a malformed payload or a runtime throw can
 * never take down the whole chat view.
 */
export default class ChatErrorBoundary extends React.Component<
  ChatErrorBoundaryProps,
  ChatErrorBoundaryState
> {
  constructor(props: ChatErrorBoundaryProps) {
    super(props)
    this.state = { hasError: false }
  }

  static getDerivedStateFromError(): ChatErrorBoundaryState {
    return { hasError: true }
  }

  componentDidCatch(error: unknown, info: unknown) {
    // Keep a console breadcrumb for debugging; never rethrow.
    console.error('ChatErrorBoundary caught a render error', error, info)
  }

  render() {
    if (this.state.hasError) {
      return (
        this.props.fallback ?? (
          <div className="my-2 rounded-md border border-border bg-card p-3 text-xs text-muted-foreground">
            This message could not be displayed.
          </div>
        )
      )
    }
    return this.props.children
  }
}
