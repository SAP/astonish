import { useCallback, useEffect, useState } from 'react'

export type FleetDetailTab = 'overview' | 'settings' | 'agents' | 'yaml'

const TABS: FleetDetailTab[] = ['overview', 'settings', 'agents', 'yaml']

export function useFleetDetailTab(kind: 'template' | 'plan', key: string): [FleetDetailTab, (tab: FleetDetailTab) => void] {
  const readTab = useCallback(() => {
    const parts = window.location.hash.replace(/^#\/?/, '').split('/').filter(Boolean)
    const maybeTab = parts[0] === 'fleet' && parts[1] === kind && decodeURIComponent(parts[2] || '') === key ? parts[3] : ''
    return TABS.includes(maybeTab as FleetDetailTab) ? maybeTab as FleetDetailTab : 'overview'
  }, [kind, key])

  const [tab, setTabState] = useState<FleetDetailTab>(readTab)

  useEffect(() => {
    const onHashChange = () => setTabState(readTab())
    window.addEventListener('hashchange', onHashChange)
    onHashChange()
    return () => window.removeEventListener('hashchange', onHashChange)
  }, [kind, key, readTab])

  const setTab = (next: FleetDetailTab) => {
    window.location.hash = `/fleet/${kind}/${encodeURIComponent(key)}/${next}`
    setTabState(next)
  }

  return [tab, setTab]
}
