import { useState, useEffect, useCallback } from 'react'
import {
  Crosshair, ChevronDown, ChevronRight, Loader, Trash2, Play, Clock,
  AlertCircle, ArrowRight, Plus, Zap, Shield, Server, Terminal,
  BarChart3, ListChecks, Code,
} from 'lucide-react'
import {
  fetchDrillSuite, deleteDrillSuite, deleteDrill,
  fetchSuiteYaml, saveSuiteYaml,
} from '../../api/drillApi'
import type { DrillSuiteDetail, DrillDetail } from '../../api/drillApi'
import { reportTestEntries, reportDurationMs } from '../../api/drillApi'
import { buildPath } from '../../hooks/useHashRouter'
import YamlDrawer from '../YamlDrawer'
import { formatTimeAgo, formatDuration } from './drillUtils'
import { StatusDot, StatusBadge } from './drillComponents'
import { ReportStepCard } from './DrillCards'
import { generateRunInstructions } from '../../utils/generateRunInstructions'

// ─── Suite Detail ───

interface SuiteDetailProps {
  suiteKey: string
  onNavigate: (path: string) => void
  onRunSuite: (suiteKey: string, instructions?: string) => void
  onAddDrills: (suiteKey: string) => void
  onRefresh?: () => void
  theme: 'dark' | 'light'
}

export default function SuiteDetail({ suiteKey, onNavigate, onRunSuite, onAddDrills, onRefresh, theme }: SuiteDetailProps) {
  const [suite, setSuite] = useState<DrillSuiteDetail | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null) // null | 'suite' | drillName
  const [activeTab, setActiveTab] = useState('drills')
  const [expandedDrills, setExpandedDrills] = useState<Record<string, boolean>>({})
  const [showYaml, setShowYaml] = useState(false)
  const [yamlContent, setYamlContent] = useState('')
  const [isSaving, setIsSaving] = useState(false)
  const [saveStatus, setSaveStatus] = useState<'saved' | 'error' | null>(null)

  const loadSuite = useCallback(async () => {
    try {
      const [suiteData, yamlData] = await Promise.all([
        fetchDrillSuite(suiteKey),
        fetchSuiteYaml(suiteKey),
      ])
      setSuite(suiteData)
      setYamlContent(yamlData)
    } catch (err: any) {
      setError(err.message)
    } finally {
      setIsLoading(false)
    }
  }, [suiteKey])

  useEffect(() => {
    setIsLoading(true)
    setError(null)
    loadSuite()
  }, [loadSuite])

  const handleDeleteSuite = async () => {
    try {
      await deleteDrillSuite(suiteKey)
      setConfirmDelete(null)
      if (onRefresh) onRefresh()
      onNavigate('#' + buildPath('drill'))
    } catch (err: any) {
      setError(err.message)
    }
  }

  const handleDeleteDrill = async (drillName: string) => {
    try {
      await deleteDrill(suiteKey, drillName)
      setConfirmDelete(null)
      const data = await fetchDrillSuite(suiteKey)
      setSuite(data)
      if (onRefresh) onRefresh()
    } catch (err: any) {
      setError(err.message)
    }
  }

  const toggleReportDrill = (name: string) => {
    setExpandedDrills(prev => ({ ...prev, [name]: !prev[name] }))
  }

  const handleSaveYaml = async () => {
    if (isSaving) return
    setIsSaving(true)
    setSaveStatus(null)
    try {
      await saveSuiteYaml(suiteKey, yamlContent)
      setSaveStatus('saved')
      // Re-fetch suite JSON to sync the visual view with saved YAML
      const suiteData = await fetchDrillSuite(suiteKey)
      setSuite(suiteData)
      if (onRefresh) onRefresh()
      setTimeout(() => setSaveStatus(null), 2000)
    } catch (err: any) {
      setSaveStatus('error')
      setError(err.message)
    } finally {
      setIsSaving(false)
    }
  }

  if (isLoading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <Loader size={24} className="animate-spin" style={{ color: 'var(--text-muted)' }} />
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <div className="text-center">
          <AlertCircle size={32} className="mx-auto mb-2" style={{ color: '#ef4444' }} />
          <p className="text-sm" style={{ color: 'var(--text-secondary)' }}>{error}</p>
        </div>
      </div>
    )
  }

  if (!suite) return null

  const report = suite.last_report
  const reportTests = reportTestEntries(report)

  return (
    <div className="flex-1 flex flex-col overflow-hidden">
      {/* Header */}
      <div className="p-6 pb-0">
        <div className="flex items-start justify-between mb-4">
          <div>
            <h1 className="text-xl font-bold mb-1" style={{ color: 'var(--text-primary)' }}>{suite.name}</h1>
            {suite.description && (
              <p className="text-sm" style={{ color: 'var(--text-secondary)' }}>{suite.description}</p>
            )}
            <p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>{suite.file}</p>
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={() => {
                const cfg = suite?.suite_config as Record<string, unknown> | undefined
                onRunSuite(suiteKey, generateRunInstructions(suiteKey, cfg))
              }}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium text-white transition-colors hover:opacity-90"
              style={{ background: 'linear-gradient(135deg, #f59e0b 0%, #d97706 100%)' }}
            >
              <Play size={12} /> Run
            </button>
            <button
              onClick={() => onAddDrills(suiteKey)}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors hover:bg-white/10"
              style={{ border: '1px solid var(--border-color)', color: 'var(--text-secondary)' }}
            >
              <Plus size={12} /> Add Drills
            </button>
            <button
              onClick={() => setConfirmDelete('suite')}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors hover:bg-red-500/10"
              style={{ border: '1px solid var(--border-color)', color: 'var(--text-secondary)' }}
            >
              <Trash2 size={12} />
            </button>
            <button
              onClick={() => setShowYaml(!showYaml)}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors"
              style={{
                background: showYaml ? 'rgba(245, 158, 11, 0.15)' : 'var(--bg-tertiary)',
                color: showYaml ? '#f59e0b' : 'var(--text-secondary)',
                border: '1px solid var(--border-color)',
              }}
            >
              <Code size={12} />
              {showYaml ? 'Hide Source' : 'View Source'}
            </button>
          </div>
        </div>

        {/* Delete confirmation */}
        {confirmDelete === 'suite' && (
          <div className="mb-4 p-3 rounded-lg flex items-center justify-between" style={{ background: 'rgba(239, 68, 68, 0.1)', border: '1px solid rgba(239, 68, 68, 0.3)' }}>
            <span className="text-xs" style={{ color: '#f87171' }}>Delete suite &ldquo;{suiteKey}&rdquo; and all its drills?</span>
            <div className="flex gap-2">
              <button onClick={handleDeleteSuite} className="px-2 py-1 rounded text-xs font-medium text-white bg-red-600 hover:bg-red-700">Delete</button>
              <button onClick={() => setConfirmDelete(null)} className="px-2 py-1 rounded text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>Cancel</button>
            </div>
          </div>
        )}

        {/* Tab bar */}
        <div className="flex gap-0 border-b" style={{ borderColor: 'var(--border-color)' }}>
          <button
            onClick={() => setActiveTab('drills')}
            className="px-4 py-2 text-xs font-medium transition-colors relative"
            style={{ color: activeTab === 'drills' ? '#f59e0b' : 'var(--text-muted)' }}
          >
            <span className="flex items-center gap-1.5">
              <ListChecks size={12} />
              Drills ({suite.drills?.length || 0})
            </span>
            {activeTab === 'drills' && (
              <div className="absolute bottom-0 left-0 right-0 h-0.5" style={{ background: '#f59e0b' }} />
            )}
          </button>
          <button
            onClick={() => setActiveTab('report')}
            className="px-4 py-2 text-xs font-medium transition-colors relative"
            style={{ color: activeTab === 'report' ? '#f59e0b' : 'var(--text-muted)' }}
          >
            <span className="flex items-center gap-1.5">
              <BarChart3 size={12} />
              Report
              {report && <StatusDot status={report.status} size={6} />}
            </span>
            {activeTab === 'report' && (
              <div className="absolute bottom-0 left-0 right-0 h-0.5" style={{ background: '#f59e0b' }} />
            )}
          </button>
        </div>
      </div>

      {/* Tab content */}
      <div className={`${showYaml ? 'h-1/2' : 'flex-1'} overflow-y-auto p-6`}>
        {activeTab === 'drills' ? (
          <>
            {/* Suite Config / Infrastructure */}
            {suite.suite_config && (
              <div className="mb-6 p-4 rounded-xl" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
                <h3 className="text-xs font-semibold uppercase tracking-wider mb-3" style={{ color: 'var(--text-muted)' }}>Infrastructure</h3>
                <div className="grid grid-cols-2 gap-3 text-xs">
                  {(suite.suite_config as Record<string, any>).template && (
                    <div className="flex items-center gap-2">
                      <Server size={12} style={{ color: '#f59e0b' }} />
                      <span style={{ color: 'var(--text-secondary)' }}>Template:</span>
                      <span className="font-mono" style={{ color: 'var(--text-primary)' }}>{(suite.suite_config as Record<string, any>).template}</span>
                    </div>
                  )}
                  {(suite.suite_config as Record<string, any>).base_url && (
                    <div className="flex items-center gap-2">
                      <Zap size={12} style={{ color: '#f59e0b' }} />
                      <span style={{ color: 'var(--text-secondary)' }}>Base URL:</span>
                      <span className="font-mono" style={{ color: 'var(--text-primary)' }}>{(suite.suite_config as Record<string, any>).base_url}</span>
                    </div>
                  )}
                  {(suite.suite_config as Record<string, any>).services && (suite.suite_config as Record<string, any>).services.length > 0 && (
                    <div className="flex items-center gap-2 col-span-2">
                      <Terminal size={12} style={{ color: '#f59e0b' }} />
                      <span style={{ color: 'var(--text-secondary)' }}>Services:</span>
                      <span className="font-mono" style={{ color: 'var(--text-primary)' }}>{(suite.suite_config as Record<string, any>).services.map((s: any) => s.name || s).join(', ')}</span>
                    </div>
                  )}
                  {(suite.suite_config as Record<string, any>).setup && (
                    <div className="flex items-start gap-2 col-span-2">
                      <Terminal size={12} className="mt-0.5" style={{ color: '#f59e0b' }} />
                      <span style={{ color: 'var(--text-secondary)' }}>Setup:</span>
                      <code className="font-mono text-[11px] bg-black/20 px-1.5 py-0.5 rounded" style={{ color: 'var(--text-primary)' }}>{(suite.suite_config as Record<string, any>).setup}</code>
                    </div>
                  )}
                  {(suite.suite_config as Record<string, any>).ready_check && (
                    <div className="flex items-start gap-2 col-span-2">
                      <Shield size={12} className="mt-0.5" style={{ color: '#f59e0b' }} />
                      <span style={{ color: 'var(--text-secondary)' }}>Ready Check:</span>
                      <code className="font-mono text-[11px] bg-black/20 px-1.5 py-0.5 rounded" style={{ color: 'var(--text-primary)' }}>{(suite.suite_config as Record<string, any>).ready_check.type}{(suite.suite_config as Record<string, any>).ready_check.url ? ` ${(suite.suite_config as Record<string, any>).ready_check.url}` : (suite.suite_config as Record<string, any>).ready_check.port ? ` :${(suite.suite_config as Record<string, any>).ready_check.port}` : ''}{(suite.suite_config as Record<string, any>).ready_check.timeout ? ` (${(suite.suite_config as Record<string, any>).ready_check.timeout}s)` : ''}</code>
                    </div>
                  )}
                </div>
              </div>
            )}

            {/* Drills List */}
            <div className="space-y-2">
              {(suite.drills || []).map((drill: DrillDetail) => (
                <div
                  key={drill.name}
                  className="p-3 rounded-lg cursor-pointer hover:bg-white/5 transition-colors group"
                  style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}
                  onClick={() => onNavigate('#' + buildPath('drill', { subView: 'drill', subKey: suiteKey, subKey2: drill.name }))}
                >
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <Crosshair size={12} style={{ color: '#f59e0b' }} />
                      <span className="text-xs font-medium" style={{ color: 'var(--text-primary)' }}>{drill.name}</span>
                      {drill.step_count > 0 && (
                        <span className="text-[10px] px-1.5 py-0.5 rounded-full" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
                          {drill.step_count} step{drill.step_count !== 1 ? 's' : ''}
                        </span>
                      )}
                    </div>
                    <div className="flex items-center gap-2">
                      {drill.tags && drill.tags.length > 0 && (
                        <div className="flex gap-1">
                          {drill.tags.map((tag: string) => (
                            <span key={tag} className="text-[10px] px-1.5 py-0.5 rounded-full" style={{ background: 'rgba(245, 158, 11, 0.1)', color: '#fbbf24' }}>
                              {tag}
                            </span>
                          ))}
                        </div>
                      )}
                      {drill.timeout > 0 && (
                        <span className="text-[10px] flex items-center gap-0.5" style={{ color: 'var(--text-muted)' }}>
                          <Clock size={10} /> {drill.timeout}s
                        </span>
                      )}
                      {confirmDelete === drill.name ? (
                        <div className="flex gap-1" onClick={(e: React.MouseEvent) => e.stopPropagation()}>
                          <button onClick={() => handleDeleteDrill(drill.name)} className="px-1.5 py-0.5 rounded text-[10px] font-medium text-white bg-red-600 hover:bg-red-700">Delete</button>
                          <button onClick={() => setConfirmDelete(null)} className="px-1.5 py-0.5 rounded text-[10px] font-medium" style={{ color: 'var(--text-muted)' }}>Cancel</button>
                        </div>
                      ) : (
                        <button
                          onClick={(e: React.MouseEvent) => { e.stopPropagation(); setConfirmDelete(drill.name) }}
                          className="opacity-0 group-hover:opacity-100 transition-opacity p-1 rounded hover:bg-red-500/10"
                          style={{ color: 'var(--text-muted)' }}
                        >
                          <Trash2 size={11} />
                        </button>
                      )}
                      <ArrowRight size={12} className="opacity-0 group-hover:opacity-50 transition-opacity" style={{ color: 'var(--text-muted)' }} />
                    </div>
                  </div>
                  {drill.description && (
                    <p className="text-[11px] mt-1 pl-5" style={{ color: 'var(--text-muted)' }}>{drill.description}</p>
                  )}
                </div>
              ))}
              {(!suite.drills || suite.drills.length === 0) && (
                <div className="text-center py-8">
                  <Crosshair size={32} className="mx-auto mb-2" style={{ color: 'rgba(245, 158, 11, 0.2)' }} />
                  <p className="text-xs" style={{ color: 'var(--text-muted)' }}>No drills yet. Click &ldquo;Add Drills&rdquo; to create some.</p>
                </div>
              )}
            </div>
          </>
        ) : (
          /* Report tab */
          report ? (
            <>
              {/* Report summary header */}
              <div className="flex items-center gap-3 mb-4">
                <StatusBadge status={report.status} />
                <span className="text-xs" style={{ color: 'var(--text-secondary)' }}>{report.summary}</span>
                <span className="text-xs" style={{ color: 'var(--text-muted)' }}>&middot; {formatDuration(reportDurationMs(report))}</span>
                <span className="text-xs" style={{ color: 'var(--text-muted)' }}>&middot; {formatTimeAgo(report.finished_at)}</span>
              </div>

              {/* Pass/Fail Summary Bar */}
              {reportTests.length > 0 && (
                <div className="mb-6">
                  <div className="flex rounded-full overflow-hidden h-2">
                    {(() => {
                      const passed = reportTests.filter((t: Record<string, any>) => t.status === 'passed').length
                      const failed = reportTests.filter((t: Record<string, any>) => t.status === 'failed').length
                      const errored = reportTests.filter((t: Record<string, any>) => t.status === 'error').length
                      const skipped = reportTests.length - passed - failed - errored
                      const total = reportTests.length
                      return (
                        <>
                          {passed > 0 && <div style={{ width: `${(passed / total) * 100}%`, background: '#22c55e' }} />}
                          {failed > 0 && <div style={{ width: `${(failed / total) * 100}%`, background: '#ef4444' }} />}
                          {errored > 0 && <div style={{ width: `${(errored / total) * 100}%`, background: '#f59e0b' }} />}
                          {skipped > 0 && <div style={{ width: `${(skipped / total) * 100}%`, background: '#6b7280' }} />}
                        </>
                      )
                    })()}
                  </div>
                  <div className="flex gap-4 mt-2 text-[10px]" style={{ color: 'var(--text-muted)' }}>
                    <span className="flex items-center gap-1"><span className="w-2 h-2 rounded-full inline-block" style={{ background: '#22c55e' }} /> {reportTests.filter((t: Record<string, any>) => t.status === 'passed').length} passed</span>
                    <span className="flex items-center gap-1"><span className="w-2 h-2 rounded-full inline-block" style={{ background: '#ef4444' }} /> {reportTests.filter((t: Record<string, any>) => t.status === 'failed').length} failed</span>
                    <span className="flex items-center gap-1"><span className="w-2 h-2 rounded-full inline-block" style={{ background: '#f59e0b' }} /> {reportTests.filter((t: Record<string, any>) => t.status === 'error').length} errored</span>
                  </div>
                </div>
              )}

              {/* Per-Drill Results */}
              <div className="space-y-2">
                {reportTests.map((test: Record<string, any>) => {
                  const isExpanded = expandedDrills[test.name]
                  const steps = test.steps || []
                  const triage = test.triage

                  return (
                    <div
                      key={test.name}
                      className="rounded-lg overflow-hidden"
                      style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}
                    >
                      <button
                        onClick={() => toggleReportDrill(test.name)}
                        className="w-full flex items-center justify-between p-3 hover:bg-white/5 transition-colors"
                      >
                        <div className="flex items-center gap-2">
                          <StatusDot status={test.status} />
                          <span className="text-xs font-medium" style={{ color: 'var(--text-primary)' }}>{test.name}</span>
                          {reportDurationMs(test) > 0 && (
                            <span className="text-[10px]" style={{ color: 'var(--text-muted)' }}>{formatDuration(reportDurationMs(test))}</span>
                          )}
                        </div>
                        <div className="flex items-center gap-2">
                          <StatusBadge status={test.status} />
                          {isExpanded ? <ChevronDown size={12} style={{ color: 'var(--text-muted)' }} /> : <ChevronRight size={12} style={{ color: 'var(--text-muted)' }} />}
                        </div>
                      </button>

                      {isExpanded && (
                        <div className="px-3 pb-3 space-y-3">
                          {test.error && (
                            <div className="p-2 rounded text-xs font-mono" style={{ background: 'rgba(239, 68, 68, 0.1)', color: '#f87171' }}>
                              {test.error}
                            </div>
                          )}

                          {steps.length > 0 && (
                            <div>
                              <span className="text-[10px] font-semibold uppercase" style={{ color: 'var(--text-muted)' }}>Steps</span>
                              <div className="mt-1 space-y-1.5">
                                {steps.map((step: Record<string, any>, si: number) => (
                                  <ReportStepCard key={si} step={step} index={si} />
                                ))}
                              </div>
                            </div>
                          )}

                          {triage && (
                            <div className="p-3 rounded-lg" style={{ background: 'color-mix(in oklab, var(--brand) 6%, transparent)', border: '1px solid color-mix(in oklab, var(--brand) 20%, transparent)' }}>
                              <div className="flex items-center gap-2 mb-2">
                                <Zap size={12} style={{ color: 'var(--brand)' }} />
                                <span className="text-[10px] font-semibold uppercase" style={{ color: 'var(--brand)' }}>AI Triage</span>
                                {triage.verdict && <StatusBadge status={triage.verdict} />}
                              </div>
                              {triage.summary && (
                                <p className="text-xs mb-2" style={{ color: 'var(--text-secondary)' }}>{triage.summary}</p>
                              )}
                              {triage.root_cause && (
                                <div className="text-xs">
                                  <span className="font-medium" style={{ color: 'var(--text-muted)' }}>Root Cause: </span>
                                  <span style={{ color: 'var(--text-secondary)' }}>{triage.root_cause}</span>
                                </div>
                              )}
                              {triage.suggestion && (
                                <div className="text-xs mt-1">
                                  <span className="font-medium" style={{ color: 'var(--text-muted)' }}>Suggestion: </span>
                                  <span style={{ color: 'var(--text-secondary)' }}>{triage.suggestion}</span>
                                </div>
                              )}
                            </div>
                          )}
                        </div>
                      )}
                    </div>
                  )
                })}
              </div>
            </>
          ) : (
            <div className="text-center py-12">
              <BarChart3 size={32} className="mx-auto mb-2" style={{ color: 'rgba(245, 158, 11, 0.2)' }} />
              <p className="text-xs" style={{ color: 'var(--text-muted)' }}>No report yet. Run the suite to generate one.</p>
            </div>
          )
        )}
      </div>

      {/* YAML Source Editor */}
      {showYaml && (
        <div className="h-1/2" style={{ borderTop: '1px solid var(--border-color)' }}>
          <YamlDrawer
            content={yamlContent}
            onChange={setYamlContent}
            onClose={() => setShowYaml(false)}
            theme={theme}
            subtitle="Suite definition YAML"
            onSave={handleSaveYaml}
            isSaving={isSaving}
            saveStatus={saveStatus}
          />
        </div>
      )}
    </div>
  )
}
