import { describe, it, expect, vi, afterEach } from 'vitest'
import {
  fetchSubtaskEvents,
  fetchArtifactContent,
  fetchArtifactBlob,
  fetchArtifactPDFBlob,
  getArtifactDownloadUrl,
  getArtifactPDFUrl,
  fetchSessions,
  fetchSessionHistory,
  deleteSession,
  connectChat,
  fetchCacheDiagnostics,
  fetchKnowledgeDebug,
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

  describe('subtask details', () => {
    it('sends precise identity and preserves a successful empty response', async () => {
      globalThis.fetch = mockFetch({ events: [] })
      expect(await fetchSubtaskEvents('s/1', { batch_id: 'b', task_id: 't', child_session_id: 'c', attempt: 2 })).toEqual([])
      expect(globalThis.fetch).toHaveBeenCalledWith(expect.stringContaining('batch_id=b&task_id=t&child_session_id=c&attempt=2'), expect.anything())
    })
    it.each([403, 404, 409, 500])('does not hide HTTP %s as empty detail', async status => {
      globalThis.fetch = vi.fn().mockResolvedValue({ ok: false, status })
      await expect(fetchSubtaskEvents('s', 'same')).rejects.toMatchObject({ status })
    })
  })

  describe('artifact session compatibility', () => {
    it.each(['live-session', 'restored/session'])('preserves session context %s', async sessionId => {
      globalThis.fetch = mockFetch('content')
      expect(await fetchArtifactContent('/tmp/report.md', sessionId)).toBe('content')
      expect(globalThis.fetch).toHaveBeenCalledWith(expect.stringContaining(`session=${encodeURIComponent(sessionId)}`), expect.anything())
      expect(getArtifactDownloadUrl('/tmp/report.md', sessionId)).toContain(`session=${encodeURIComponent(sessionId)}`)
      expect(getArtifactPDFUrl('/tmp/report.md', sessionId)).toContain(`session=${encodeURIComponent(sessionId)}`)
    })
    it('does not turn denied artifact content or media into a successful preview', async () => {
      globalThis.fetch = vi.fn().mockResolvedValue({ok:false, status:404})
      await expect(fetchArtifactContent('/tmp/report.md', 'foreign')).rejects.toThrow('404')
      await expect(fetchArtifactBlob('/tmp/movie.mp4', 'foreign')).rejects.toThrow('404')
      await expect(fetchArtifactPDFBlob('/tmp/report.md', 'foreign')).rejects.toThrow('404')
    })
    it.each(['live-session', 'restored/session', undefined])('fetches PDF bytes using team transport for %s', async sessionId => {
      const blob = new Blob(['pdf fixture'], { type: 'application/pdf' })
      globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, blob: async () => blob })
      expect(await fetchArtifactPDFBlob('/tmp/report.md', sessionId)).toBe(blob)
      expect(globalThis.fetch).toHaveBeenCalledWith(getArtifactPDFUrl('/tmp/report.md', sessionId), expect.objectContaining({ headers: expect.any(Headers) }))
      const headers = vi.mocked(globalThis.fetch).mock.calls[0][1]?.headers as Headers
      expect(headers.get('X-Requested-With')).toBeTruthy()
    })
  })

  describe('fetchKnowledgeDebug', () => {
    it('encodes session and invocation, preserves capture text and uses team transport headers', async () => {
      const data = {
        sessionId: 's/1', invocationId: 'inv &/?', tools: null,
        knowledge: {
          version: 1, invocation_id: 'inv &/?', dispatched: true, capture_status: 'captured',
          type: 'knowledge', query: '', bm25_query_len: 0, results: [], result_count: 1, estimated_tokens: 10,
          snapshot: { text: '<memory>\n  exact & literal\n</memory>', snippets: null, capture_bytes: 39, original_bytes: 39, truncated: false, redacted: false },
        },
      }
      globalThis.fetch = mockFetch(data)
      expect(await fetchKnowledgeDebug('s/1', 'inv &/?')).toEqual(data)
      expect(globalThis.fetch).toHaveBeenCalledWith('/api/studio/sessions/s%2F1/knowledge-debug?invocationId=inv+%26%2F%3F', expect.objectContaining({ headers: expect.any(Headers) }))
      const headers = vi.mocked(globalThis.fetch).mock.calls[0][1]?.headers as Headers
      expect(headers.get('X-Requested-With')).toBeTruthy()
    })

    it('accepts legacy metadata without inventing captured text', async () => {
      const legacy = { sessionId: 's1', invocationId: 'old', tools: null, knowledge: { type: 'none', query: '', bm25_query_len: 0, results: [], result_count: 0, estimated_tokens: 0 } }
      globalThis.fetch = mockFetch(legacy)
      const result = await fetchKnowledgeDebug('s1', 'old')
      expect(result).toEqual(legacy)
      expect(result.knowledge?.snapshot).toBeUndefined()
    })

    it.each([
      ['Forbidden', 'Forbidden'],
      ['', 'Failed to fetch knowledge debug: Server Error'],
    ])('surfaces non-OK response %j', async (body, message) => {
      globalThis.fetch = mockFetch(body, false, 'Server Error')
      await expect(fetchKnowledgeDebug('s1', 'inv1')).rejects.toThrow(message)
    })

    it('propagates network failures', async () => {
      globalThis.fetch = vi.fn().mockRejectedValue(new Error('Network unavailable'))
      await expect(fetchKnowledgeDebug('s1', 'inv1')).rejects.toThrow('Network unavailable')
    })
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