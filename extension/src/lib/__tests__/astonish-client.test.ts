import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { persistSession } from '../auth';
import {
  connectChat,
  fetchSessionHistory,
  fetchSessions,
  historyMessageKind,
  sessionIdFromEvent,
} from '../astonish-client';

type Store = Record<string, unknown>;

function installChromeMock() {
  const local: Store = {};
  const session: Store = {};

  const area = (store: Store) => ({
    get: async (key: string | string[] | Record<string, unknown>) => {
      if (typeof key === 'string') {
        return { [key]: store[key] };
      }
      if (Array.isArray(key)) {
        const out: Store = {};
        for (const k of key) {
          out[k] = store[k];
        }
        return out;
      }
      return { ...key };
    },
    set: async (value: Record<string, unknown>) => {
      Object.assign(store, value);
    },
    remove: async (key: string | string[]) => {
      for (const k of Array.isArray(key) ? key : [key]) {
        delete store[k];
      }
    },
  });

  (globalThis as unknown as { chrome: unknown }).chrome = {
    storage: {
      local: area(local),
      session: area(session),
    },
    permissions: {
      request: vi.fn(async () => true),
    },
  };
}

describe('astonish-client', () => {
  const originalFetch = globalThis.fetch;

  beforeEach(async () => {
    installChromeMock();
    await persistSession({
      serverUrl: 'http://localhost:9393',
      accessToken: 'tok',
      refreshToken: '',
      teamSlug: 'eng',
      expiresAt: Date.now() + 3600_000,
    });
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  it('connectChat parses session/text/done events', async () => {
    const events: Array<{ type: string; data: Record<string, unknown> }> = [];
    const sseResponse = [
      'event: session\ndata: {"sessionId":"s1","isNew":true}\n\n',
      'event: session_title\ndata: {"title":"Hello chat","sessionId":"s1"}\n\n',
      'event: text\ndata: {"text":"Hello"}\n\n',
      'event: ignored\ndata: {"nope":true}\n\n',
      'event: done\ndata: {"status":"complete"}\n\n',
    ].join('');

    const encoder = new TextEncoder();
    const stream = new ReadableStream({
      start(controller) {
        controller.enqueue(encoder.encode(sseResponse));
        controller.close();
      },
    });

    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      body: stream,
    });

    const onDone = vi.fn();
    connectChat({
      sessionId: '',
      message: 'hello',
      systemContext: '## Browser page context\nURL: https://example.com\nTitle: Example',
      onEvent: (type, data) => events.push({ type, data }),
      onDone,
    });

    await vi.waitFor(() => {
      expect(onDone).toHaveBeenCalled();
    });

    expect(events).toEqual([
      { type: 'session', data: { sessionId: 's1', isNew: true } },
      { type: 'session_title', data: { title: 'Hello chat', sessionId: 's1' } },
      { type: 'text', data: { text: 'Hello' } },
      { type: 'done', data: { status: 'complete' } },
    ]);
    expect(sessionIdFromEvent(events[0].data)).toBe('s1');

    const [url, init] = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0];
    expect(url).toBe('http://localhost:9393/api/studio/chat');
    expect(init.method).toBe('POST');
    const payload = JSON.parse(init.body as string) as {
      sessionId: string;
      message: string;
      autoApprove: boolean;
      systemContext: string;
    };
    expect(payload).toEqual({
      sessionId: '',
      message: 'hello',
      autoApprove: false,
      systemContext: '## Browser page context\nURL: https://example.com\nTitle: Example',
    });
    expect(payload.systemContext).toContain('URL:');
    expect(payload.systemContext).toContain('Title:');
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer tok');
  });

  it('sessionIdFromEvent prefers sessionId and falls back to id', () => {
    expect(sessionIdFromEvent({ sessionId: 'live', id: 'fixture' })).toBe('live');
    expect(sessionIdFromEvent({ id: 'fixture' })).toBe('fixture');
    expect(sessionIdFromEvent({})).toBe('');
  });

  it('historyMessageKind prefers type and maps assistant role to agent', () => {
    expect(historyMessageKind({ type: 'user', role: 'assistant', content: 'x' })).toBe('user');
    expect(historyMessageKind({ role: 'assistant', content: 'hello' })).toBe('agent');
    expect(historyMessageKind({ role: 'user', content: 'hi' })).toBe('user');
    expect(historyMessageKind({})).toBe('');
  });

  it('fetchSessions lists Studio chats for the picker to filter', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => [
        { id: 'studio-1', title: 'Studio chat' },
        { id: 'ext-1', title: 'Extension chat' },
      ],
    });

    await expect(fetchSessions()).resolves.toEqual([
      { id: 'studio-1', title: 'Studio chat' },
      { id: 'ext-1', title: 'Extension chat' },
    ]);
    expect((globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0]).toBe(
      'http://localhost:9393/api/studio/sessions',
    );
  });

  it('fetchSessionHistory returns messages for restore', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        id: 'ext-1',
        title: 'GitHub issue',
        messages: [
          { type: 'user', content: 'hi' },
          { type: 'agent', content: 'hello' },
        ],
      }),
    });

    await expect(fetchSessionHistory('ext-1')).resolves.toEqual({
      id: 'ext-1',
      title: 'GitHub issue',
      messages: [
        { type: 'user', content: 'hi' },
        { type: 'agent', content: 'hello' },
      ],
    });
  });

  it('fetchSessionHistory keeps Studio role/content messages', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        id: 'ext-2',
        title: 'Role history',
        messages: [
          { role: 'user', content: 'hi' },
          { role: 'assistant', content: 'hello' },
          { content: 'skip me' },
        ],
      }),
    });

    await expect(fetchSessionHistory('ext-2')).resolves.toEqual({
      id: 'ext-2',
      title: 'Role history',
      messages: [
        { role: 'user', content: 'hi' },
        { role: 'assistant', content: 'hello' },
      ],
    });
  });

  it('connectChat reuses a stored sessionId on follow-up turns', async () => {
    const encoder = new TextEncoder();
    const stream = new ReadableStream({
      start(controller) {
        controller.enqueue(encoder.encode('event: done\ndata: {"status":"complete"}\n\n'));
        controller.close();
      },
    });
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      body: stream,
    });

    const onDone = vi.fn();
    connectChat({
      sessionId: 'ext-1',
      message: 'follow up',
      onEvent: () => {},
      onDone,
    });
    await vi.waitFor(() => {
      expect(onDone).toHaveBeenCalled();
    });

    const [, init] = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0];
    const payload = JSON.parse(init.body as string) as { sessionId: string; message: string };
    expect(payload.sessionId).toBe('ext-1');
    expect(payload.message).toBe('follow up');
  });
});
