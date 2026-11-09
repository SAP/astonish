import { useCallback, useEffect, useState } from 'react'

export type WorkspaceResource<T> = { data?: T; error?: string; loading: boolean; retry: () => void }

/** Identity and refresh are deliberately separate: old identities are never exposed,
 * even during the render before effect cleanup. Same-identity failures keep edits mounted. */
export function useWorkspaceResource<T>(load: (signal: AbortSignal) => Promise<T>, key: string, revision = 0): WorkspaceResource<T> {
  const [attempt, setAttempt] = useState(0)
  const retry = useCallback(() => setAttempt(value => value + 1), [])
  const [state, setState] = useState<{ key: string; data?: T; error?: string; loading: boolean }>({ key, loading: true })
  useEffect(() => {
    const controller = new AbortController()
    setState(previous => ({ key, data: previous.key === key ? previous.data : undefined, loading: true }))
    Promise.resolve().then(() => load(controller.signal)).then(data => {
      if (!controller.signal.aborted) setState({ key, data, loading: false })
    }).catch(error => {
      if (!controller.signal.aborted) setState(previous => ({ key, data: previous.key === key ? previous.data : undefined, loading: false, error: error instanceof Error ? error.message : String(error) }))
    })
    return () => controller.abort()
  }, [key, revision, attempt]) // eslint-disable-line react-hooks/exhaustive-deps
  return { ...(state.key === key ? state : { loading: true }), retry }
}
