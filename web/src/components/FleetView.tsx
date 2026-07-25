import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { Rocket } from 'lucide-react'
import {
  fetchFleetPlans, fetchFleets, fetchFleetSessionsHistory, fetchSetupProfiles,
} from '../api/fleetChat'
import type {
  FleetDefinition, FleetPlanSummary, SetupProfileSummary,
} from '../api/fleetChat'
import { deleteSession } from '../api/studioChat'
import { buildPath } from '../hooks/useHashRouter'
import type { RouterPath } from '../hooks/useHashRouter'
import type { FleetChatSession, SelectedItem } from './fleet/fleetUtils'
import FleetSidebar from './fleet/FleetSidebar'
import PlanDetail from './fleet/PlanDetail'
import SessionTrace from './fleet/SessionTrace'
import TemplateDetail from './fleet/TemplateDetail'
import SetupProfileDetail from './fleet/SetupProfileDetail'

// ─── Empty State ───

function EmptyState() {
  return (
    <div className="flex flex-1 items-center justify-center bg-[color:var(--work-background,var(--background))]">
      <div className="text-center">
        <Rocket size={48} className="mx-auto mb-4 text-primary/35" />
        <h2 className="mb-2 text-lg font-semibold text-foreground">Fleet Management</h2>
        <p className="max-w-md text-sm text-muted-foreground">
          Select a fleet plan, session, or template from the sidebar to get started.
          Fleet plans define autonomous agent teams that can be launched manually or
          activated to monitor external channels like GitHub Issues.
        </p>
      </div>
    </div>
  )
}

// ─── Main FleetView Component ───

interface FleetViewProps {
  theme: string
  path?: RouterPath | null
  onNavigate: (path: string) => void
  onCreatePlan?: (key: string, draftId?: string) => void
}

export default function FleetView({ theme, path, onNavigate, onCreatePlan }: FleetViewProps) {
  const [plans, setPlans] = useState<FleetPlanSummary[]>([])
  const [templates, setTemplates] = useState<FleetDefinition[]>([])
  const [setupProfiles, setSetupProfiles] = useState<SetupProfileSummary[]>([])
  const [sessions, setSessions] = useState<FleetChatSession[]>([])
  const [isLoading, setIsLoading] = useState(true)
  const [searchQuery, setSearchQuery] = useState('')
  const refreshInterval = useRef<ReturnType<typeof setInterval> | null>(null)

  // Derive selected item from URL path
  const selectedItem = useMemo<SelectedItem | null>(() => {
    const subView = path?.params?.subView
    const subKey = path?.params?.subKey
    if (subView && subKey) {
      return { type: subView, key: subKey }
    }
    return null
  }, [path])

  // Load data
  const loadData = useCallback(async () => {
    try {
      const [planData, fleetData, profileData, sessionData] = await Promise.all([
        fetchFleetPlans().catch(() => ({ plans: [] as FleetPlanSummary[] })),
        fetchFleets().catch(() => ({ fleets: [] as FleetDefinition[] })),
        fetchSetupProfiles().catch(() => ({ profiles: [] as SetupProfileSummary[] })),
        fetchFleetSessionsHistory().catch(() => [] as FleetChatSession[]),
      ])
      setPlans(planData.plans || [])
      setTemplates(fleetData.fleets || [])
      setSetupProfiles(profileData.profiles || [])
      // Fleet sessions endpoint already returns only fleet sessions.
      const allSessions: FleetChatSession[] = Array.isArray(sessionData) ? sessionData as FleetChatSession[] : []
      setSessions(allSessions)
    } catch (err: any) {
      console.error('Failed to load fleet data:', err)
    } finally {
      setIsLoading(false)
    }
  }, [])

  useEffect(() => {
    loadData()
    // Refresh every 30s
    refreshInterval.current = setInterval(loadData, 30000)
    return () => { if (refreshInterval.current) clearInterval(refreshInterval.current) }
  }, [loadData])

  const handleSelect = useCallback((item: SelectedItem) => {
    const hashPath = buildPath('fleet', { subView: item.type, subKey: item.key })
    onNavigate('#' + hashPath)
  }, [onNavigate])

  const handleNavigate = useCallback((hashPath: string) => {
    onNavigate('#' + hashPath)
  }, [onNavigate])

  const handleDeleteSession = useCallback(async (sessionId: string) => {
    try {
      await deleteSession(sessionId)
      // If the deleted session is currently selected, clear selection
      if (selectedItem?.type === 'session' && selectedItem?.key === sessionId) {
        onNavigate('#' + buildPath('fleet'))
      }
      loadData()
    } catch (err: any) {
      console.error('Failed to delete session:', err)
    }
  }, [selectedItem, onNavigate, loadData])

  // Filter items by search query
  const filteredPlans = useMemo(() => {
    if (!searchQuery) return plans
    const q = searchQuery.toLowerCase()
    return plans.filter(p => p.name?.toLowerCase().includes(q) || p.key?.toLowerCase().includes(q))
  }, [plans, searchQuery])

  const filteredSessions = useMemo(() => {
    if (!searchQuery) return sessions
    const q = searchQuery.toLowerCase()
    return sessions.filter(s =>
      s.title?.toLowerCase().includes(q) ||
      s.id?.toLowerCase().includes(q) ||
      s.fleetName?.toLowerCase().includes(q)
    )
  }, [sessions, searchQuery])

  const filteredTemplates = useMemo(() => {
    if (!searchQuery) return templates
    const q = searchQuery.toLowerCase()
    return templates.filter(t => t.name?.toLowerCase().includes(q) || t.key?.toLowerCase().includes(q))
  }, [templates, searchQuery])

  // Render main content based on selection
  const renderContent = () => {
    if (!selectedItem) return <EmptyState />

    switch (selectedItem.type) {
      case 'plan':
        return (
          <PlanDetail
            key={selectedItem.key}
            planKey={selectedItem.key}
            onNavigate={handleNavigate}
            onRefresh={loadData}
            theme={theme}
          />
        )
      case 'session':
        return (
          <SessionTrace
            key={selectedItem.key}
            sessionId={selectedItem.key}
            onRefresh={loadData}
            onNavigate={handleNavigate}
          />
        )
      case 'template':
        return (
          <TemplateDetail
            key={selectedItem.key}
            templateKey={selectedItem.key}
            templates={templates}
            setupProfiles={setupProfiles}
            onNavigateToSetupProfile={(profileKey) => handleSelect({ type: 'setup-profile', key: profileKey })}
            onCreatePlan={onCreatePlan}
            onPlanCreated={(planKey) => {
              loadData()
              handleSelect({ type: 'plan', key: planKey })
            }}
            onCloned={async (newKey) => {
              await loadData()
              handleSelect({ type: 'template', key: newKey })
            }}
          />
        )
      case 'setup-profile':
        return (
          <SetupProfileDetail
            key={selectedItem.key}
            profileKey={selectedItem.key}
            theme={theme}
            onCloned={async (newKey) => {
              await loadData()
              handleSelect({ type: 'setup-profile', key: newKey })
            }}
            onDeleted={async () => {
              await loadData()
              onNavigate('#' + buildPath('fleet'))
            }}
            onUpdated={loadData}
          />
        )
      default:
        return <EmptyState />
    }
  }

  return (
    <div className="flex flex-1 overflow-hidden bg-[color:var(--work-background,var(--background))]">
      <FleetSidebar
        plans={filteredPlans}
        sessions={filteredSessions}
        templates={filteredTemplates}
        setupProfiles={setupProfiles}
        selectedItem={selectedItem}
        onSelect={handleSelect}
        onDeleteSession={handleDeleteSession}
        onSearch={setSearchQuery}
        searchQuery={searchQuery}
        isLoading={isLoading}
        theme={theme}
        onRefresh={loadData}
      />
      <div className="flex-1 flex flex-col overflow-hidden">
        {renderContent()}
      </div>
    </div>
  )
}
