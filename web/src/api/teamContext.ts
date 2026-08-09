/**
 * Team context for platform-mode API calls.
 *
 * Provides a module-level active team slug and a fetch wrapper that
 * injects the X-Astonish-Team header into every request. The backend's
 * PlatformAuthMiddleware reads this header to override the JWT's default
 * team, enabling per-request team scoping without re-issuing tokens.
 *
 * Usage:
 *   import { setActiveTeam, teamFetch } from './teamContext'
 *
 *   // On team switch:
 *   setActiveTeam('cronus')
 *
 *   // In API calls:
 *   const res = await teamFetch('/api/memories/team')
 */

import { refreshToken } from './auth'

let _activeTeam: string | null = null

const STORAGE_KEY = 'astonish_active_team'

// Callback invoked when the middleware rejects a team (403).
// App.tsx sets this to trigger a team reset + UI notification.
let _onTeamRejected: ((teamSlug: string) => void) | null = null

/** Register a callback for when the middleware rejects a team selection. */
export function onTeamRejected(cb: (teamSlug: string) => void) {
  _onTeamRejected = cb
}

// Callback invoked when a 401 cannot be recovered via token refresh.
// App.tsx wires this to clear auth state and show the login gate.
let _onAuthExpired: (() => void) | null = null

/** Register a callback for when the session cannot be silently renewed. */
export function onAuthExpired(cb: () => void) {
  _onAuthExpired = cb
}

// Single-flight access-token refresh so concurrent 401s share one /refresh.
let _refreshInflight: Promise<boolean> | null = null

function refreshAccessTokenOnce(): Promise<boolean> {
  if (_refreshInflight) return _refreshInflight
  _refreshInflight = (async () => {
    try {
      await refreshToken()
      return true
    } catch {
      return false
    }
  })().finally(() => { _refreshInflight = null })
  return _refreshInflight
}

function requestUrl(input: Parameters<typeof fetch>[0]): string {
  if (typeof input === 'string') return input
  if (input instanceof URL) return input.href
  return input.url
}

function isAuthApiPath(url: string): boolean {
  try {
    const path = url.startsWith('http') ? new URL(url).pathname : url.split('?')[0]
    return path.startsWith('/api/auth/')
  } catch {
    return url.includes('/api/auth/')
  }
}

// Restore from localStorage on module load
try {
  _activeTeam = localStorage.getItem(STORAGE_KEY)
} catch { /* SSR or private browsing */ }

/** Update the active team slug. Called from App.tsx when the user switches teams. */
export function setActiveTeam(slug: string | null) {
  _activeTeam = slug
  try {
    if (slug) {
      localStorage.setItem(STORAGE_KEY, slug)
    } else {
      localStorage.removeItem(STORAGE_KEY)
    }
  } catch { /* ignore */ }
}

/** Get the current active team slug. */
export function getActiveTeam(): string | null {
  return _activeTeam
}

/**
 * Fetch wrapper that injects X-Astonish-Team header when an active team
 * is set. Signature matches the standard fetch() API so it's a drop-in
 * replacement. Falls through to plain fetch() when no team is active
 * (personal mode).
 *
 * If `explicitTeam` is provided, it overrides the global active team for
 * this single request. This is used when a component needs to target a
 * specific team (e.g., Team Management viewing a different team than the
 * one selected in the top-bar).
 *
 * If the backend returns 403 due to team membership rejection, the active
 * team is cleared and the rejection callback is fired so the UI can react.
 *
 * On 401, silently refreshes the access token once and retries the request.
 * If refresh fails, the auth-expired callback is fired so the app can show login.
 */
export async function teamFetch(input: Parameters<typeof fetch>[0], init?: Parameters<typeof fetch>[1], explicitTeam?: string | null): Promise<Response> {
  const effectiveTeam = explicitTeam !== undefined ? explicitTeam : _activeTeam

  const headers = new Headers(init?.headers)
  // CSRF protection: always include X-Requested-With so the server knows
  // this is a programmatic request, not a cross-origin form submission.
  if (!headers.has('X-Requested-With')) {
    headers.set('X-Requested-With', 'XMLHttpRequest')
  }

  // Only set if not already explicitly provided by the caller
  if (effectiveTeam && !headers.has('X-Astonish-Team')) {
    headers.set('X-Astonish-Team', effectiveTeam)
  }

  const res = await fetch(input, { ...init, headers })

  // If the middleware rejected this team (not a member), clear it.
  // Only trigger rejection handling for the global active team, not explicit overrides.
  if (res.status === 403 && explicitTeam === undefined) {
    try {
      const cloned = res.clone()
      const body = await cloned.json()
      if (body?.error?.includes('not a member of this team') || body?.message?.includes('not a member of this team')) {
        const rejectedTeam = _activeTeam
        setActiveTeam(null)
        if (_onTeamRejected && rejectedTeam) {
          _onTeamRejected(rejectedTeam)
        }
      }
    } catch { /* not JSON or other parse error — ignore */ }
  }

  if (res.status === 401 && !isAuthApiPath(requestUrl(input))) {
    const refreshed = await refreshAccessTokenOnce()
    if (refreshed) {
      return fetch(input, { ...init, headers })
    }
    if (_onAuthExpired) {
      _onAuthExpired()
    }
  }

  return res
}
