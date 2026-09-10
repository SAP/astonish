import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { listSSOProviders, loginWithSSO, persistSession, studioFetch } from '../auth';

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
      const out: Store = { ...key };
      for (const k of Object.keys(key)) {
        if (k in store) {
          out[k] = store[k];
        }
      }
      return out;
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
    tabs: {
      create: vi.fn(async () => ({ id: 1 })),
    },
  };
}

describe('auth', () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    installChromeMock();
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  it('listSSOProviders GETs /api/auth/sso/providers', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        providers: [{ id: 'okta', name: 'Okta' }],
      }),
    });

    const providers = await listSSOProviders('http://localhost:9393/');
    expect(providers).toEqual([{ id: 'okta', name: 'Okta' }]);
    expect(globalThis.fetch).toHaveBeenCalledWith('http://localhost:9393/api/auth/sso/providers');
  });

  it('loginWithSSO inits, opens verify URL, polls until complete', async () => {
    globalThis.fetch = vi
      .fn()
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          device_code: 'dev-1',
          verify_url: 'http://localhost:9393/api/auth/sso/verify/dev-1',
          expires_in: 600,
          interval: 0,
        }),
      })
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ status: 'pending' }),
      })
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          status: 'complete',
          access_token: 'access-sso',
          refresh_token: 'refresh-sso',
          expires_in: 3600,
          team: 'eng',
        }),
      });

    const statuses: string[] = [];
    const session = await loginWithSSO('http://localhost:9393/', 'okta', (status) => {
      statuses.push(status);
    });

    expect(session.accessToken).toBe('access-sso');
    expect(session.teamSlug).toBe('eng');
    expect(session.serverUrl).toBe('http://localhost:9393');
    expect(statuses).toEqual(['opening_browser', 'polling']);

    const calls = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls;
    expect(calls[0][0]).toBe('http://localhost:9393/api/auth/sso/init');
    expect(JSON.parse(calls[0][1].body as string)).toEqual({ provider_id: 'okta' });
    expect(calls[1][0]).toBe('http://localhost:9393/api/auth/sso/poll');
    expect(JSON.parse(calls[1][1].body as string)).toEqual({ device_code: 'dev-1' });
    expect(chrome.tabs.create).toHaveBeenCalledWith({
      url: 'http://localhost:9393/api/auth/sso/verify/dev-1',
    });
  });

  it('studioFetch sets Bearer and X-Astonish-Team', async () => {
    await persistSession({
      serverUrl: 'http://localhost:9393',
      accessToken: 'tok-abc',
      refreshToken: '',
      teamSlug: 'platform',
      expiresAt: Date.now() + 3600_000,
    });
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({}),
    });

    await studioFetch('/api/studio/sessions', { method: 'GET' });

    expect(globalThis.fetch).toHaveBeenCalledTimes(1);
    const [url, init] = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0];
    expect(url).toBe('http://localhost:9393/api/studio/sessions');
    const headers = new Headers(init.headers);
    expect(headers.get('Authorization')).toBe('Bearer tok-abc');
    expect(headers.get('X-Astonish-Team')).toBe('platform');
    expect(headers.get('Content-Type')).toBe('application/json');
  });

  it('401 triggers refresh with refresh_token body and retries once', async () => {
    await persistSession({
      serverUrl: 'http://localhost:9393',
      accessToken: 'old-access',
      refreshToken: 'refresh-keep',
      teamSlug: 'eng',
      expiresAt: Date.now() + 3600_000,
    });

    globalThis.fetch = vi
      .fn()
      .mockResolvedValueOnce({
        ok: false,
        status: 401,
        json: async () => ({ error: 'expired' }),
      })
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          access_token: 'new-access',
          refresh_token: 'new-refresh',
          expires_in: 3600,
        }),
      })
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ ok: true }),
      });

    const response = await studioFetch('/api/studio/chat', {
      method: 'POST',
      body: JSON.stringify({ message: 'hello' }),
    });
    expect(response.ok).toBe(true);

    const calls = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls;
    expect(calls).toHaveLength(3);
    expect(calls[0][0]).toBe('http://localhost:9393/api/studio/chat');
    expect(new Headers(calls[0][1].headers).get('Authorization')).toBe('Bearer old-access');

    expect(calls[1][0]).toBe('http://localhost:9393/api/auth/refresh');
    expect(JSON.parse(calls[1][1].body as string)).toEqual({ refresh_token: 'refresh-keep' });

    expect(calls[2][0]).toBe('http://localhost:9393/api/studio/chat');
    expect(new Headers(calls[2][1].headers).get('Authorization')).toBe('Bearer new-access');
    expect(new Headers(calls[2][1].headers).get('X-Astonish-Team')).toBe('eng');
  });
});
