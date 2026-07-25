import { useState, useEffect, useCallback, useMemo } from 'react'
import { Crosshair } from 'lucide-react'
import { fetchDrillSuites } from '../api/drillApi'
import type { DrillSuiteSummary } from '../api/drillApi'
import { buildPath } from '../hooks/useHashRouter'
import type { RouterPath } from '../hooks/useHashRouter'
import type { SelectedItem } from './drill/drillUtils'
import DrillSidebar from './drill/DrillSidebar'
import SuiteDetail from './drill/SuiteDetail'
import DrillDetailView from './drill/DrillDetailView'


// ─── Empty State ───

function EmptyState() {
  return (
    <div className="flex flex-1 items-center justify-center">
      <div className="text-center">
        <Crosshair size={48} className="mx-auto mb-4 text-[color:var(--warning)]/30" />
        <h2 className="mb-2 text-lg font-semibold text-foreground">Drill Management</h2>
        <p className="max-w-md text-sm text-muted-foreground">
          Select a drill suite from the sidebar to get started.
          Drills are AI-composed, mechanically-replayed sequences of tool calls
          with assertions and reporting — perfect for health checks, deployment
          verification, and repeatable multi-step automation.
        </p>
      </div>
    </div>
  )
}


// ─── Main DrillView ───

interface DrillViewProps {
  path: RouterPath | null
  onNavigate: (path: string) => void
  onRunSuite: (suiteKey: string, instructions?: string) => void
  onAddDrills: (suiteKey: string) => void
  theme: 'dark' | 'light'
}

export default function DrillView({ path, onNavigate, onRunSuite, onAddDrills, theme }: DrillViewProps) {
  const [suites, setSuites] = useState<DrillSuiteSummary[]>([])
  const [isLoading, setIsLoading] = useState(true)
  const [searchQuery, setSearchQuery] = useState('')

  const loadData = useCallback(async () => {
    try {
      const suitesData = await fetchDrillSuites()
      setSuites(Array.isArray(suitesData) ? suitesData : [])
    } catch (err) {
      console.error('Failed to load drill data:', err)
    } finally {
      setIsLoading(false)
    }
  }, [])

  useEffect(() => {
    loadData()
    const interval = setInterval(loadData, 30000)
    return () => clearInterval(interval)
  }, [loadData])

  const selectedItem = useMemo((): SelectedItem | null => {
    const subView = path?.params?.subView
    const subKey = path?.params?.subKey
    if (subView && subKey) {
      return { type: subView, key: subKey }
    }
    return null
  }, [path])

  const handleSelect = useCallback((item: SelectedItem) => {
    const hashPath = buildPath('drill', { subView: item.type, subKey: item.key })
    onNavigate('#' + hashPath)
  }, [onNavigate])

  const handleNavigate = useCallback((hashPath: string) => {
    onNavigate(hashPath)
  }, [onNavigate])

  const filteredSuites = useMemo(() => {
    if (!searchQuery) return suites
    const q = searchQuery.toLowerCase()
    return suites.filter(s =>
      s.name.toLowerCase().includes(q) ||
      (s.description && s.description.toLowerCase().includes(q))
    )
  }, [suites, searchQuery])

  const renderContent = () => {
    if (!selectedItem) return <EmptyState />

    switch (selectedItem.type) {
      case 'suite':
        return (
          <SuiteDetail
            key={selectedItem.key}
            suiteKey={selectedItem.key}
            onNavigate={handleNavigate}
            onRunSuite={onRunSuite}
            onAddDrills={onAddDrills}
            onRefresh={loadData}
            theme={theme}
          />
        )
      case 'drill':
        return (
          <DrillDetailView
            key={`${path?.params?.subKey}-${path?.params?.subKey2}`}
            suiteKey={path?.params?.subKey || ''}
            drillKey={path?.params?.subKey2 || ''}
            onNavigate={handleNavigate}
            theme={theme}
          />
        )
      default:
        return <EmptyState />
    }
  }

  return (
    <div className="flex flex-1 overflow-hidden">
      <DrillSidebar
        suites={filteredSuites}
        selectedItem={selectedItem}
        onSelect={handleSelect}
        onSearch={setSearchQuery}
        searchQuery={searchQuery}
        isLoading={isLoading}
      />
      <div className="flex-1 flex flex-col overflow-hidden">
        {renderContent()}
      </div>
    </div>
  )
}
