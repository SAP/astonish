import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { loginWithOAuth, persistSession, studioFetch } from '../auth';

type Store = Record<string, unknown>;

function installChromeMock(callbackURL?: string) {
  const local: Store = {};
  const session: Store = {};

  const area = (store: Store) => ({
    get: async (key: string | string[] | Record<string, unknown>) => {
      if (typeof key === 'string') return { [key]: store[key] };
      if (Array.isArray(key)) return Object.fromEntries(key.map(k => [k, store[k]]));
      return { ...key, ...store };
    },
    set: async (value: Record<string, unknown>) => Object.assign(store, value),
    remove: async (key: string | string[]) => {
      for (const k of Array.isArray(key) ? key : [key]) delete store[k];
    },
  });

  (globalThis as unknown as { chrome: unknown }).chrome = {
    storage: { local: area(local), session: area(session) },
    permissions: { request: vi.fn(async () => true) },
    identity: {
      getRedirectURL: vi.fn(() => 'https://oeiocpkpaiphjhoapdlihnbdpimipelg.chromiumapp.org/oauth2'),
      launchWebAuthFlow: vi.fn(async ({ url }: { url: string }) => {
        if (callbackURL) return callbackURL;
        const state = new URL(url).searchParams.get('state');
        return `https://oeiocpkpaiphjhoapdlihnbdpimipelg.chromiumapp.org/oauth2?code=code-123&state=${state}`;
      }),
    },
  };
}

describe('auth', () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => installChromeMock());
  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  it('runs authorization code with PKCE and exchanges the verified callback code', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ access_token: 'access-oauth', refresh_token: 'refresh-oauth', expires_in: 3600 }),
    });

    const statuses: string[] = [];
    const session = await loginWithOAuth('http://localhost:9393/', status => statuses.push(status));

    expect(session.accessToken).toBe('access-oauth');
    expect(session.serverUrl).toBe('http://localhost:9393');
    expect(statuses).toEqual(['opening_browser', 'exchanging_code']);
    expect(chrome.identity.launchWebAuthFlow).toHaveBeenCalledTimes(1);

    const authorizeURL = new URL((chrome.identity.launchWebAuthFlow as ReturnType<typeof vi.fn>).mock.calls[0][0].url);
    expect(authorizeURL.origin).toBe('http://localhost:9393');
    expect(authorizeURL.pathname).toBe('/oauth/authorize');
    expect(authorizeURL.searchParams.get('client_id')).toBe('astonish-chrome-extension');
    expect(authorizeURL.searchParams.get('redirect_uri')).toBe('https://oeiocpkpaiphjhoapdlihnbdpimipelg.chromiumapp.org/oauth2');
    expect(authorizeURL.searchParams.get('scope')).toBe('chat tool:execute offline_access');
    expect(authorizeURL.searchParams.get('code_challenge_method')).toBe('S256');
    expect(authorizeURL.searchParams.get('code_challenge')).toBeTruthy();

    const [tokenURL, tokenInit] = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0];
    expect(tokenURL).toBe('http://localhost:9393/oauth/token');
    expect(tokenInit.headers).toEqual({ 'Content-Type': 'application/x-www-form-urlencoded' });
    const body = new URLSearchParams(tokenInit.body);
    expect(body.get('grant_type')).toBe('authorization_code');
    expect(body.get('client_id')).toBe('astonish-chrome-extension');
    expect(body.get('code')).toBe('code-123');
    expect(body.get('redirect_uri')).toBe('https://oeiocpkpaiphjhoapdlihnbdpimipelg.chromiumapp.org/oauth2');
    expect(body.get('code_verifier')).toBeTruthy();
  });

  it('rejects a callback with a mismatched state before token exchange', async () => {
    installChromeMock('https://oeiocpkpaiphjhoapdlihnbdpimipelg.chromiumapp.org/oauth2?code=code-123&state=wrong');
    globalThis.fetch = vi.fn();

    await expect(loginWithOAuth('http://localhost:9393')).rejects.toThrow('state did not match');
    expect(globalThis.fetch).not.toHaveBeenCalled();
  });

  it('studioFetch sets the scoped OAuth bearer without a team override header', async () => {
    await persistSession({ serverUrl: 'http://localhost:9393', accessToken: 'tok-abc', refreshToken: '', teamSlug: 'platform', expiresAt: Date.now() + 3600_000 });
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => ({}) });

    await studioFetch('/api/studio/sessions', { method: 'GET' });

    const [url, init] = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0];
    expect(url).toBe('http://localhost:9393/api/studio/sessions');
    const headers = new Headers(init.headers);
    expect(headers.get('Authorization')).toBe('Bearer tok-abc');
    expect(headers.get('X-Astonish-Team')).toBeNull();
    expect(headers.get('Content-Type')).toBe('application/json');
  });

  it('refreshes through the OAuth token endpoint and retries once', async () => {
    await persistSession({ serverUrl: 'http://localhost:9393', accessToken: 'old-access', refreshToken: 'refresh-keep', teamSlug: 'eng', expiresAt: Date.now() + 3600_000 });
    globalThis.fetch = vi
      .fn()
      .mockResolvedValueOnce({ ok: false, status: 401, json: async () => ({ error: 'expired' }) })
      .mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ access_token: 'new-access', refresh_token: 'new-refresh', expires_in: 3600 }) })
      .mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ ok: true }) });

    await studioFetch('/api/studio/chat', { method: 'POST', body: JSON.stringify({ message: 'hello' }) });

    const calls = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls;
    expect(calls).toHaveLength(3);
    expect(calls[1][0]).toBe('http://localhost:9393/oauth/token');
    const body = new URLSearchParams(calls[1][1].body);
    expect(body.get('grant_type')).toBe('refresh_token');
    expect(body.get('client_id')).toBe('astonish-chrome-extension');
    expect(body.get('refresh_token')).toBe('refresh-keep');
    expect(new Headers(calls[2][1].headers).get('Authorization')).toBe('Bearer new-access');
  });
});
