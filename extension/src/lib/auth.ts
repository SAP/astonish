export type AuthSession = {
  serverUrl: string;
  accessToken: string;
  refreshToken: string;
  teamSlug: string;
  expiresAt: number;
};

export type OAuthStatus = 'opening_browser' | 'exchanging_code';

const LOCAL_KEY = 'astonishAuth';
const EXTENSION_CLIENT_ID = 'astonish-chrome-extension';
const EXTENSION_SCOPE = 'chat tool:execute offline_access';

export function normalizeServerUrl(serverUrl: string): string {
  return serverUrl.trim().replace(/\/+$/, '');
}

function originPattern(serverUrl: string): string {
  return `${new URL(normalizeServerUrl(serverUrl)).origin}/*`;
}

async function ensureHostPermission(serverUrl: string): Promise<void> {
  if (typeof chrome === 'undefined' || !chrome.permissions?.request) return;
  const origin = originPattern(serverUrl);
  if (!await chrome.permissions.request({ origins: [origin] })) {
    throw new Error(`Host permission was not granted for ${origin}`);
  }
}

async function storageSet(area: 'local' | 'session', value: Record<string, unknown>): Promise<void> {
  if (typeof chrome === 'undefined' || !chrome.storage?.[area]) {
    throw new Error('chrome.storage is not available');
  }
  await chrome.storage[area].set(value);
}

async function storageGet<T>(area: 'local' | 'session', key: string): Promise<T | undefined> {
  if (typeof chrome === 'undefined' || !chrome.storage?.[area]) return undefined;
  const result = await chrome.storage[area].get(key);
  return result[key] as T | undefined;
}

export async function persistSession(session: AuthSession): Promise<void> {
  await storageSet('local', { [LOCAL_KEY]: session });
  await storageSet('session', { accessToken: session.accessToken, serverUrl: session.serverUrl, teamSlug: session.teamSlug });
}

export async function loadSession(): Promise<AuthSession | null> {
  const session = await storageGet<AuthSession>('local', LOCAL_KEY);
  return session?.serverUrl && session.accessToken ? session : null;
}

export async function clearSession(): Promise<void> {
  if (typeof chrome === 'undefined' || !chrome.storage) return;
  await chrome.storage.local.remove(LOCAL_KEY);
  await chrome.storage.session.remove(['accessToken', 'serverUrl', 'teamSlug']);
}

type TokenResponse = {
  access_token?: string;
  refresh_token?: string;
  expires_in?: number;
  team?: string;
  error?: string;
  error_description?: string;
  message?: string;
};

function apiError(data: TokenResponse, fallback: string): Error {
  return new Error(data.message || data.error_description || data.error || fallback);
}

function sessionFromTokens(serverUrl: string, data: TokenResponse): AuthSession {
  if (!data.access_token) throw new Error('OAuth server did not return an access token');
  return {
    serverUrl: normalizeServerUrl(serverUrl),
    accessToken: data.access_token,
    refreshToken: data.refresh_token ?? '',
    teamSlug: data.team ?? '',
    expiresAt: Date.now() + (typeof data.expires_in === 'number' ? data.expires_in : 3600) * 1000,
  };
}

function base64URL(bytes: Uint8Array): string {
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function randomValue(bytes = 32): string {
  const value = new Uint8Array(bytes);
  crypto.getRandomValues(value);
  return base64URL(value);
}

async function pkceChallenge(verifier: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier));
  return base64URL(new Uint8Array(digest));
}

function extensionRedirectURI(): string {
  if (typeof chrome === 'undefined' || !chrome.identity?.getRedirectURL) {
    throw new Error('chrome.identity is not available');
  }
  return chrome.identity.getRedirectURL('oauth2');
}

function callbackCode(callbackURL: string, expectedState: string): string {
  const callback = new URL(callbackURL);
  const error = callback.searchParams.get('error');
  if (error) throw new Error(callback.searchParams.get('error_description') || error);
  if (callback.searchParams.get('state') !== expectedState) {
    throw new Error('OAuth callback state did not match the authorization request');
  }
  const code = callback.searchParams.get('code');
  if (!code) throw new Error('OAuth callback did not contain an authorization code');
  return code;
}

export async function loginWithOAuth(
  serverUrl: string,
  onStatus?: (status: OAuthStatus) => void,
): Promise<AuthSession> {
  const normalized = normalizeServerUrl(serverUrl);
  await ensureHostPermission(normalized);
  if (typeof chrome === 'undefined' || !chrome.identity?.launchWebAuthFlow) {
    throw new Error('chrome.identity.launchWebAuthFlow is not available');
  }

  const verifier = randomValue(64);
  const state = randomValue();
  const redirectURI = extensionRedirectURI();
  const authorize = new URL(`${normalized}/oauth/authorize`);
  authorize.search = new URLSearchParams({
    response_type: 'code',
    client_id: EXTENSION_CLIENT_ID,
    redirect_uri: redirectURI,
    scope: EXTENSION_SCOPE,
    state,
    code_challenge_method: 'S256',
    code_challenge: await pkceChallenge(verifier),
  }).toString();

  onStatus?.('opening_browser');
  const callbackURL = await chrome.identity.launchWebAuthFlow({ url: authorize.toString(), interactive: true });
  if (!callbackURL) throw new Error('OAuth authorization was cancelled');

  onStatus?.('exchanging_code');
  const form = new URLSearchParams({
    grant_type: 'authorization_code',
    client_id: EXTENSION_CLIENT_ID,
    code: callbackCode(callbackURL, state),
    redirect_uri: redirectURI,
    code_verifier: verifier,
  });
  const response = await fetch(`${normalized}/oauth/token`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: form.toString(),
  });
  const data = (await response.json().catch(() => ({}))) as TokenResponse;
  if (!response.ok || !data.access_token) {
    throw apiError(data, `OAuth token exchange failed (${response.status})`);
  }
  const session = sessionFromTokens(normalized, data);
  await persistSession(session);
  return session;
}

async function refreshTokens(session: AuthSession): Promise<AuthSession> {
  if (!session.refreshToken) throw new Error('no OAuth refresh token available');
  const form = new URLSearchParams({ grant_type: 'refresh_token', client_id: EXTENSION_CLIENT_ID, refresh_token: session.refreshToken });
  const response = await fetch(`${session.serverUrl}/oauth/token`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: form.toString(),
  });
  const data = (await response.json().catch(() => ({}))) as TokenResponse;
  if (!response.ok || !data.access_token) throw apiError(data, `OAuth refresh failed (${response.status})`);
  const next = { ...session, ...sessionFromTokens(session.serverUrl, data), teamSlug: data.team || session.teamSlug };
  await persistSession(next);
  return next;
}

export async function studioFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const session = await loadSession();
  if (!session) throw new Error('not signed in');

  const request = (token: string) => {
    const headers = new Headers(init.headers);
    if (!headers.has('Content-Type')) headers.set('Content-Type', 'application/json');
    headers.set('Authorization', `Bearer ${token}`);
    return fetch(`${session.serverUrl}${path.startsWith('/') ? path : `/${path}`}`, { ...init, headers });
  };
  const response = await request(session.accessToken);
  if (response.status !== 401 || !session.refreshToken) return response;
  return request((await refreshTokens(session)).accessToken);
}
