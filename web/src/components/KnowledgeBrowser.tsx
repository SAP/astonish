import { useState, useEffect, useCallback, useRef } from 'react'
import { Search, Brain, Plus, Trash2, ArrowUpRight, Loader2, AlertCircle, BookOpen, User, ChevronDown, ChevronUp, Pencil, X, Check, Map, AlertTriangle, RefreshCw, Wand2, HeartPulse } from 'lucide-react'
import {
  searchMemories, listTeamMemories, listOrgMemories, listPersonalMemories,
  saveTeamMemory, savePersonalMemory, saveOrgMemory,
  deleteTeamMemory, deleteOrgMemory, deletePersonalMemory,
  promoteMemoryToOrg, promotePersonalToTeam, updateMemory,
  previewMemoryConsolidation, applyMemoryConsolidation, fetchMemoryHealth,
} from '../api/platform'
import type { MemoryEntry, MemoryMapGroup, MemoryMapResponse, ScenarioCard, MemoryHealthResponse, MemoryRecommendation } from '../api/platform'

interface KnowledgeBrowserProps {
  theme: 'dark' | 'light'
  user: { id: string; email: string; display_name: string; role: string }
  activeTeam?: string | null
}
type Tab = 'personal' | 'team' | 'org' | 'health' | 'add'
const SCOPE_COLORS: Record<string, string> = {
  personal: 'var(--info)',
  team: 'var(--brand)',
  org: 'var(--success)',
}

function ScopeBadge({ scope }: { scope: string }) {
  const label = scope || 'unknown'
  const color = SCOPE_COLORS[label] || 'var(--muted-foreground)'
  return (
    <span
      className="rounded-full border px-2 py-0.5 text-xs font-medium capitalize"
      style={{
        background: `color-mix(in oklab, ${color} 14%, transparent)`,
        color,
        borderColor: `color-mix(in oklab, ${color} 28%, transparent)`,
      }}
    >
      {label}
    </span>
  )
}

interface MemoryCardProps {
  entry: MemoryEntry
  userId: string
  isAdmin: boolean
  currentTab: Tab
  onDelete: (id: string, scope: string) => void
  onPromote?: (id: string, direction: 'to-team' | 'to-org') => void
  onUpdate?: (id: string, scope: string, content: string, category: string) => void
}

function MemoryCard({ entry, userId, isAdmin, currentTab, onDelete, onPromote, onUpdate }: MemoryCardProps) {
  const [expanded, setExpanded] = useState(false)
  const [editing, setEditing] = useState(false)
  const [editContent, setEditContent] = useState(entry.snippet)
  const [editCategory, setEditCategory] = useState(entry.category || '')
  const isLong = entry.snippet.length > 200

  // Determine if user can manage this memory
  const isOwner = entry.created_by === userId
  const canManage = entry.scope === 'personal' || isOwner || (entry.scope === 'team' && isAdmin) || (entry.scope === 'org' && isAdmin)

  // Determine promotion options
  const canPromoteToTeam = entry.scope === 'personal' && currentTab === 'personal'
  const canPromoteToOrg = entry.scope === 'team' && currentTab === 'team' && isAdmin

  const handleSaveEdit = () => {
    if (editContent.trim() && onUpdate) {
      onUpdate(entry.id, entry.scope, editContent.trim(), editCategory.trim())
      setEditing(false)
    }
  }

  const handleCancelEdit = () => {
    setEditContent(entry.snippet)
    setEditCategory(entry.category || '')
    setEditing(false)
  }

  const displayText = expanded || editing ? entry.snippet : (isLong ? entry.snippet.slice(0, 200) + '...' : entry.snippet)

  return (
    <div className="rounded-lg border border-border bg-card p-4 shadow-[var(--shadow-soft)] transition-all">

      {/* Content area */}
      {editing ? (
        <div className="space-y-3 mb-3">
          <textarea
            value={editContent}
            onChange={e => setEditContent(e.target.value)}
            rows={6}
            className="w-full rounded-lg px-3 py-2 text-sm outline-none resize-y"
            style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
          />
          <input
            type="text"
            value={editCategory}
            onChange={e => setEditCategory(e.target.value)}
            placeholder="Category"
            className="w-full rounded-lg px-3 py-2 text-sm outline-none"
            style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
          />
          <div className="flex gap-2">
            <button onClick={handleSaveEdit} className="flex items-center gap-1 px-3 py-1.5 rounded-lg text-xs font-medium text-white" style={{ background: 'var(--brand)' }}>
              <Check size={12} /> Save
            </button>
            <button onClick={handleCancelEdit} className="flex items-center gap-1 px-3 py-1.5 rounded-lg text-xs" style={{ color: 'var(--text-muted)' }}>
              <X size={12} /> Cancel
            </button>
          </div>
        </div>
      ) : (
        <div className="mb-3">
          <p className="text-sm leading-relaxed whitespace-pre-wrap" style={{ color: 'var(--text-primary)' }}>{displayText}</p>
          {isLong && !expanded && (
            <button
              onClick={() => setExpanded(true)}
              className="flex items-center gap-1 mt-2 text-xs font-medium transition-colors hover:opacity-80"
              style={{ color: 'var(--brand)' }}
            >
              <ChevronDown size={14} /> Show more
            </button>
          )}
          {isLong && expanded && (
            <button
              onClick={() => setExpanded(false)}
              className="flex items-center gap-1 mt-2 text-xs font-medium transition-colors hover:opacity-80"
              style={{ color: 'var(--brand)' }}
            >
              <ChevronUp size={14} /> Show less
            </button>
          )}
        </div>
      )}

      {/* Footer with badges and actions */}
      <div className="flex items-center gap-2 flex-wrap">
        <ScopeBadge scope={entry.scope} />
        {entry.category && (
          <span className="px-2 py-0.5 rounded-full text-xs font-medium"
            style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)', border: '1px solid var(--border-color)' }}>
            {entry.category}
          </span>
        )}
        {entry.score != null && entry.score < 1.0 && (
          <span className="text-xs" style={{ color: 'var(--text-muted)' }}>
            {(entry.score * 100).toFixed(0)}% match
          </span>
        )}
        {entry.created_at && (
          <span className="text-xs" style={{ color: 'var(--text-muted)' }}>
            {new Date(entry.created_at).toLocaleDateString()}
          </span>
        )}

        {/* Actions */}
        <div className="flex items-center gap-1 ml-auto">
          {canPromoteToTeam && onPromote && (
            <button
              onClick={() => onPromote(entry.id, 'to-team')}
              title="Promote to Team"
              className="p-1.5 rounded-md transition-colors hover:opacity-80"
              style={{ color: SCOPE_COLORS.team }}
            >
              <ArrowUpRight size={15} />
            </button>
          )}
          {canPromoteToOrg && onPromote && (
            <button
              onClick={() => onPromote(entry.id, 'to-org')}
              title="Promote to Org"
              className="p-1.5 rounded-md transition-colors hover:opacity-80"
              style={{ color: SCOPE_COLORS.org }}
            >
              <ArrowUpRight size={15} />
            </button>
          )}
          {canManage && !editing && (
            <button
              onClick={() => { setEditing(true); setExpanded(true) }}
              title="Edit"
              className="p-1.5 rounded-md transition-colors hover:opacity-80"
              style={{ color: 'var(--text-muted)' }}
            >
              <Pencil size={15} />
            </button>
          )}
          {canManage && (
            <button
              onClick={() => onDelete(entry.id, entry.scope)}
              title="Delete"
              className="p-1.5 rounded-md transition-colors hover:opacity-80"
              style={{ color: 'var(--danger, #ef4444)' }}
            >
              <Trash2 size={15} />
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

const FLAG_STYLES: Record<string, { label: string; color: string }> = {
  duplicate_risk: { label: 'Duplicate risk', color: 'var(--info)' },
  scattered_topic: { label: 'Scattered topic', color: 'var(--warning)' },
  transient_failure_risk: { label: 'Transient failure wording', color: 'var(--warning)' },
  trial_error_risk: { label: 'Trial/error wording', color: 'var(--info)' },
  scenario_card: { label: 'Scenario card', color: 'var(--success)' },
}

function MemoryMapFlagBadge({ type }: { type: string }) {
  const style = FLAG_STYLES[type] || { label: type.replace(/_/g, ' '), color: 'var(--muted-foreground)' }
  return (
    <span
      className="inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium"
      style={{
        background: `color-mix(in oklab, ${style.color} 12%, transparent)`,
        color: style.color,
        borderColor: `color-mix(in oklab, ${style.color} 30%, transparent)`,
      }}
    >
      <AlertTriangle size={12} />
      {style.label}
    </span>
  )
}

function MemoryMapGroupCard({ group, onDraft }: { group: MemoryMapGroup; onDraft: (group: MemoryMapGroup) => void }) {
  const [expanded, setExpanded] = useState(false)
  const preview = group.representative?.snippet || 'No representative memory content available.'
  const memories = group.memories || []
  const scopes = group.scopes || []
  const categories = group.categories || []
  const visibleMemories = expanded ? memories : memories.slice(0, 3)

  return (
    <div className="rounded-xl border border-border bg-card p-4 shadow-[var(--shadow-soft)]">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2 mb-2">
            <h3 className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>{group.title}</h3>
            <span className="rounded-full px-2 py-0.5 text-xs" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
              {group.memory_count} memor{group.memory_count === 1 ? 'y' : 'ies'}
            </span>
          </div>
          <p className="text-xs mb-3" style={{ color: 'var(--text-muted)' }}>
            Key: <code>{group.key}</code>
          </p>
          <p className="text-sm leading-relaxed whitespace-pre-wrap" style={{ color: 'var(--text-secondary)' }}>
            {preview.length > 260 ? `${preview.slice(0, 260)}...` : preview}
          </p>
        </div>
        <button
          onClick={() => onDraft(group)}
          className="flex shrink-0 items-center gap-2 rounded-lg border px-3 py-2 text-xs font-medium transition-colors hover:opacity-80"
          style={{ background: 'var(--bg-tertiary)', color: 'var(--brand)', borderColor: 'var(--border-color)' }}
          disabled={group.has_scenario_card}
          title={group.has_scenario_card ? 'This group already has a scenario card' : 'Draft an efficient successful path card'}
        >
          <Wand2 size={14} />
          {group.has_scenario_card ? 'Card exists' : 'Draft card'}
        </button>
      </div>

      <div className="flex flex-wrap gap-2 mt-4">
        {scopes.map(scope => <ScopeBadge key={scope} scope={scope} />)}
        {categories.map(category => (
          <span key={category} className="rounded-full px-2 py-0.5 text-xs" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)', border: '1px solid var(--border-color)' }}>
            {category}
          </span>
        ))}
      </div>

      {group.flags && group.flags.length > 0 && (
        <div className="flex flex-wrap gap-2 mt-3">
          {group.flags.map(flag => <MemoryMapFlagBadge key={flag.type} type={flag.type} />)}
        </div>
      )}

      {visibleMemories.length > 0 && (
        <div className="mt-4 space-y-2">
          {visibleMemories.map(memory => (
            <div key={memory.id || `${memory.scope}-${memory.created_at}-${memory.snippet.slice(0, 20)}`} className="rounded-lg border p-3" style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}>
              <div className="mb-2 flex flex-wrap items-center gap-2">
                <ScopeBadge scope={memory.scope} />
                {memory.category && <span className="text-xs" style={{ color: 'var(--text-muted)' }}>{memory.category}</span>}
                {memory.session_id && <span className="text-xs" style={{ color: 'var(--text-muted)' }}>session {memory.session_id}</span>}
              </div>
              <p className="text-xs leading-relaxed whitespace-pre-wrap" style={{ color: 'var(--text-secondary)' }}>
                {memory.snippet.length > 220 ? `${memory.snippet.slice(0, 220)}...` : memory.snippet}
              </p>
            </div>
          ))}
        </div>
      )}

      {memories.length > 3 && (
        <button
          onClick={() => setExpanded(v => !v)}
          className="mt-3 flex items-center gap-1 text-xs font-medium transition-colors hover:opacity-80"
          style={{ color: 'var(--brand)' }}
        >
          {expanded ? <ChevronUp size={14} /> : <ChevronDown size={14} />}
          {expanded ? 'Show fewer memories' : `Show all ${memories.length} memories`}
        </button>
      )}
    </div>
  )
}

function ScenarioCardPreview({ card, onChange }: { card: ScenarioCard; onChange: (card: ScenarioCard) => void }) {
  const setField = (field: keyof ScenarioCard, value: any) => onChange({ ...card, [field]: value })
  const setLines = (field: keyof ScenarioCard, value: string) => setField(field, value.split('\n').map(v => v.trim()).filter(Boolean))
  const lines = (values?: string[]) => (values || []).join('\n')

  return (
    <div className="rounded-xl border p-4" style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}>
      <h3 className="text-sm font-semibold mb-3" style={{ color: 'var(--text-primary)' }}>Consolidated scenario card preview</h3>
      <label className="block text-xs font-medium mb-1" style={{ color: 'var(--text-muted)' }}>Title</label>
      <input aria-label="Title" value={card.title} onChange={e => setField('title', e.target.value)} className="mb-3 w-full rounded-lg px-3 py-2 text-sm outline-none" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }} />
      <label className="block text-xs font-medium mb-1" style={{ color: 'var(--text-muted)' }}>Recommended path</label>
      <textarea aria-label="Recommended path" value={lines(card.recommended_recipe)} onChange={e => setLines('recommended_recipe', e.target.value)} rows={5} className="mb-3 w-full rounded-lg px-3 py-2 text-sm outline-none" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }} />
      <label className="block text-xs font-medium mb-1" style={{ color: 'var(--text-muted)' }}>Conditions</label>
      <textarea aria-label="Conditions" value={lines(card.conditions)} onChange={e => setLines('conditions', e.target.value)} rows={3} className="mb-3 w-full rounded-lg px-3 py-2 text-sm outline-none" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }} />
      <label className="block text-xs font-medium mb-1" style={{ color: 'var(--text-muted)' }}>Cautions or conditional failures</label>
      <textarea aria-label="Cautions or conditional failures" value={lines(card.cautions_or_conditional_failures)} onChange={e => setLines('cautions_or_conditional_failures', e.target.value)} rows={3} className="mb-3 w-full rounded-lg px-3 py-2 text-sm outline-none" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }} />
      <label className="block text-xs font-medium mb-1" style={{ color: 'var(--text-muted)' }}>Verification</label>
      <textarea aria-label="Verification" value={lines(card.verification)} onChange={e => setLines('verification', e.target.value)} rows={2} className="w-full rounded-lg px-3 py-2 text-sm outline-none" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }} />
    </div>
  )
}

function MemoryRecommendationCard({ recommendation, onReview }: { recommendation: MemoryRecommendation; onReview: (recommendation: MemoryRecommendation) => void }) {
  const severityColor = recommendation.severity === 'high' ? 'var(--warning)' : recommendation.severity === 'medium' ? 'var(--info)' : 'var(--muted-foreground)'
  const isCleanup = recommendation.type === 'cleanup_raw_sources'
  const isDuplicateMerge = recommendation.type === 'merge_duplicate_scenario_cards'
  return (
    <div className="rounded-xl border border-border bg-card p-4 shadow-[var(--shadow-soft)]">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 flex-1">
          <div className="mb-2 flex flex-wrap items-center gap-2">
            <span className="rounded-full border px-2 py-0.5 text-xs font-medium capitalize" style={{ background: `color-mix(in oklab, ${severityColor} 12%, transparent)`, color: severityColor, borderColor: `color-mix(in oklab, ${severityColor} 30%, transparent)` }}>
              {recommendation.severity}
            </span>
            <ScopeBadge scope={recommendation.target_scope} />
            <span className="text-xs" style={{ color: 'var(--text-muted)' }}>{(recommendation.memory_ids || []).length} source memor{(recommendation.memory_ids || []).length === 1 ? 'y' : 'ies'}</span>
            {isDuplicateMerge && <span className="text-xs" style={{ color: 'var(--text-muted)' }}>{(recommendation.duplicate_card_ids || []).length} duplicate card{(recommendation.duplicate_card_ids || []).length === 1 ? '' : 's'}</span>}
          </div>
          <h3 className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>{recommendation.title}</h3>
          <p className="mt-1 text-sm" style={{ color: 'var(--text-secondary)' }}>{recommendation.description}</p>
          {recommendation.resolver_signals && recommendation.resolver_signals.length > 0 && (
            <div className="mt-3 flex flex-wrap gap-2">
              {recommendation.resolver_signals.slice(0, 4).map(signal => (
                <span key={signal} className="rounded-full border px-2 py-0.5 text-xs" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)', borderColor: 'var(--border-color)' }}>
                  {signal}
                </span>
              ))}
            </div>
          )}
          {recommendation.flags && recommendation.flags.length > 0 && (
            <div className="mt-3 flex flex-wrap gap-2">
              {recommendation.flags.map(flag => <MemoryMapFlagBadge key={flag.type} type={flag.type} />)}
            </div>
          )}
        </div>
        <button onClick={() => onReview(recommendation)} className="flex shrink-0 items-center gap-2 rounded-lg px-3 py-2 text-sm font-medium text-white" style={{ background: 'var(--brand)' }}>
          <Wand2 size={14} /> {isCleanup ? 'Clean up' : isDuplicateMerge ? 'Merge cards' : 'Review'}
        </button>
      </div>
    </div>
  )
}

function MemoryHealthPanel({ health, onRefresh, onReview, showAdvancedMap, onToggleAdvancedMap, onDraftFromMap }: { health: MemoryHealthResponse | null; onRefresh: () => void; onReview: (recommendation: MemoryRecommendation) => void; showAdvancedMap: boolean; onToggleAdvancedMap: () => void; onDraftFromMap: (group: MemoryMapGroup) => void }) {
  if (!health) {
    return (
      <div className="text-center py-12" style={{ color: 'var(--text-muted)' }}>
        <HeartPulse size={40} className="mx-auto mb-3 opacity-30" />
        <p className="text-sm">No memory health evaluation loaded yet.</p>
      </div>
    )
  }
  const recommendations = health.recommendations || []
  const map = health.map || { groups: [], stats: { total_memories: 0, group_count: 0, duplicate_risk_count: 0, scattered_topic_count: 0, transient_risk_count: 0, trial_error_risk_count: 0 } }
  const evaluated = new Date(health.evaluated_at).toLocaleString()
  const expires = new Date(health.expires_at).toLocaleDateString()

  return (
    <div className="space-y-4">
      <div className="rounded-xl border p-4" style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}>
        <div className="flex items-start justify-between gap-4">
          <div>
            <h2 className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>Memory Health</h2>
            <p className="text-sm mt-1" style={{ color: 'var(--text-muted)' }}>
              Astonish checks for memory improvements lazily when this page is opened. Fresh evaluations are reused for five days.
            </p>
            <p className="text-xs mt-2" style={{ color: 'var(--text-muted)' }}>
              Last analyzed: {evaluated} · Next refresh after: {expires} · {health.generated ? 'Generated now' : 'Using recent evaluation'}
            </p>
          </div>
          <button onClick={onRefresh} className="flex items-center gap-2 rounded-lg border px-3 py-2 text-sm font-medium transition-colors hover:opacity-80" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)', borderColor: 'var(--border-color)' }}>
            <RefreshCw size={14} /> Reanalyze
          </button>
        </div>
        <div className="mt-4 grid gap-3 sm:grid-cols-3">
          <div className="rounded-lg border p-3" style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)' }}>
            <div className="text-xs" style={{ color: 'var(--text-muted)' }}>Suggestions</div>
            <div className="mt-1 text-xl font-semibold" style={{ color: 'var(--text-primary)' }}>{health.recommendation_count}</div>
          </div>
          <div className="rounded-lg border p-3" style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)' }}>
            <div className="text-xs" style={{ color: 'var(--text-muted)' }}>Memory groups</div>
            <div className="mt-1 text-xl font-semibold" style={{ color: 'var(--text-primary)' }}>{map.stats.group_count}</div>
          </div>
          <div className="rounded-lg border p-3" style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)' }}>
            <div className="text-xs" style={{ color: 'var(--text-muted)' }}>Duplicate risks</div>
            <div className="mt-1 text-xl font-semibold" style={{ color: 'var(--text-primary)' }}>{map.stats.duplicate_risk_count}</div>
          </div>
        </div>
      </div>

      {recommendations.length === 0 ? (
        <div className="rounded-xl border p-8 text-center" style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)', color: 'var(--text-muted)' }}>
          <Check size={36} className="mx-auto mb-3 opacity-40" />
          <p className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>No suggested improvements right now.</p>
          <p className="mt-1 text-sm">Memory already looks organized enough for the current heuristic check.</p>
        </div>
      ) : (
        <div className="space-y-3">
          {recommendations.map(recommendation => <MemoryRecommendationCard key={recommendation.id} recommendation={recommendation} onReview={onReview} />)}
        </div>
      )}

      <div className="rounded-xl border p-4" style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}>
        <button onClick={onToggleAdvancedMap} className="flex items-center gap-2 text-sm font-medium" style={{ color: 'var(--brand)' }}>
          {showAdvancedMap ? <ChevronUp size={14} /> : <ChevronDown size={14} />}
          {showAdvancedMap ? 'Hide advanced Memory Map' : 'Show advanced Memory Map'}
        </button>
        {showAdvancedMap && <div className="mt-4"><MemoryMapPanel report={map} onRefresh={onRefresh} onDraft={onDraftFromMap} /></div>}
      </div>
    </div>
  )
}

function MemoryMapPanel({ report, onRefresh, onDraft }: { report: MemoryMapResponse | null; onRefresh: () => void; onDraft: (group: MemoryMapGroup) => void }) {
  if (!report) {
    return (
      <div className="text-center py-12" style={{ color: 'var(--text-muted)' }}>
        <Map size={40} className="mx-auto mb-3 opacity-30" />
        <p className="text-sm">No memory map loaded yet.</p>
      </div>
    )
  }

  const statCards = [
    ['Total memories', report.stats.total_memories],
    ['Groups', report.stats.group_count],
    ['Duplicate risks', report.stats.duplicate_risk_count],
    ['Scattered topics', report.stats.scattered_topic_count],
    ['Transient wording', report.stats.transient_risk_count],
    ['Trial/error wording', report.stats.trial_error_risk_count],
  ] as const

  return (
    <div className="space-y-4">
      <div className="rounded-xl border p-4" style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}>
        <div className="flex items-start justify-between gap-4 mb-4">
          <div>
            <h2 className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>Memory Map</h2>
            <p className="text-sm mt-1" style={{ color: 'var(--text-muted)' }}>
              Read-only diagnostics for scattered, duplicated, or risky memory wording. Use this to decide what should be consolidated into efficient successful paths.
            </p>
          </div>
          <button
            onClick={onRefresh}
            className="flex items-center gap-2 rounded-lg border px-3 py-2 text-sm font-medium transition-colors hover:opacity-80"
            style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)', borderColor: 'var(--border-color)' }}
          >
            <RefreshCw size={14} /> Refresh
          </button>
        </div>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {statCards.map(([label, value]) => (
            <div key={label} className="rounded-lg border p-3" style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)' }}>
              <div className="text-xs" style={{ color: 'var(--text-muted)' }}>{label}</div>
              <div className="mt-1 text-xl font-semibold" style={{ color: 'var(--text-primary)' }}>{value}</div>
            </div>
          ))}
        </div>
      </div>

      {report.groups.length === 0 ? (
        <div className="text-center py-12" style={{ color: 'var(--text-muted)' }}>
          <Brain size={40} className="mx-auto mb-3 opacity-30" />
          <p className="text-sm">No memory groups found.</p>
        </div>
      ) : (
        <div className="flex flex-col gap-3">
          {report.groups.map(group => <MemoryMapGroupCard key={group.key} group={group} onDraft={onDraft} />)}
        </div>
      )}
    </div>
  )
}

// Main Component
export default function KnowledgeBrowser({ theme, user, activeTeam }: KnowledgeBrowserProps) {
  const isAdmin = user.role === 'admin' || user.role === 'owner'
  const [tab, setTab] = useState<Tab>('personal')
  const [query, setQuery] = useState('')
  const [searchResults, setSearchResults] = useState<MemoryEntry[] | null>(null)
  const [personalEntries, setPersonalEntries] = useState<MemoryEntry[]>([])
  const [teamEntries, setTeamEntries] = useState<MemoryEntry[]>([])
  const [orgEntries, setOrgEntries] = useState<MemoryEntry[]>([])
  const [memoryHealth, setMemoryHealth] = useState<MemoryHealthResponse | null>(null)
  const [showAdvancedMap, setShowAdvancedMap] = useState(false)
  const [draftCard, setDraftCard] = useState<ScenarioCard | null>(null)
  const [draftScope, setDraftScope] = useState<'personal' | 'team' | 'org'>('team')
  const [draftDuplicateCardIDs, setDraftDuplicateCardIDs] = useState<string[]>([])
  const [drafting, setDrafting] = useState(false)
  const [applyingDraft, setApplyingDraft] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [success, setSuccess] = useState<string | null>(null)

  // Add-form state
  const [snippet, setSnippet] = useState('')
  const [category, setCategory] = useState('')
  const [saveScope, setSaveScope] = useState<'personal' | 'team' | 'org'>('personal')
  const [saving, setSaving] = useState(false)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const loadTab = useCallback(async (t: Tab) => {
    if (t === 'add') return
    setLoading(true)
    setError(null)
    try {
      const teamSlug = activeTeam || undefined
      if (t === 'personal') setPersonalEntries(await listPersonalMemories(teamSlug))
      else if (t === 'team') setTeamEntries(await listTeamMemories(teamSlug))
      else if (t === 'org') setOrgEntries(await listOrgMemories(teamSlug))
      else if (t === 'health') setMemoryHealth(await fetchMemoryHealth(500, false, teamSlug))
    } catch (err: any) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }, [activeTeam])

  // Load initial data on mount
  const mountedRef = useRef<boolean | null>(null)
  if (mountedRef.current == null) {
    mountedRef.current = true
    loadTab(tab)
  }

  const switchTab = useCallback((t: Tab) => {
    setTab(t)
    setSuccess(null)
    loadTab(t)
  }, [loadTab])

  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current)
    if (query.length < 3) {
      debounceRef.current = setTimeout(() => setSearchResults(null), 0)
      return () => { if (debounceRef.current) clearTimeout(debounceRef.current) }
    }
    debounceRef.current = setTimeout(async () => {
      setLoading(true)
      setError(null)
      try {
        setSearchResults(await searchMemories(query, 20, activeTeam || undefined))
      } catch (err: any) {
        setError(err.message)
      } finally {
        setLoading(false)
      }
    }, 300)
    return () => { if (debounceRef.current) clearTimeout(debounceRef.current) }
  }, [query, activeTeam])

  const handleDelete = useCallback(async (id: string, scope: string) => {
    if (!confirm('Delete this memory permanently?')) return
    setError(null)
    try {
      const teamSlug = activeTeam || undefined
      if (scope === 'personal') await deletePersonalMemory(id, teamSlug)
      else if (scope === 'team') await deleteTeamMemory(id, teamSlug)
      else if (scope === 'org') await deleteOrgMemory(id, teamSlug)
      // Remove from local state
      if (searchResults) {
        setSearchResults(prev => prev ? prev.filter(e => e.id !== id) : null)
      }
      setSuccess('Memory deleted')
      loadTab(tab)
    } catch (err: any) {
      setError(err.message)
    }
  }, [searchResults, tab, loadTab, activeTeam])

  const handlePromote = useCallback(async (id: string, direction: 'to-team' | 'to-org') => {
    const label = direction === 'to-team' ? 'team' : 'organization'
    if (!confirm(`Promote this memory to ${label}? It will be merged into a scenario card when possible. The source memory is kept as provenance.`)) return
    setError(null)
    try {
      const teamSlug = activeTeam || undefined
      if (direction === 'to-team') await promotePersonalToTeam(id, teamSlug)
      else await promoteMemoryToOrg(id, teamSlug)
      setSuccess(`Memory promoted to ${label}`)
      loadTab(tab)
    } catch (err: any) {
      setError(err.message)
    }
  }, [tab, loadTab, activeTeam])

  const handleUpdate = useCallback(async (id: string, scope: string, content: string, cat: string) => {
    setError(null)
    try {
      await updateMemory(scope, id, content, cat, activeTeam || undefined)
      setSuccess('Memory updated')
      loadTab(tab)
    } catch (err: any) {
      setError(err.message)
    }
  }, [tab, loadTab, activeTeam])

  const handleDraftScenarioCard = useCallback(async (group: MemoryMapGroup) => {
    setDrafting(true)
    setError(null)
    try {
      const preview = await previewMemoryConsolidation(group.key, draftScope, (group.memories || []).map(m => m.id).filter(Boolean), activeTeam || undefined)
      setDraftCard(preview.card)
      setDraftScope((preview.card.scope as 'personal' | 'team' | 'org') || draftScope)
      setDraftDuplicateCardIDs([])
    } catch (err: any) {
      setError(err.message)
    } finally {
      setDrafting(false)
    }
  }, [activeTeam, draftScope])

  const handleReviewRecommendation = useCallback((recommendation: MemoryRecommendation) => {
    setDraftCard(recommendation.card)
    setDraftScope(recommendation.target_scope)
    setDraftDuplicateCardIDs(recommendation.duplicate_card_ids || [])
    setError(null)
    setSuccess(null)
  }, [])

  const handleRefreshHealth = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setMemoryHealth(await fetchMemoryHealth(500, true, activeTeam || undefined))
    } catch (err: any) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }, [activeTeam])

  const handleApplyScenarioCard = useCallback(async () => {
    if (!draftCard) return
    setApplyingDraft(true)
    setError(null)
    try {
      const saved = await applyMemoryConsolidation({ ...draftCard, scope: draftScope }, draftScope, activeTeam || undefined, draftDuplicateCardIDs)
      const deletedText = saved.deleted_sources ? ` and deleted ${saved.deleted_sources} source memor${saved.deleted_sources === 1 ? 'y' : 'ies'}` : ''
      const removedDuplicateCount = saved.deleted_duplicate_cards ?? draftDuplicateCardIDs.length
      const duplicateText = removedDuplicateCount ? ` and removed ${removedDuplicateCount} duplicate card${removedDuplicateCount === 1 ? '' : 's'}` : ''
      setSuccess(`Scenario card ${saved.action}${deletedText}${duplicateText}`)
      setDraftCard(null)
      setDraftDuplicateCardIDs([])
      if (tab === 'health') {
        setMemoryHealth(await fetchMemoryHealth(500, true, activeTeam || undefined))
      } else {
        loadTab(tab)
      }
    } catch (err: any) {
      setError(err.message)
    } finally {
      setApplyingDraft(false)
    }
  }, [activeTeam, draftCard, draftScope, draftDuplicateCardIDs, loadTab, tab])

  const handleSave = useCallback(async () => {
    if (!snippet.trim()) return
    setSaving(true)
    setError(null)
    try {
      const cat = category.trim() || 'general'
      const teamSlug = activeTeam || undefined
      if (saveScope === 'team') await saveTeamMemory(snippet, cat, teamSlug)
      else if (saveScope === 'org') await saveOrgMemory(snippet, cat, teamSlug)
      else await savePersonalMemory(snippet, cat, teamSlug)
      setSnippet('')
      setCategory('')
      setSuccess('Memory saved')
      switchTab(saveScope === 'org' ? 'org' : saveScope === 'team' ? 'team' : 'personal')
    } catch (err: any) {
      setError(err.message)
    } finally {
      setSaving(false)
    }
  }, [snippet, category, saveScope, switchTab, activeTeam])

  const entries = tab === 'personal' ? personalEntries : tab === 'team' ? teamEntries : tab === 'org' ? orgEntries : []
  const displayList = searchResults ?? entries

  const tabDefs: { key: Tab; label: string; icon: typeof Brain }[] = [
    { key: 'personal', label: 'Personal', icon: User },
    { key: 'team', label: 'Team', icon: Brain },
    { key: 'org', label: 'Organization', icon: BookOpen },
    { key: 'health', label: 'Memory Health', icon: HeartPulse },
    { key: 'add', label: 'Add New', icon: Plus },
  ]

  return (
    <div className="flex flex-col flex-1 overflow-hidden" data-theme={theme}
      style={{ background: 'var(--bg-primary)' }}>
      {/* Header */}
      <div className="px-6 pt-6 pb-4">
        <h1 className="text-2xl font-bold mb-1" style={{ color: 'var(--text-primary)' }}>
          Knowledge Browser
        </h1>
        <p className="text-sm mb-5" style={{ color: 'var(--text-muted)' }}>
          Search and manage memories across personal, team, and org scopes.
        </p>

        {/* Search bar */}
        <div className="relative">
          <Search size={20} className="absolute left-4 top-1/2 -translate-y-1/2"
            style={{ color: 'var(--text-muted)' }} />
          <input
            type="text"
            placeholder="Search across all memory tiers..."
            value={query}
            onChange={e => setQuery(e.target.value)}
            className="w-full pl-12 pr-4 py-3 rounded-xl text-base outline-none transition-colors"
            style={{
              background: 'var(--bg-secondary)',
              color: 'var(--text-primary)',
              border: '1px solid var(--border-color)',
            }}
          />
        </div>
      </div>

      {/* Messages */}
      {error && (
        <div className="mx-6 mb-3 flex items-center gap-2 px-4 py-2 rounded-lg text-sm"
          style={{ background: 'rgba(239,68,68,0.12)', color: 'var(--danger, #ef4444)' }}>
          <AlertCircle size={16} /> {error}
        </div>
      )}
      {success && (
        <div className="mx-6 mb-3 flex items-center gap-2 px-4 py-2 rounded-lg text-sm"
          style={{ background: 'rgba(34,197,94,0.12)', color: '#22c55e' }}>
          <Check size={16} /> {success}
        </div>
      )}

      {/* Tabs (hidden when showing search results) */}
      {!searchResults && (
        <div className="flex gap-1 px-6 mb-4">
          {tabDefs.map(t => {
            const Icon = t.icon
            const active = tab === t.key
            return (
              <button
                key={t.key}
                onClick={() => switchTab(t.key)}
                className="flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium transition-colors"
                style={{
                  background: active
                    ? 'linear-gradient(135deg, var(--brand) 0%, var(--brand-strong) 100%)'
                    : 'var(--bg-secondary)',
                  color: active ? '#fff' : 'var(--text-secondary)',
                  border: `1px solid ${active ? 'transparent' : 'var(--border-color)'}`,
                }}
              >
                <Icon size={16} />
                {t.label}
              </button>
            )
          })}
        </div>
      )}

      {/* Content */}
      <div className="flex-1 overflow-y-auto px-6 pb-6">
        {/* Loading indicator */}
        {loading && (
          <div className="flex items-center justify-center py-12">
            <Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} />
            <span className="ml-2 text-sm" style={{ color: 'var(--text-muted)' }}>Loading...</span>
          </div>
        )}

        {/* Search results header */}
        {searchResults && !loading && (
          <div className="flex items-center justify-between mb-4">
            <span className="text-sm font-medium" style={{ color: 'var(--text-muted)' }}>
              {searchResults.length} result{searchResults.length !== 1 ? 's' : ''} for &quot;{query}&quot;
            </span>
            <button
              onClick={() => { setQuery(''); setSearchResults(null) }}
              className="text-xs px-3 py-1 rounded-md transition-colors"
              style={{ background: 'var(--bg-secondary)', color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}
            >
              Clear search
            </button>
          </div>
        )}

        {/* Memory list (search results or browse tab) */}
        {(searchResults || (tab !== 'add' && tab !== 'health')) && !loading && (
          <div className="flex flex-col gap-3">
            {displayList.length === 0 && (
              <div className="text-center py-12" style={{ color: 'var(--text-muted)' }}>
                <Brain size={40} className="mx-auto mb-3 opacity-30" />
                <p className="text-sm">No memories found.</p>
              </div>
            )}
            {displayList.map(entry => (
              <MemoryCard
                key={entry.id}
                entry={entry}
                userId={user.id}
                isAdmin={isAdmin}
                currentTab={tab}
                onDelete={handleDelete}
                onPromote={handlePromote}
                onUpdate={handleUpdate}
              />
            ))}
          </div>
        )}

        {!searchResults && tab === 'health' && !loading && (
          <div className="space-y-4">
            {drafting && (
              <div className="flex items-center gap-2 rounded-lg border px-4 py-3 text-sm" style={{ background: 'var(--bg-secondary)', color: 'var(--text-muted)', borderColor: 'var(--border-color)' }}>
                <Loader2 size={16} className="animate-spin" /> Drafting scenario card...
              </div>
            )}
            {draftCard && (
              <div className="space-y-3">
                <div className="flex flex-wrap items-center gap-3 rounded-xl border p-4" style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}>
                  <span className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>Save card as</span>
                  {(['personal', 'team', 'org'] as const).map(scope => (
                    <label key={scope} className={`flex items-center gap-2 text-sm ${scope === 'org' && !isAdmin ? 'opacity-40 pointer-events-none' : ''}`} style={{ color: 'var(--text-secondary)' }}>
                      <input type="radio" checked={draftScope === scope} disabled={scope === 'org' && !isAdmin} onChange={() => setDraftScope(scope)} style={{ accentColor: SCOPE_COLORS[scope] }} />
                      <ScopeBadge scope={scope} />
                    </label>
                  ))}
                  <div className="ml-auto flex gap-2">
                    <button onClick={() => { setDraftCard(null); setDraftDuplicateCardIDs([]) }} className="rounded-lg px-3 py-2 text-sm" style={{ color: 'var(--text-muted)' }}>Cancel</button>
                    <button onClick={handleApplyScenarioCard} disabled={applyingDraft} className="flex items-center gap-2 rounded-lg px-3 py-2 text-sm font-medium text-white disabled:opacity-50" style={{ background: 'var(--brand)' }}>
                      {applyingDraft ? <Loader2 size={14} className="animate-spin" /> : <Check size={14} />}
                      Apply recommendation
                    </button>
                  </div>
                </div>
                {draftDuplicateCardIDs.length > 0 && (
                  <div className="rounded-lg border px-4 py-3 text-sm" style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)', color: 'var(--text-secondary)' }}>
                    This will remove {draftDuplicateCardIDs.length} duplicate scenario card{draftDuplicateCardIDs.length === 1 ? '' : 's'} after the merged card is saved.
                  </div>
                )}
                <ScenarioCardPreview card={draftCard} onChange={setDraftCard} />
              </div>
            )}
            <MemoryHealthPanel
              health={memoryHealth}
              onRefresh={handleRefreshHealth}
              onReview={handleReviewRecommendation}
              showAdvancedMap={showAdvancedMap}
              onToggleAdvancedMap={() => setShowAdvancedMap(v => !v)}
              onDraftFromMap={handleDraftScenarioCard}
            />
          </div>
        )}

        {/* Add New tab form */}
        {!searchResults && tab === 'add' && !loading && (
          <div
            className="max-w-lg mx-auto p-6 rounded-xl border"
            style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}
          >
            <h2 className="text-lg font-semibold mb-4" style={{ color: 'var(--text-primary)' }}>
              Save a Memory
            </h2>

            <label className="block text-sm font-medium mb-1" style={{ color: 'var(--text-secondary)' }}>
              Content
            </label>
            <textarea
              rows={5}
              value={snippet}
              onChange={e => setSnippet(e.target.value)}
              placeholder="Paste knowledge, a tip, or any useful text..."
              className="w-full rounded-lg px-3 py-2 text-sm outline-none resize-y mb-4"
              style={{
                background: 'var(--bg-tertiary)',
                color: 'var(--text-primary)',
                border: '1px solid var(--border-color)',
              }}
            />

            <label className="block text-sm font-medium mb-1" style={{ color: 'var(--text-secondary)' }}>
              Category <span style={{ color: 'var(--text-muted)' }}>(optional)</span>
            </label>
            <input
              type="text"
              value={category}
              onChange={e => setCategory(e.target.value)}
              placeholder="general"
              className="w-full rounded-lg px-3 py-2 text-sm outline-none mb-4"
              style={{
                background: 'var(--bg-tertiary)',
                color: 'var(--text-primary)',
                border: '1px solid var(--border-color)',
              }}
            />

            <fieldset className="mb-5">
              <legend className="text-sm font-medium mb-2" style={{ color: 'var(--text-secondary)' }}>
                Scope
              </legend>
              <label className="flex items-center gap-2 mb-2 cursor-pointer">
                <input
                  type="radio"
                  name="scope"
                  checked={saveScope === 'personal'}
                  onChange={() => setSaveScope('personal')}
                  style={{ accentColor: SCOPE_COLORS.personal }}
                />
                <span className="text-sm" style={{ color: 'var(--text-primary)' }}>Save for me only</span>
                <ScopeBadge scope="personal" />
              </label>
              <label className="flex items-center gap-2 mb-2 cursor-pointer">
                <input
                  type="radio"
                  name="scope"
                  checked={saveScope === 'team'}
                  onChange={() => setSaveScope('team')}
                  style={{ accentColor: SCOPE_COLORS.team }}
                />
                <span className="text-sm" style={{ color: 'var(--text-primary)' }}>Share with team</span>
                <ScopeBadge scope="team" />
              </label>
              <label className={`flex items-center gap-2 cursor-pointer ${!isAdmin ? 'opacity-40 pointer-events-none' : ''}`}>
                <input
                  type="radio"
                  name="scope"
                  checked={saveScope === 'org'}
                  onChange={() => setSaveScope('org')}
                  disabled={!isAdmin}
                  style={{ accentColor: SCOPE_COLORS.org }}
                />
                <span className="text-sm" style={{ color: 'var(--text-primary)' }}>Share with organization</span>
                <ScopeBadge scope="org" />
                {!isAdmin && <span className="text-xs" style={{ color: 'var(--text-muted)' }}>(admin only)</span>}
              </label>
            </fieldset>

            <button
              onClick={handleSave}
              disabled={!snippet.trim() || saving}
              className="flex items-center justify-center gap-2 w-full py-2.5 rounded-lg text-sm font-medium text-white transition-opacity disabled:opacity-40"
              style={{ background: 'linear-gradient(135deg, var(--brand) 0%, var(--brand-strong) 100%)' }}
            >
              {saving ? <Loader2 size={16} className="animate-spin" /> : <Plus size={16} />}
              {saving ? 'Saving...' : 'Save Memory'}
            </button>
          </div>
        )}
      </div>
    </div>
  )
}
