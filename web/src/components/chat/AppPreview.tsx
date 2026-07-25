import { useRef, useEffect, useCallback, useState } from 'react'
import { fetchAppData, fetchAppAction, fetchAppAI, fetchAppStateQuery, fetchAppStateExec } from '../../api/apps'
import { buildAppCanvasThemeMessage } from '../../themes/appCanvas'

// The sandbox HTML is served directly by the backend with its own permissive
// CSP (allowing inline scripts/eval). Origin isolation is enforced by the
// iframe sandbox="allow-scripts" attribute (no allow-same-origin), which gives
// the document an opaque origin — it cannot access the parent's DOM, cookies,
// localStorage, or make credentialed requests. The browser caches the response
// via Cache-Control: public, max-age=86400, immutable.
const SANDBOX_URL = '/api/app-preview/sandbox-full'

interface AppPreviewProps {
  code: string
  maxHeight?: number
  /** Optional app name — used for saved app data source lookups */
  appName?: string
  /** App identifier for persistent state (appName takes precedence, falls back to this) */
  stateId?: string
  /** Stretch the iframe to fill the parent instead of auto-sizing to content (capped by maxHeight). */
  fillHeight?: boolean
}

export default function AppPreview({ code, maxHeight = 500, appName = '', stateId = '', fillHeight = false }: AppPreviewProps) {
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const [height, setHeight] = useState(200)
  const [error, setError] = useState<string | null>(null)
  const [ready, setReady] = useState(false)
  const pendingCode = useRef<string | null>(null)
  // Track polling intervals for data_subscribe messages
  const pollingIntervals = useRef<Map<string, ReturnType<typeof setInterval>>>(new Map())

  const sendToIframe = useCallback((data: Record<string, unknown>) => {
    if (iframeRef.current?.contentWindow) {
      iframeRef.current.contentWindow.postMessage(data, '*')
    }
  }, [])

  const sendCode = useCallback((codeToSend: string) => {
    setError(null)
    sendToIframe({ type: 'render', code: codeToSend })
  }, [sendToIframe])

  /** Push active brand pack + light/dark App Canvas tokens into the sandbox. */
  const sendTheme = useCallback(() => {
    sendToIframe(buildAppCanvasThemeMessage())
  }, [sendToIframe])

  // Retheme when Studio light/dark (class) or brand pack (data-theme) changes on <html>
  useEffect(() => {
    if (!ready) return
    sendTheme()
    const root = document.documentElement
    const obs = new MutationObserver(() => sendTheme())
    obs.observe(root, { attributes: true, attributeFilter: ['class', 'data-theme'] })
    return () => obs.disconnect()
  }, [ready, sendTheme])

  // Clean up polling intervals on unmount
  useEffect(() => {
    const intervals = pollingIntervals.current
    return () => {
      intervals.forEach((id) => clearInterval(id))
      intervals.clear()
    }
  }, [])

  useEffect(() => {
    function handleMessage(e: MessageEvent) {
      if (e.source !== iframeRef.current?.contentWindow) return
      const msg = e.data
      if (!msg || !msg.type) return

      switch (msg.type) {
        case 'sandbox_ready':
          setReady(true)
          sendTheme()
          // Send app context for state operations
          {
            const effectiveAppName = appName || stateId
            if (effectiveAppName) {
              sendToIframe({ type: 'set_context', appName: effectiveAppName })
            }
          }
          if (pendingCode.current) {
            sendCode(pendingCode.current)
            pendingCode.current = null
          }
          break
        case 'render_success':
          setError(null)
          if (!fillHeight && msg.height && typeof msg.height === 'number') {
            setHeight(Math.min(msg.height + 2, maxHeight))
          }
          break
        case 'render_error':
          setError(msg.error || 'Unknown render error')
          break

        // ── Data proxy relay ──────────────────────────────────────
        case 'data_request':
          handleDataRequest(msg)
          break
        case 'action_request':
          handleActionRequest(msg)
          break
        case 'ai_request':
          handleAIRequest(msg)
          break
        case 'state_query':
          handleStateQuery(msg)
          break
        case 'state_exec':
          handleStateExec(msg)
          break
        case 'data_subscribe':
          handleDataSubscribe(msg)
          break
        case 'data_unsubscribe':
          handleDataUnsubscribe(msg)
          break
      }
    }

    // Relay data_request from iframe → backend → iframe
    async function handleDataRequest(msg: Record<string, unknown>) {
      const { sourceId, args, requestId } = msg as {
        sourceId: string
        args: Record<string, unknown>
        requestId: string
      }
      try {
        const resp = await fetchAppData(sourceId, args || {}, requestId, appName)
        sendToIframe({
          type: 'data_response',
          requestId: resp.requestId || requestId,
          data: resp.data,
          error: resp.error,
        })
      } catch (err: unknown) {
        sendToIframe({
          type: 'data_response',
          requestId,
          error: err instanceof Error ? err.message : 'Data request failed',
        })
      }
    }

    // Relay action_request from iframe → backend → iframe
    async function handleActionRequest(msg: Record<string, unknown>) {
      const { actionId, payload, requestId } = msg as {
        actionId: string
        payload: Record<string, unknown>
        requestId: string
      }
      try {
        const resp = await fetchAppAction(actionId, payload || {}, requestId)
        sendToIframe({
          type: 'action_response',
          requestId: resp.requestId || requestId,
          data: resp.data,
          error: resp.error,
        })
      } catch (err: unknown) {
        sendToIframe({
          type: 'action_response',
          requestId,
          error: err instanceof Error ? err.message : 'Action request failed',
        })
      }
    }

    // Relay ai_request from iframe → backend → iframe
    async function handleAIRequest(msg: Record<string, unknown>) {
      const { prompt, system, context, requestId } = msg as {
        prompt: string
        system: string
        context: unknown
        requestId: string
      }
      try {
        const resp = await fetchAppAI(prompt, system, context, requestId, appName)
        sendToIframe({
          type: 'ai_response',
          requestId: resp.requestId || requestId,
          text: resp.text,
          error: resp.error,
        })
      } catch (err: unknown) {
        sendToIframe({
          type: 'ai_response',
          requestId,
          error: err instanceof Error ? err.message : 'AI request failed',
        })
      }
    }

    // Relay state_query from iframe → backend → iframe
    async function handleStateQuery(msg: Record<string, unknown>) {
      const { appName: reqAppName, sql, params, requestId } = msg as {
        appName: string
        sql: string
        params: unknown[]
        requestId: string
      }
      const effectiveName = reqAppName || appName || stateId
      if (!effectiveName) {
        sendToIframe({
          type: 'state_response',
          requestId,
          error: 'App name is required for state operations. Save the app first or provide a stateId.',
        })
        return
      }
      try {
        const resp = await fetchAppStateQuery(effectiveName, sql, params, requestId)
        sendToIframe({
          type: 'state_response',
          requestId: resp.requestId || requestId,
          data: resp.data,
          error: resp.error,
        })
      } catch (err: unknown) {
        sendToIframe({
          type: 'state_response',
          requestId,
          error: err instanceof Error ? err.message : 'State query failed',
        })
      }
    }

    // Relay state_exec from iframe → backend → iframe
    async function handleStateExec(msg: Record<string, unknown>) {
      const { appName: reqAppName, sql, params, requestId } = msg as {
        appName: string
        sql: string
        params: unknown[]
        requestId: string
      }
      const effectiveName = reqAppName || appName || stateId
      if (!effectiveName) {
        sendToIframe({
          type: 'state_response',
          requestId,
          error: 'App name is required for state operations. Save the app first or provide a stateId.',
        })
        return
      }
      try {
        const resp = await fetchAppStateExec(effectiveName, sql, params, requestId)
        sendToIframe({
          type: 'state_response',
          requestId: resp.requestId || requestId,
          data: resp.data,
          error: resp.error,
        })
      } catch (err: unknown) {
        sendToIframe({
          type: 'state_response',
          requestId,
          error: err instanceof Error ? err.message : 'State exec failed',
        })
      }
    }

    // Set up polling for data_subscribe messages from iframe
    function handleDataSubscribe(msg: Record<string, unknown>) {
      const { sourceId, args, interval } = msg as {
        sourceId: string
        args: Record<string, unknown>
        interval: number
      }
      // Clear existing interval for this sourceId
      const existing = pollingIntervals.current.get(sourceId)
      if (existing) clearInterval(existing)

      // Minimum 5s interval
      const ms = Math.max(interval || 30000, 5000)

      const id = setInterval(async () => {
        try {
          const resp = await fetchAppData(sourceId, args || {}, '', appName)
          sendToIframe({
            type: 'data_update',
            sourceId,
            data: resp.data,
            error: resp.error,
          })
        } catch (err: unknown) {
          sendToIframe({
            type: 'data_update',
            sourceId,
            error: err instanceof Error ? err.message : 'Polling failed',
          })
        }
      }, ms)

      pollingIntervals.current.set(sourceId, id)
    }

    // Stop polling for a sourceId
    function handleDataUnsubscribe(msg: Record<string, unknown>) {
      const { sourceId } = msg as { sourceId: string }
      const existing = pollingIntervals.current.get(sourceId)
      if (existing) {
        clearInterval(existing)
        pollingIntervals.current.delete(sourceId)
      }
    }

    window.addEventListener('message', handleMessage)
    return () => window.removeEventListener('message', handleMessage)
  }, [maxHeight, fillHeight, sendCode, sendTheme, sendToIframe, appName, stateId])

  // Re-push App Canvas when brand pack changes (data-theme). Ignore light/dark class.
  useEffect(() => {
    const observer = new MutationObserver((mutations) => {
      for (const m of mutations) {
        if (m.attributeName === 'data-theme') {
          sendTheme()
          break
        }
      }
    })
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => observer.disconnect()
  }, [sendTheme])

  useEffect(() => {
    if (!code) return
    if (ready) {
      sendCode(code)
    } else {
      pendingCode.current = code
    }
  }, [code, ready, sendCode])

  return (
    <div className={fillHeight ? 'relative w-full h-full min-h-0' : 'relative w-full'}>
      <iframe
        ref={iframeRef}
        src={SANDBOX_URL}
        sandbox="allow-scripts allow-forms"
        style={{
          width: '100%',
          height: fillHeight ? '100%' : `${height}px`,
          border: 'none',
          borderRadius: '8px',
          overflow: 'hidden',
          background: 'var(--card)',
          transition: fillHeight ? undefined : 'height 0.2s ease',
        }}
        title="App Preview"
      />
      {error && (
        <div
          className="mt-2 px-3 py-2 rounded-lg text-xs font-mono"
          style={{
            background: 'rgba(239, 68, 68, 0.1)',
            border: '1px solid rgba(239, 68, 68, 0.3)',
            color: '#ef4444',
          }}
        >
          {error}
        </div>
      )}
    </div>
  )
}
