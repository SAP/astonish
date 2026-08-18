import { useState, useEffect, useCallback } from 'react'
import { Plus, Trash2, ChevronRight, ChevronDown, RefreshCw, Loader2, AlertCircle, Check, Zap } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import {
  fetchA2AAgents,
  createA2AAgent,
  updateA2AAgent,
  deleteA2AAgent,
  toggleA2AAgent,
  refreshA2AAgent,
  testA2AAgent,
} from '../../api/a2aAgents'
import type { A2AAgentListItem, A2AAgentCreateRequest } from '../../api/a2aAgents'

interface A2AAgentsSettingsProps {
  scope?: 'team' | 'org' | 'platform'
  teamSlug?: string
  theme?: string
  inheritedAgents?: A2AAgentListItem[]
  inheritedLabel?: string
  onRefresh?: () => void
}

const emptyForm: A2AAgentCreateRequest = {
  name: '',
  url: '',
  auth_type: 'none',
  credential_name: '',
  timeout: '',
  headers: {},
}

export default function A2AAgentsSettings({
  scope,
  teamSlug,
  theme,
  inheritedAgents,
  inheritedLabel,
  onRefresh,
}: A2AAgentsSettingsProps) {
  const [agents, setAgents] = useState<A2AAgentListItem[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [expandedAgent, setExpandedAgent] = useState<string | null>(null)
  const [testingAgent, setTestingAgent] = useState<string | null>(null)
  const [testResults, setTestResults] = useState<Record<string, { status: string; message?: string }>>({})
  const [savingAgent, setSavingAgent] = useState<string | null>(null)
  const [editForm, setEditForm] = useState<A2AAgentCreateRequest>({ ...emptyForm })
  const [isNew, setIsNew] = useState(false)

  const loadAgents = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const data = await fetchA2AAgents(scope, teamSlug)
      setAgents(data.agents || [])
    } catch (err: any) {
      setError(err.message || 'Failed to load A2A agents')
    } finally {
      setLoading(false)
    }
  }, [scope, teamSlug])

  useEffect(() => {
    loadAgents()
  }, [loadAgents])

  const handleAddAgent = () => {
    setIsNew(true)
    setEditForm({ ...emptyForm })
    setExpandedAgent('__new__')
  }

  const handleExpand = (agent: A2AAgentListItem) => {
    if (expandedAgent === agent.name) {
      setExpandedAgent(null)
      setIsNew(false)
      return
    }
    setIsNew(false)
    setEditForm({
      name: agent.name,
      url: agent.url,
      auth_type: agent.auth_type || 'none',
      credential_name: agent.credential_name || '',
      timeout: agent.timeout || '',
      headers: agent.headers || {},
    })
    setExpandedAgent(agent.name)
  }

  const handleSave = async () => {
    if (!editForm.name || !editForm.url) {
      setError('Name and URL are required')
      return
    }
    setSavingAgent(editForm.name)
    setError(null)
    try {
      if (isNew) {
        await createA2AAgent(editForm, scope, teamSlug)
      } else {
        await updateA2AAgent(encodeURIComponent(expandedAgent!), editForm, scope, teamSlug)
      }
      setExpandedAgent(null)
      setIsNew(false)
      await loadAgents()
      if (onRefresh) onRefresh()
    } catch (err: any) {
      setError(err.message || 'Failed to save agent')
    } finally {
      setSavingAgent(null)
    }
  }

  const handleDelete = async (agentName: string) => {
    try {
      await deleteA2AAgent(agentName, scope, teamSlug)
      if (expandedAgent === agentName) {
        setExpandedAgent(null)
        setIsNew(false)
      }
      await loadAgents()
      if (onRefresh) onRefresh()
    } catch (err: any) {
      setError(err.message || 'Failed to delete agent')
    }
  }

  const handleToggle = async (agent: A2AAgentListItem) => {
    const newEnabled = !agent.enabled
    // Optimistic update
    setAgents(prev =>
      prev.map(a => (a.name === agent.name ? { ...a, enabled: newEnabled } : a))
    )
    try {
      await toggleA2AAgent(encodeURIComponent(agent.name), newEnabled, scope, teamSlug)
      if (onRefresh) onRefresh()
    } catch (err: any) {
      // Revert on error
      setAgents(prev =>
        prev.map(a => (a.name === agent.name ? { ...a, enabled: agent.enabled } : a))
      )
      setError(err.message || 'Failed to toggle agent')
    }
  }

  const handleTest = async (agentName: string) => {
    setTestingAgent(agentName)
    setTestResults(prev => ({ ...prev, [agentName]: { status: 'testing' } }))
    try {
      const result = await testA2AAgent(agentName, scope, teamSlug)
      setTestResults(prev => ({
        ...prev,
        [agentName]: { status: result.status === 'ok' ? 'success' : 'error', message: result.message },
      }))
    } catch (err: any) {
      setTestResults(prev => ({
        ...prev,
        [agentName]: { status: 'error', message: err.message || 'Connection test failed' },
      }))
    } finally {
      setTestingAgent(null)
    }
  }

  const handleRefreshCard = async (agentName: string) => {
    try {
      await refreshA2AAgent(agentName, scope, teamSlug)
      await loadAgents()
      if (onRefresh) onRefresh()
    } catch (err: any) {
      setError(err.message || 'Failed to refresh agent card')
    }
  }

  const handleHeaderAdd = () => {
    setEditForm(prev => ({
      ...prev,
      headers: { ...prev.headers, '': '' },
    }))
  }

  const handleHeaderRemove = (key: string) => {
    setEditForm(prev => {
      const newHeaders = { ...prev.headers }
      delete newHeaders[key]
      return { ...prev, headers: newHeaders }
    })
  }

  const handleHeaderChange = (oldKey: string, newKey: string, value: string) => {
    setEditForm(prev => {
      const entries = Object.entries(prev.headers || {})
      const newHeaders: Record<string, string> = {}
      for (const [k, v] of entries) {
        if (k === oldKey) {
          newHeaders[newKey] = value
        } else {
          newHeaders[k] = v
        }
      }
      return { ...prev, headers: newHeaders }
    })
  }

  const inputStyle = {
    background: 'var(--input-bg, var(--background))',
    borderColor: 'var(--border)',
    color: 'var(--foreground)',
  }

  const renderAgentRow = (agent: A2AAgentListItem, isInherited = false) => {
    const isExpanded = !isInherited && expandedAgent === agent.name

    return (
      <div
        key={agent.name}
        className="rounded-lg"
        style={{ background: 'var(--card)', border: '1px solid var(--border-color)' }}
      >
        {/* Agent row header */}
        <div className="flex items-center gap-3 px-4 py-3">
          {/* Status dot */}
          <div
            className="w-2.5 h-2.5 rounded-full flex-shrink-0"
            style={{ background: agent.enabled ? '#22c55e' : '#6b7280' }}
          />

          {/* Name */}
          <span className="font-semibold text-sm" style={{ color: 'var(--text-primary)' }}>
            {agent.name}
          </span>

          {/* URL truncated */}
          <span
            className="text-xs truncate max-w-[200px]"
            style={{ color: 'var(--text-muted)' }}
          >
            {agent.url}
          </span>

          {/* Skill count badge */}
          {agent.has_card && agent.skill_count != null && agent.skill_count > 0 && (
            <Badge variant="secondary" className="text-[10px] px-2 py-0.5">
              {agent.skill_count} skill{agent.skill_count !== 1 ? 's' : ''}
            </Badge>
          )}

          {/* Scope badge for inherited */}
          {isInherited && agent.scope && (
            <span
              className="text-[10px] px-2 py-0.5 rounded-full"
              style={{ background: 'var(--brand-muted)', color: 'var(--brand)' }}
            >
              {agent.scope}
            </span>
          )}

          <div className="flex-1" />

          {/* Actions (only for non-inherited) */}
          {!isInherited && (
            <div className="flex items-center gap-1">
              {/* Toggle */}
              <Button
                variant="ghost"
                size="sm"
                className="h-7 px-2 text-xs"
                onClick={(e) => { e.stopPropagation(); handleToggle(agent) }}
              >
                {agent.enabled ? 'Disable' : 'Enable'}
              </Button>

              {/* Expand/collapse chevron */}
              <Button
                variant="ghost"
                size="sm"
                className="h-7 w-7 p-0"
                onClick={() => handleExpand(agent)}
              >
                {isExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
              </Button>

              {/* Delete */}
              <Button
                variant="ghost"
                size="sm"
                className="h-7 w-7 p-0 text-red-400 hover:text-red-300"
                onClick={(e) => { e.stopPropagation(); handleDelete(agent.name) }}
              >
                <Trash2 size={14} />
              </Button>
            </div>
          )}
        </div>

        {/* Expanded edit form */}
        {isExpanded && (
          <div className="px-4 pb-4 space-y-4" style={{ borderTop: '1px solid var(--border-color)' }}>
            <div className="pt-4 space-y-3">
              {/* Name input */}
              <div>
                <label className="text-xs font-medium mb-1 block" style={{ color: 'var(--text-muted)' }}>
                  Name
                </label>
                <input
                  className="w-full rounded-md border px-3 py-2 text-sm"
                  style={inputStyle}
                  value={editForm.name}
                  onChange={(e) => setEditForm(prev => ({ ...prev, name: e.target.value }))}
                  placeholder="Agent name"
                />
              </div>

              {/* URL input */}
              <div>
                <label className="text-xs font-medium mb-1 block" style={{ color: 'var(--text-muted)' }}>
                  URL <span className="text-red-400">*</span>
                </label>
                <input
                  className="w-full rounded-md border px-3 py-2 text-sm"
                  style={inputStyle}
                  value={editForm.url}
                  onChange={(e) => setEditForm(prev => ({ ...prev, url: e.target.value }))}
                  placeholder="https://agent.example.com/.well-known/agent.json"
                  required
                />
              </div>

              {/* Auth Type select */}
              <div>
                <label className="text-xs font-medium mb-1 block" style={{ color: 'var(--text-muted)' }}>
                  Auth Type
                </label>
                <select
                  className="w-full rounded-md border px-3 py-2 text-sm"
                  style={inputStyle}
                  value={editForm.auth_type}
                  onChange={(e) => setEditForm(prev => ({ ...prev, auth_type: e.target.value as any }))}
                >
                  <option value="none">None</option>
                  <option value="bearer">Bearer Token</option>
                  <option value="api_key">API Key</option>
                  <option value="oauth">OAuth</option>
                </select>
              </div>

              {/* Credential Name */}
              <div>
                <label className="text-xs font-medium mb-1 block" style={{ color: 'var(--text-muted)' }}>
                  Credential Name
                </label>
                <input
                  className="w-full rounded-md border px-3 py-2 text-sm"
                  style={inputStyle}
                  value={editForm.credential_name || ''}
                  onChange={(e) => setEditForm(prev => ({ ...prev, credential_name: e.target.value }))}
                  placeholder="Reference to credential store"
                />
              </div>

              {/* Timeout */}
              <div>
                <label className="text-xs font-medium mb-1 block" style={{ color: 'var(--text-muted)' }}>
                  Timeout
                </label>
                <input
                  className="w-full rounded-md border px-3 py-2 text-sm"
                  style={inputStyle}
                  value={editForm.timeout || ''}
                  onChange={(e) => setEditForm(prev => ({ ...prev, timeout: e.target.value }))}
                  placeholder="30s"
                />
              </div>

              {/* Headers section */}
              <div>
                <div className="flex items-center justify-between mb-2">
                  <label className="text-xs font-medium" style={{ color: 'var(--text-muted)' }}>
                    Headers
                  </label>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-6 px-2 text-xs"
                    onClick={handleHeaderAdd}
                  >
                    <Plus size={12} className="mr-1" /> Add
                  </Button>
                </div>
                <div className="space-y-2">
                  {Object.entries(editForm.headers || {}).map(([key, value], idx) => (
                    <div key={idx} className="flex items-center gap-2">
                      <input
                        className="flex-1 rounded-md border px-3 py-1.5 text-sm"
                        style={inputStyle}
                        value={key}
                        onChange={(e) => handleHeaderChange(key, e.target.value, value)}
                        placeholder="Header name"
                      />
                      <input
                        className="flex-1 rounded-md border px-3 py-1.5 text-sm"
                        style={inputStyle}
                        value={value}
                        onChange={(e) => handleHeaderChange(key, key, e.target.value)}
                        placeholder="Value"
                      />
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-7 w-7 p-0 text-red-400 hover:text-red-300"
                        onClick={() => handleHeaderRemove(key)}
                      >
                        <Trash2 size={12} />
                      </Button>
                    </div>
                  ))}
                </div>
              </div>

              {/* Action buttons */}
              <div className="flex items-center gap-2 pt-2">
                <Button
                  size="sm"
                  onClick={handleSave}
                  disabled={savingAgent === editForm.name}
                >
                  {savingAgent === editForm.name ? (
                    <Loader2 size={14} className="mr-1 animate-spin" />
                  ) : (
                    <Check size={14} className="mr-1" />
                  )}
                  Save
                </Button>

                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => handleTest(isNew ? '__new__' : agent.name)}
                  disabled={testingAgent !== null || isNew}
                  title={isNew ? 'Save the agent first to test the connection' : undefined}
                >
                  {testingAgent === (isNew ? '__new__' : agent.name) ? (
                    <Loader2 size={14} className="mr-1 animate-spin" />
                  ) : (
                    <Zap size={14} className="mr-1" />
                  )}
                  Test Connection
                </Button>

                {!isNew && (
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => handleRefreshCard(agent.name)}
                  >
                    <RefreshCw size={14} className="mr-1" />
                    Refresh Card
                  </Button>
                )}
              </div>

              {/* Test result inline */}
              {testResults[isNew ? '__new__' : agent.name] && (
                <div className="flex items-center gap-2 text-xs mt-2">
                  {testResults[isNew ? '__new__' : agent.name].status === 'success' ? (
                    <>
                      <Check size={14} className="text-green-400" />
                      <span className="text-green-400">
                        {testResults[isNew ? '__new__' : agent.name].message || 'Connection successful'}
                      </span>
                    </>
                  ) : testResults[isNew ? '__new__' : agent.name].status === 'testing' ? (
                    <>
                      <Loader2 size={14} className="animate-spin" style={{ color: 'var(--text-muted)' }} />
                      <span style={{ color: 'var(--text-muted)' }}>Testing...</span>
                    </>
                  ) : (
                    <>
                      <AlertCircle size={14} className="text-red-400" />
                      <span className="text-red-400">
                        {testResults[isNew ? '__new__' : agent.name].message || 'Connection failed'}
                      </span>
                    </>
                  )}
                </div>
              )}
            </div>
          </div>
        )}
      </div>
    )
  }

  // Render new agent form
  const renderNewAgentForm = () => {
    if (expandedAgent !== '__new__') return null

    return (
      <div
        className="rounded-lg"
        style={{ background: 'var(--card)', border: '1px solid var(--border-color)' }}
      >
        <div className="flex items-center gap-3 px-4 py-3">
          <div
            className="w-2.5 h-2.5 rounded-full flex-shrink-0"
            style={{ background: '#22c55e' }}
          />
          <span className="font-semibold text-sm" style={{ color: 'var(--text-primary)' }}>
            {editForm.name || 'New Agent'}
          </span>
          <div className="flex-1" />
          <Button
            variant="ghost"
            size="sm"
            className="h-7 w-7 p-0"
            onClick={() => { setExpandedAgent(null); setIsNew(false) }}
          >
            <ChevronDown size={14} />
          </Button>
        </div>

        <div className="px-4 pb-4 space-y-4" style={{ borderTop: '1px solid var(--border-color)' }}>
          <div className="pt-4 space-y-3">
            {/* Name input */}
            <div>
              <label className="text-xs font-medium mb-1 block" style={{ color: 'var(--text-muted)' }}>
                Name
              </label>
              <input
                className="w-full rounded-md border px-3 py-2 text-sm"
                style={inputStyle}
                value={editForm.name}
                onChange={(e) => setEditForm(prev => ({ ...prev, name: e.target.value }))}
                placeholder="Agent name"
              />
            </div>

            {/* URL input */}
            <div>
              <label className="text-xs font-medium mb-1 block" style={{ color: 'var(--text-muted)' }}>
                URL <span className="text-red-400">*</span>
              </label>
              <input
                className="w-full rounded-md border px-3 py-2 text-sm"
                style={inputStyle}
                value={editForm.url}
                onChange={(e) => setEditForm(prev => ({ ...prev, url: e.target.value }))}
                placeholder="https://agent.example.com/.well-known/agent.json"
                required
              />
            </div>

            {/* Auth Type select */}
            <div>
              <label className="text-xs font-medium mb-1 block" style={{ color: 'var(--text-muted)' }}>
                Auth Type
              </label>
              <select
                className="w-full rounded-md border px-3 py-2 text-sm"
                style={inputStyle}
                value={editForm.auth_type}
                onChange={(e) => setEditForm(prev => ({ ...prev, auth_type: e.target.value as any }))}
              >
                <option value="none">None</option>
                <option value="bearer">Bearer Token</option>
                <option value="api_key">API Key</option>
                <option value="oauth">OAuth</option>
              </select>
            </div>

            {/* Credential Name */}
            <div>
              <label className="text-xs font-medium mb-1 block" style={{ color: 'var(--text-muted)' }}>
                Credential Name
              </label>
              <input
                className="w-full rounded-md border px-3 py-2 text-sm"
                style={inputStyle}
                value={editForm.credential_name || ''}
                onChange={(e) => setEditForm(prev => ({ ...prev, credential_name: e.target.value }))}
                placeholder="Reference to credential store"
              />
            </div>

            {/* Timeout */}
            <div>
              <label className="text-xs font-medium mb-1 block" style={{ color: 'var(--text-muted)' }}>
                Timeout
              </label>
              <input
                className="w-full rounded-md border px-3 py-2 text-sm"
                style={inputStyle}
                value={editForm.timeout || ''}
                onChange={(e) => setEditForm(prev => ({ ...prev, timeout: e.target.value }))}
                placeholder="30s"
              />
            </div>

            {/* Headers section */}
            <div>
              <div className="flex items-center justify-between mb-2">
                <label className="text-xs font-medium" style={{ color: 'var(--text-muted)' }}>
                  Headers
                </label>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-6 px-2 text-xs"
                  onClick={handleHeaderAdd}
                >
                  <Plus size={12} className="mr-1" /> Add
                </Button>
              </div>
              <div className="space-y-2">
                {Object.entries(editForm.headers || {}).map(([key, value], idx) => (
                  <div key={idx} className="flex items-center gap-2">
                    <input
                      className="flex-1 rounded-md border px-3 py-1.5 text-sm"
                      style={inputStyle}
                      value={key}
                      onChange={(e) => handleHeaderChange(key, e.target.value, value)}
                      placeholder="Header name"
                    />
                    <input
                      className="flex-1 rounded-md border px-3 py-1.5 text-sm"
                      style={inputStyle}
                      value={value}
                      onChange={(e) => handleHeaderChange(key, key, e.target.value)}
                      placeholder="Value"
                    />
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-7 w-7 p-0 text-red-400 hover:text-red-300"
                      onClick={() => handleHeaderRemove(key)}
                    >
                      <Trash2 size={12} />
                    </Button>
                  </div>
                ))}
              </div>
            </div>

            {/* Action buttons */}
            <div className="flex items-center gap-2 pt-2">
              <Button
                size="sm"
                onClick={handleSave}
                disabled={savingAgent === editForm.name}
              >
                {savingAgent === editForm.name ? (
                  <Loader2 size={14} className="mr-1 animate-spin" />
                ) : (
                  <Check size={14} className="mr-1" />
                )}
                Save
              </Button>

              <Button
                variant="outline"
                size="sm"
                onClick={() => handleTest('__new__')}
                disabled={true}
                title="Save the agent first to test the connection"
              >
                {testingAgent === '__new__' ? (
                  <Loader2 size={14} className="mr-1 animate-spin" />
                ) : (
                  <Zap size={14} className="mr-1" />
                )}
                Test Connection
              </Button>
            </div>

            {/* Test result inline */}
            {testResults['__new__'] && (
              <div className="flex items-center gap-2 text-xs mt-2">
                {testResults['__new__'].status === 'success' ? (
                  <>
                    <Check size={14} className="text-green-400" />
                    <span className="text-green-400">
                      {testResults['__new__'].message || 'Connection successful'}
                    </span>
                  </>
                ) : testResults['__new__'].status === 'testing' ? (
                  <>
                    <Loader2 size={14} className="animate-spin" style={{ color: 'var(--text-muted)' }} />
                    <span style={{ color: 'var(--text-muted)' }}>Testing...</span>
                  </>
                ) : (
                  <>
                    <AlertCircle size={14} className="text-red-400" />
                    <span className="text-red-400">
                      {testResults['__new__'].message || 'Connection failed'}
                    </span>
                  </>
                )}
              </div>
            )}
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-4">
      {/* Header with Add button */}
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>
          A2A Agents
        </h3>
        <Button
          variant="outline"
          size="sm"
          onClick={handleAddAgent}
          disabled={expandedAgent === '__new__'}
        >
          <Plus size={14} className="mr-1" />
          Add Agent
        </Button>
      </div>

      {/* Error display */}
      {error && (
        <div className="flex items-center gap-2 text-xs text-red-400 px-3 py-2 rounded-md" style={{ background: 'var(--card)', border: '1px solid var(--border-color)' }}>
          <AlertCircle size={14} />
          <span>{error}</span>
          <button className="ml-auto text-xs underline" onClick={() => setError(null)}>
            Dismiss
          </button>
        </div>
      )}

      {/* Loading state */}
      {loading && (
        <div className="flex items-center justify-center py-8">
          <Loader2 size={20} className="animate-spin" style={{ color: 'var(--text-muted)' }} />
        </div>
      )}

      {/* Inherited agents section */}
      {inheritedAgents && inheritedAgents.length > 0 && (
        <div className="space-y-2">
          <h4 className="text-xs font-medium" style={{ color: 'var(--text-muted)' }}>
            {inheritedLabel || 'Inherited'}
          </h4>
          {inheritedAgents.map(agent => renderAgentRow(agent, true))}
        </div>
      )}

      {/* New agent form */}
      {renderNewAgentForm()}

      {/* Agent list */}
      {!loading && (
        <div className="space-y-2">
          {agents.map(agent => renderAgentRow(agent, false))}
          {agents.length === 0 && !isNew && (
            <p className="text-xs text-center py-4" style={{ color: 'var(--text-muted)' }}>
              No A2A agents configured. Click "Add Agent" to get started.
            </p>
          )}
        </div>
      )}
    </div>
  )
}
