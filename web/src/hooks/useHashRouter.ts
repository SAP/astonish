import { useState, useEffect, useCallback } from 'react'

// --- Types ---

export interface RouterPath {
  view: string
  params: Record<string, string>
}

export interface HashRouter {
  path: RouterPath
  navigate: (newPath: string) => void
  replaceHash: (newPath: string) => void
}

export interface BuildPathParams {
  agentName?: string
  section?: string
  sessionId?: string
  subView?: string
  subKey?: string
  subKey2?: string
  tab?: string
  tabSection?: string
  /** Settings: team slug for team-scoped sections */
  teamSlug?: string
  /** Settings: tab within team detail */
  teamTab?: string
}

// --- Hook ---

export function useHashRouter(): HashRouter {
  const [path, setPath] = useState<RouterPath>(() => parseHash(window.location.hash))

  useEffect(() => {
    const handleHashChange = () => {
      setPath(parseHash(window.location.hash))
    }
    
    window.addEventListener('hashchange', handleHashChange)
    return () => window.removeEventListener('hashchange', handleHashChange)
  }, [])

  const navigate = useCallback((newPath: string) => {
    const hash = newPath.startsWith('#') ? newPath : `#${newPath}`
    window.location.hash = hash
  }, [])

  const replaceHash = useCallback((newPath: string) => {
    const hash = newPath.startsWith('#') ? newPath : `#${newPath}`
    window.history.replaceState(null, '', hash)
    setPath(parseHash(hash))
  }, [])

  return { path, navigate, replaceHash }
}

export function parseHash(hash: string): RouterPath {
  const cleanHash = hash.replace(/^#\/?/, '')
  const parts = cleanHash.split('/').filter(Boolean)
  
  if (parts.length === 0) {
    return { view: 'chat', params: {} }
  }

  const view = parts[0]

  if (view === 'agent' && parts[1]) {
    return { view: 'agent', params: { agentName: decodeURIComponent(parts[1]) } }
  }

  if (view === 'settings') {
    // Routes:
    //   #/settings                        → section='chat' (first item)
    //   #/settings/general                → section='general'
    //   #/settings/team/members           → section='team', teamTab='members' (active team)
    //   #/settings/team/engineering/skills → section='team', teamSlug='engineering', teamTab='skills'
    //   #/settings/org/users              → section='org', subsection='users'
    //   #/settings/platform/orgs          → section='platform', subsection='orgs'
    const section = parts[1] || 'chat'

    if (section === 'team') {
      // 2 parts after 'team' → slug + tab; 1 part → just tab (use active team)
      if (parts.length >= 4) {
        // #/settings/team/:slug/:tab
        return { view: 'settings', params: { section: 'team', teamSlug: decodeURIComponent(parts[2]), teamTab: parts[3] || 'members' } }
      }
      // #/settings/team/:tab
      return { view: 'settings', params: { section: 'team', teamSlug: '', teamTab: parts[2] || 'members' } }
    }

    if (section === 'org') {
      return { view: 'settings', params: { section: 'org', subsection: parts[2] || 'users' } }
    }

    if (section === 'platform') {
      return { view: 'settings', params: { section: 'platform', subsection: parts[2] || 'orgs' } }
    }

    return { view: 'settings', params: { section } }
  }

  if (view === 'chat') {
    return { view: 'chat', params: { sessionId: parts[1] ? decodeURIComponent(parts[1]) : '' } }
  }

  if (view === 'canvas') {
    return { view: 'canvas', params: {} }
  }

  if (view === 'fleet') {
    const subView = parts[1] || ''
    const subKey = parts[2] ? decodeURIComponent(parts[2]) : ''
    return { view: 'fleet', params: { subView, subKey } }
  }

  if (view === 'drill') {
    const subView = parts[1] || ''
    const subKey = parts[2] ? decodeURIComponent(parts[2]) : ''
    const subKey2 = parts[3] ? decodeURIComponent(parts[3]) : ''
    return { view: 'drill', params: { subView, subKey, subKey2 } }
  }

  if (view === 'apps') {
    return { view: 'apps', params: { appName: parts[1] ? decodeURIComponent(parts[1]) : '' } }
  }

  if (view === 'slides') {
    // Reserved first segment 'templates' routes to the dedicated Templates area.
    if (parts[1] === 'templates') {
      return { view: 'slides', params: { subView: 'templates' } }
    }
    return { view: 'slides', params: { deckSlug: parts[1] ? decodeURIComponent(parts[1]) : '' } }
  }

  if (view === 'home') {
    return { view: 'chat', params: {} }
  }

  if (view === 'credentials') {
    return { view: 'settings', params: { section: 'credentials' } }
  }

  if (view === 'knowledge') {
    return { view: 'settings', params: { section: 'knowledge' } }
  }

  // Legacy: redirect team-mgmt URLs to new settings paths
  if (view === 'team-mgmt') {
    const mgmtSection = parts[1] || 'teams'
    if (mgmtSection === 'users') return { view: 'settings', params: { section: 'org', subsection: 'users' } }
    if (mgmtSection === 'audit') return { view: 'settings', params: { section: 'org', subsection: 'audit' } }
    if (mgmtSection === 'platform') return { view: 'settings', params: { section: 'platform', subsection: 'orgs' } }
    if (mgmtSection === 'teams' && parts[2]) {
      const teamSlug = parts[2]
      const teamTab = parts[3] || 'members'
      return { view: 'settings', params: { section: 'team', teamSlug, teamTab } }
    }
    // Default: go to team members
    return { view: 'settings', params: { section: 'team', teamSlug: '', teamTab: 'members' } }
  }

  if (view === 'platform-admin') {
    // Legacy: redirect to new settings/platform path
    const tab = parts[1] || 'orgs'
    return { view: 'settings', params: { section: 'platform', subsection: tab } }
  }

  return { view, params: {} }
}

export function buildPath(view: string, params: BuildPathParams = {}): string {
  switch (view) {
    case 'agent':
      return `/agent/${encodeURIComponent(params.agentName || '')}`
    case 'settings': {
      const section = params.section || 'chat'

      if (section === 'team') {
        const tab = params.teamTab || 'members'
        if (params.teamSlug) {
          if (tab !== 'members') return `/settings/team/${encodeURIComponent(params.teamSlug)}/${tab}`
          return `/settings/team/${encodeURIComponent(params.teamSlug)}/members`
        }
        // No slug — use active team (just tab in URL)
        return `/settings/team/${tab}`
      }

      if (section === 'org') {
        const sub = params.subView || 'users'
        return `/settings/org/${sub}`
      }

      if (section === 'platform') {
        const sub = params.subView || 'orgs'
        return `/settings/platform/${sub}`
      }

      return `/settings/${section}`
    }
    case 'chat':
      if (params.sessionId) {
        return `/chat/${encodeURIComponent(params.sessionId)}`
      }
      return '/chat'
    case 'canvas':
      return '/canvas'
    case 'fleet':
      if (params.subView && params.subKey) {
        return `/fleet/${params.subView}/${encodeURIComponent(params.subKey)}`
      }
      return '/fleet'
    case 'drill':
      if (params.subView === 'drill' && params.subKey && params.subKey2) {
        return `/drill/drill/${encodeURIComponent(params.subKey)}/${encodeURIComponent(params.subKey2)}`
      }
      if (params.subView && params.subKey) {
        return `/drill/${params.subView}/${encodeURIComponent(params.subKey)}`
      }
      return '/drill'
    case 'apps':
      if (params.subKey) {
        return `/apps/${encodeURIComponent(params.subKey)}`
      }
      return '/apps'
    case 'slides':
      if (params.subView === 'templates') {
        return '/slides/templates'
      }
      if (params.subKey) {
        return `/slides/${encodeURIComponent(params.subKey)}`
      }
      return '/slides'
    default:
      return '/chat'
  }
}
