import { describe, it, expect, vi, afterEach } from 'vitest'
import {
  fetchSessions,
  fetchSessionHistory,
  deleteSession,
  connectChat,
  fetchCacheDiagnostics,
  stopChat,
} from '../../api/studioChat'

function mockFetch(data: unknown, ok = true, statusText = 'OK') {
  return vi.fn().mockResolvedValue({
    ok,
    statusText,
    json: () => Promise.resolve(data),
    text: () => Promise.resolve(typeof data === 'string' ? data : JSON.stringify(data)),
  })
}

describe('studioChat API', () => {
  const originalFetch = globalThis.fetch

  afterEach(() => {
    globalThis.fetch = originalFetch
  })

  describe('fetchSessions', () => {
    it('calls GET /api/studio/sessions', async () => {
      const sessions = [{ id: '1', title: 't', createdAt: '', updatedAt: '', messageCount: 0 }]
      globalThis.fetch = mockFetch(sessions)

      const result = await fetchSessions()
      expect(result).toEqual(sessions)
      expect(globalThis.fetch).toHaveBeenCalledWith('/api/studio/sessions', expect.objectContaining({ headers: expect.any(Headers) }))
    })

    it('throws on error', async () => {
      globalThis.fetch = mockFetch(null, false, 'Server Error')
      await expect(fetchSessions()).rejects.toThrow('Failed to fetch sessions')
    })
  })

  describe('fetchSessionHistory', () => {
    it('calls GET /api/studio/sessions/:id', async () => {
      const history = { id: '1', title: 't', messages: [] }
      globalThis.fetch = mockFetch(history)

      const result = await fetchSessionHistory('abc')
      expect(result).toEqual(history)
      expect(globalThis.fetch).toHaveBeenCalledWith('/api/studio/sessions/abc', expect.objectContaining({ headers: expect.any(Headers) }))
    })
  })

  describe('deleteSession', () => {
    it('calls DELETE /api/studio/sessions/:id', async () => {
      globalThis.fetch = vi.fn().mockResolvedValue({ ok: true })

      await deleteSession('abc')
      expect(globalThis.fetch).toHaveBeenCalledWith('/api/studio/sessions/abc', expect.objectContaining({ method: 'DELETE' }))
    })

    it('throws on error', async () => {
      globalThis.fetch = mockFetch(null, false, 'Not Found')
      await expect(deleteSession('x')).rejects.toThrow('Failed to delete session')
    })
  })

  describe('connectChat', () => {
    it('sends POST to /api/studio/chat and processes SSE events', async () => {
      const events: Array<{ type: string; data: Record<string, unknown> }> = []

      // Encode SSE blocks
      const sseResponse = [
        'event: session\ndata: {"id":"s1","isNew":true}\n\n',
        'event: text\ndata: {"text":"Hello"}\n\n',
        'event: done\ndata: {"status":"complete"}\n\n',
      ].join('')

      const encoder = new TextEncoder()
      const stream = new ReadableStream({
        start(controller) {
          controller.enqueue(encoder.encode(sseResponse))
          controller.close()
        },
      })

      globalThis.fetch = vi.fn().mockResolvedValue({
        ok: true,
        body: stream,
      })

      const onDone = vi.fn()
      const controller = connectChat({
        sessionId: 'sess1',
        message: 'hello',
        onEvent: (type, data) => events.push({ type, data }),
        onDone,
      })

      expect(controller).toBeInstanceOf(AbortController)

      // Wait for async processing
      await new Promise(resolve => setTimeout(resolve, 50))

      expect(events).toHaveLength(3)
      expect(events[0]).toEqual({ type: 'session', data: { id: 's1', isNew: true } })
      expect(events[1]).toEqual({ type: 'text', data: { text: 'Hello' } })
      expect(events[2]).toEqual({ type: 'done', data: { status: 'complete' } })
      expect(onDone).toHaveBeenCalled()

      expect(globalThis.fetch).toHaveBeenCalledWith('/api/studio/chat', expect.objectContaining({
        method: 'POST',
      }))
      const headers = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][1]?.headers as Headers
      expect(headers.get('Content-Type')).toBe('application/json')
    })

    it('handles chunked SSE data split across reads', async () => {
      const events: Array<{ type: string; data: Record<string, unknown> }> = []

      const encoder = new TextEncoder()
      const chunk1 = encoder.encode('event: text\nda')
      const chunk2 = encoder.encode('ta: {"text":"Hi"}\n\n')

      const stream = new ReadableStream({
        start(controller) {
          controller.enqueue(chunk1)
          controller.enqueue(chunk2)
          controller.close()
        },
      })

      globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, body: stream })

      connectChat({
        onEvent: (type, data) => events.push({ type, data }),
      })

      await new Promise(resolve => setTimeout(resolve, 50))

      expect(events).toHaveLength(1)
      expect(events[0]).toEqual({ type: 'text', data: { text: 'Hi' } })
    })

    it('calls onError for HTTP errors', async () => {
      globalThis.fetch = vi.fn().mockResolvedValue({
        ok: false,
        status: 500,
        text: () => Promise.resolve('Internal Server Error'),
      })

      const onError = vi.fn()
      connectChat({ onEvent: vi.fn(), onError })

      await new Promise(resolve => setTimeout(resolve, 50))

      expect(onError).toHaveBeenCalled()
      expect(onError.mock.calls[0][0]).toBeInstanceOf(Error)
      expect(onError.mock.calls[0][0].message).toBe('Internal Server Error')
    })

    it('calls onDone on abort', async () => {
      // Simulate fetch throwing AbortError when signal is aborted
      globalThis.fetch = vi.fn().mockImplementation((_url: string, opts: RequestInit) => {
        return new Promise((_resolve, reject) => {
          opts.signal?.addEventListener('abort', () => {
            const err = new Error('The operation was aborted.')
            err.name = 'AbortError'
            reject(err)
          })
        })
      })

      const onDone = vi.fn()
      const onError = vi.fn()
      const controller = connectChat({ onEvent: vi.fn(), onDone, onError })

      // Give run() time to reach the fetch call
      await new Promise(resolve => setTimeout(resolve, 20))
      controller.abort()

      // Give the abort handler time to fire
      await new Promise(resolve => setTimeout(resolve, 100))

      expect(onDone).toHaveBeenCalled()
      expect(onError).not.toHaveBeenCalled()
    })

    it('includes systemContext when provided', async () => {
      const stream = new ReadableStream({ start(c) { c.close() } })
      globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, body: stream })

      connectChat({
        sessionId: 's1',
        message: 'hi',
        systemContext: 'context here',
        autoApprove: true,
        onEvent: vi.fn(),
      })

      await new Promise(resolve => setTimeout(resolve, 50))

      const body = JSON.parse((globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][1].body)
      expect(body.systemContext).toBe('context here')
      expect(body.autoApprove).toBe(true)
    })

    it('includes debug only when enabled', async () => {
      const stream = new ReadableStream({ start(c) { c.close() } })
      globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, body: stream })

      connectChat({ message: 'inspect', debug: true, onEvent: vi.fn() })
      await new Promise(resolve => setTimeout(resolve, 50))

      const body = JSON.parse((globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][1].body)
      expect(body.debug).toBe(true)
    })

    it('includes planMode when enabled', async () => {
      const stream = new ReadableStream({ start(c) { c.close() } })
      globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, body: stream })

      connectChat({
        sessionId: 's1',
        message: 'hi',
        planMode: true,
        onEvent: vi.fn(),
      })

      await new Promise(resolve => setTimeout(resolve, 50))

      const body = JSON.parse((globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][1].body)
      expect(body.planMode).toBe(true)
    })

    it('omits planMode when disabled', async () => {
      const stream = new ReadableStream({ start(c) { c.close() } })
      globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, body: stream })

      connectChat({
        sessionId: 's1',
        message: 'hi',
        onEvent: vi.fn(),
      })

      await new Promise(resolve => setTimeout(resolve, 50))

      const body = JSON.parse((globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][1].body)
      expect(body.planMode).toBeUndefined()
    })
  })

  describe('fetchCacheDiagnostics', () => {
    it('fetches typed diagnostics for an encoded session and invocation', async () => {
      const diagnostics = { sessionId: 'session/a', invocationId: 'inv/2', rounds: [] }
      globalThis.fetch = mockFetch(diagnostics)

      await expect(fetchCacheDiagnostics('session/a', 'inv/2')).resolves.toEqual(diagnostics)
      expect(globalThis.fetch).toHaveBeenCalledWith(
        '/api/studio/sessions/session%2Fa/cache-diagnostics?invocationId=inv%2F2',
        expect.objectContaining({ headers: expect.any(Headers) }),
      )
    })

    it('surfaces the diagnostics API error body', async () => {
      globalThis.fetch = mockFetch('diagnostics unavailable', false, 'Forbidden')
      await expect(fetchCacheDiagnostics('s1', 'inv-1')).rejects.toThrow('diagnostics unavailable')
    })
  })

  describe('stopChat', () => {
    it('calls POST to stop endpoint', async () => {
      globalThis.fetch = vi.fn().mockResolvedValue({ ok: true })
      await stopChat('sess1')
      expect(globalThis.fetch).toHaveBeenCalledWith('/api/studio/sessions/sess1/stop', expect.objectContaining({ method: 'POST' }))
    })

    it('does not throw on failure', async () => {
      // Suppress expected console.warn from stopChat's catch block
      const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
      globalThis.fetch = vi.fn().mockRejectedValue(new Error('network error'))
      await expect(stopChat('x')).resolves.toBeUndefined()
      warnSpy.mockRestore()
    })
  })
})
