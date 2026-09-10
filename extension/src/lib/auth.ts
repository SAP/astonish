export type AuthSession = {
  serverUrl: string;
  accessToken: string;
  refreshToken: string;
  teamSlug: string;
  expiresAt: number;
};

export type SSOProvider = {
  id: string;
  name: string;
};

export type SSOStatus = 'opening_browser' | 'browser_failed' | 'polling';

const LOCAL_KEY = 'astonishAuth';

export function normalizeServerUrl(serverUrl: string): string {
  return serverUrl.trim().replace(/\/+$/, '');
}

function originPattern(serverUrl: string): string {
  return `${new URL(normalizeServerUrl(serverUrl)).origin}/*`;
}

async function ensureHostPermission(serverUrl: string): Promise<void> {
  if (typeof chrome === 'undefined' || !chrome.permissions?.request) {
    return;
  }
  const origin = originPattern(serverUrl);
  const granted = await chrome.permissions.request({ origins: [origin] });
  if (!granted) {
    throw new Error(`Host permission was not granted for ${origin}`);
  }
}

async function storageSet(
  area: 'local' | 'session',
  value: Record<string, unknown>,
): Promise<void> {
  if (typeof chrome === 'undefined' || !chrome.storage?.[area]) {
    throw new Error('chrome.storage is not available');
  }
  await chrome.storage[area].set(value);
}

async function storageGet<T>(area: 'local' | 'session', key: string): Promise<T | undefined> {
  if (typeof chrome === 'undefined' || !chrome.storage?.[area]) {
    return undefined;
  }
  const result = await chrome.storage[area].get(key);
  return result[key] as T | undefined;
}

export async function persistSession(session: AuthSession): Promise<void> {
  await storageSet('local', { [LOCAL_KEY]: session });
  await storageSet('session', {
    accessToken: session.accessToken,
    serverUrl: session.serverUrl,
    teamSlug: session.teamSlug,
  });
}

export async function loadSession(): Promise<AuthSession | null> {
  const session = await storageGet<AuthSession>('local', LOCAL_KEY);
  if (!session?.serverUrl || !session.accessToken) {
    return null;
  }
  return session;
}

export async function clearSession(): Promise<void> {
  if (typeof chrome === 'undefined' || !chrome.storage) {
    return;
  }
  await chrome.storage.local.remove(LOCAL_KEY);
  await chrome.storage.session.remove(['accessToken', 'serverUrl', 'teamSlug']);
}

type LoginResponse = {
  access_token?: string;
  refresh_token?: string;
  expires_in?: number;
  team?: string;
  error?: string;
  message?: string;
};

function apiError(data: LoginResponse, fallback: string): Error {
  return new Error(data.message || data.error || fallback);
}

function sessionFromLogin(
  serverUrl: string,
  data: LoginResponse,
  fallbackTeam = '',
): AuthSession {
  if (!data.access_token) {
    throw new Error('server did not return tokens (ensure server version supports CLI login)');
  }
  const expiresIn = typeof data.expires_in === 'number' ? data.expires_in : 3600;
  return {
    serverUrl: normalizeServerUrl(serverUrl),
    accessToken: data.access_token,
    refreshToken: data.refresh_token ?? '',
    teamSlug: data.team || fallbackTeam,
    expiresAt: Date.now() + expiresIn * 1000,
  };
}

export async function listSSOProviders(serverUrl: string): Promise<SSOProvider[]> {
  const normalized = normalizeServerUrl(serverUrl);
  await ensureHostPermission(normalized);
  const response = await fetch(`${normalized}/api/auth/sso/providers`);
  if (!response.ok) {
    const data = (await response.json().catch(() => ({}))) as LoginResponse;
    throw apiError(data, `could not list SSO providers (${response.status})`);
  }
  const data = (await response.json().catch(() => ({}))) as { providers?: SSOProvider[] };
  return Array.isArray(data.providers) ? data.providers : [];
}

type SSOInitResponse = {
  device_code?: string;
  verify_url?: string;
  expires_in?: number;
  interval?: number;
  error?: string;
  message?: string;
};

type SSOPollResponse = LoginResponse & {
  status?: string;
};

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, ms);
  });
}

async function openVerifyUrl(url: string): Promise<boolean> {
  if (typeof chrome !== 'undefined' && chrome.tabs?.create) {
    try {
      await chrome.tabs.create({ url });
      return true;
    } catch {
      return false;
    }
  }
  if (typeof window !== 'undefined') {
    return window.open(url, '_blank') !== null;
  }
  return false;
}

export async function loginWithSSO(
  serverUrl: string,
  providerID = '',
  onStatus?: (status: SSOStatus, verifyUrl?: string) => void,
): Promise<AuthSession> {
  const normalized = normalizeServerUrl(serverUrl);
  await ensureHostPermission(normalized);

  const initBody: Record<string, string> = {};
  if (providerID) {
    initBody.provider_id = providerID;
  }

  const initResponse = await fetch(`${normalized}/api/auth/sso/init`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(initBody),
  });
  const initData = (await initResponse.json().catch(() => ({}))) as SSOInitResponse;
  if (!initResponse.ok || !initData.device_code || !initData.verify_url) {
    throw apiError(initData, `SSO init failed (${initResponse.status})`);
  }

  onStatus?.('opening_browser', initData.verify_url);
  const opened = await openVerifyUrl(initData.verify_url);
  if (!opened) {
    onStatus?.('browser_failed', initData.verify_url);
  }

  const intervalMs = (typeof initData.interval === 'number' ? initData.interval : 2) * 1000;
  const timeoutMs = (initData.expires_in && initData.expires_in > 0 ? initData.expires_in : 600) * 1000;
  const deadline = Date.now() + timeoutMs;

  onStatus?.('polling', initData.verify_url);

  while (Date.now() < deadline) {
    if (intervalMs > 0) {
      await sleep(intervalMs);
    }
    let pollResponse: Response;
    try {
      pollResponse = await fetch(`${normalized}/api/auth/sso/poll`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ device_code: initData.device_code }),
      });
    } catch {
      continue;
    }
    const pollData = (await pollResponse.json().catch(() => ({}))) as SSOPollResponse;
    if (pollData.status === 'pending') {
      continue;
    }
    if (pollData.status === 'failed') {
      throw apiError(pollData, 'SSO login failed');
    }
    if (pollData.status === 'complete') {
      const session = sessionFromLogin(normalized, pollData);
      await persistSession(session);
      return session;
    }
  }

  throw new Error('SSO login timed out. Please try again');
}

async function refreshTokens(session: AuthSession): Promise<AuthSession> {
  if (!session.refreshToken) {
    throw new Error('no refresh token available');
  }
  const response = await fetch(`${session.serverUrl}/api/auth/refresh`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ refresh_token: session.refreshToken }),
  });
  const data = (await response.json().catch(() => ({}))) as LoginResponse;
  if (!response.ok || !data.access_token) {
    throw apiError(data, `refresh failed (${response.status})`);
  }
  const next: AuthSession = {
    ...session,
    accessToken: data.access_token,
    refreshToken: data.refresh_token || session.refreshToken,
    expiresAt: Date.now() + (data.expires_in ?? 3600) * 1000,
  };
  await persistSession(next);
  return next;
}

export async function studioFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const session = await loadSession();
  if (!session) {
    throw new Error('not signed in');
  }

  const headers = new Headers(init.headers);
  if (!headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json');
  }
  headers.set('Authorization', `Bearer ${session.accessToken}`);
  if (session.teamSlug) {
    headers.set('X-Astonish-Team', session.teamSlug);
  }

  const url = `${session.serverUrl}${path.startsWith('/') ? path : `/${path}`}`;
  const response = await fetch(url, { ...init, headers });
  if (response.status !== 401 || !session.refreshToken) {
    return response;
  }

  const refreshed = await refreshTokens(session);
  const retryHeaders = new Headers(init.headers);
  if (!retryHeaders.has('Content-Type')) {
    retryHeaders.set('Content-Type', 'application/json');
  }
  retryHeaders.set('Authorization', `Bearer ${refreshed.accessToken}`);
  if (refreshed.teamSlug) {
    retryHeaders.set('X-Astonish-Team', refreshed.teamSlug);
  }
  return fetch(url, { ...init, headers: retryHeaders });
}
