import { useCallback, useEffect, useState } from 'react'
import { AlertCircle, Copy, FileText, Loader, Save, Trash2 } from 'lucide-react'
import CodeMirror from '@uiw/react-codemirror'
import { yaml } from '@codemirror/lang-yaml'

import {
  cloneSetupProfile,
  deleteSetupProfile,
  fetchSetupProfile,
  fetchSetupProfileYaml,
  saveSetupProfile,
  saveSetupProfileYaml,
} from '../../api/fleetChat'
import type { SetupProfile } from '../../api/fleetChat'
import SetupProfileDialog from './SetupProfileDialog'
import SetupProfileStepEditor from './SetupProfileStepEditor'

interface SetupProfileDetailProps {
  profileKey: string
  theme?: string
  onCloned?: (newKey: string) => void
  onDeleted?: () => void
  onUpdated?: () => void
}

type Tab = 'overview' | 'yaml'

export default function SetupProfileDetail({
  profileKey,
  theme = 'dark',
  onCloned,
  onDeleted,
  onUpdated,
}: SetupProfileDetailProps) {
  const [profile, setProfile] = useState<SetupProfile | null>(null)
  const [source, setSource] = useState<'bundled' | 'custom'>('bundled')
  const [error, setError] = useState<string | null>(null)
  const [tab, setTab] = useState<Tab>('overview')
  const [yamlContent, setYamlContent] = useState('')
  const [yamlDirty, setYamlDirty] = useState(false)
  const [yamlLoading, setYamlLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [saveOk, setSaveOk] = useState(false)
  const [cloneOpen, setCloneOpen] = useState(false)
  const [cloning, setCloning] = useState(false)
  const [cloneError, setCloneError] = useState<string | null>(null)
  const [profileDirty, setProfileDirty] = useState(false)
  const [profileSaving, setProfileSaving] = useState(false)

  const isBundled = source === 'bundled'

  const loadProfile = useCallback(async () => {
    setError(null)
    try {
      const res = await fetchSetupProfile(profileKey)
      setProfile(res.profile)
      setSource(res.source === 'custom' ? 'custom' : 'bundled')
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [profileKey])

  useEffect(() => {
    setProfile(null)
    setTab('overview')
    setYamlContent('')
    setYamlDirty(false)
    setProfileDirty(false)
    loadProfile()
  }, [loadProfile])

  useEffect(() => {
    if (tab !== 'yaml' || isBundled) return
    let cancelled = false
    setYamlLoading(true)
    fetchSetupProfileYaml(profileKey)
      .then(content => {
        if (!cancelled) {
          setYamlContent(content)
          setYamlDirty(false)
        }
      })
      .catch(err => {
        if (!cancelled) setSaveError(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        if (!cancelled) setYamlLoading(false)
      })
    return () => { cancelled = true }
  }, [tab, profileKey, isBundled])

  const handleSaveProfile = async () => {
    if (!profile) return
    setProfileSaving(true)
    setSaveError(null)
    setSaveOk(false)
    try {
      await saveSetupProfile(profileKey, profile)
      setProfileDirty(false)
      setSaveOk(true)
      onUpdated?.()
      setTimeout(() => setSaveOk(false), 2000)
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : String(err))
    } finally {
      setProfileSaving(false)
    }
  }

  const handleSaveYaml = async () => {
    setSaving(true)
    setSaveError(null)
    setSaveOk(false)
    try {
      await saveSetupProfileYaml(profileKey, yamlContent)
      setYamlDirty(false)
      setSaveOk(true)
      await loadProfile()
      onUpdated?.()
      setTimeout(() => setSaveOk(false), 2000)
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  const handleCloneSubmit = async ({ key, name }: { key: string; name: string }) => {
    setCloning(true)
    setCloneError(null)
    try {
      const result = await cloneSetupProfile(profileKey, key, name)
      setCloneOpen(false)
      onCloned?.(result.key)
    } catch (err) {
      setCloneError(err instanceof Error ? err.message : String(err))
    } finally {
      setCloning(false)
    }
  }

  const handleDelete = async () => {
    if (!window.confirm(`Delete setup profile "${profile?.name || profileKey}"?`)) return
    try {
      await deleteSetupProfile(profileKey)
      onDeleted?.()
    } catch (err) {
      alert('Delete failed: ' + (err instanceof Error ? err.message : String(err)))
    }
  }

  if (error) {
    return <p className="text-sm text-red-400 p-6">{error}</p>
  }
  if (!profile) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <Loader size={24} className="animate-spin text-primary" />
      </div>
    )
  }

  return (
    <div className="flex-1 flex flex-col overflow-hidden">
      <div className="px-6 py-4 flex items-center justify-between gap-4" style={{ borderBottom: '1px solid var(--border-color)' }}>
        <div>
          <h1 className="text-xl font-bold" style={{ color: 'var(--text-primary)' }}>{profile.name}</h1>
          {profile.description && (
            <p className="text-sm mt-0.5" style={{ color: 'var(--text-secondary)' }}>{profile.description}</p>
          )}
          <div className="flex gap-2 mt-2">
            <span className="text-xs px-2 py-0.5 rounded font-mono" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>{profile.key}</span>
            {profile.domain && <span className="text-xs px-2 py-0.5 rounded" style={{ background: 'var(--brand-muted)', color: 'var(--brand)' }}>{profile.domain}</span>}
            {isBundled && <span className="text-xs px-2 py-0.5 rounded" style={{ background: 'color-mix(in oklab, var(--brand) 25%, transparent)', color: 'var(--brand)' }}>Bundled</span>}
          </div>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <button
            type="button"
            onClick={() => setCloneOpen(true)}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg border transition-colors hover:bg-primary/10"
            style={{ borderColor: 'var(--border-color)', color: 'var(--text-secondary)' }}
          >
            <Copy size={12} className="text-primary" /> Clone
          </button>
          {!isBundled && (
            <button
              type="button"
              onClick={handleDelete}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg border transition-colors hover:bg-red-500/10"
              style={{ borderColor: 'var(--border-color)', color: 'var(--text-secondary)' }}
            >
              <Trash2 size={12} className="text-red-400" /> Delete
            </button>
          )}
        </div>
      </div>

      <div className="px-6 flex gap-1" style={{ borderBottom: '1px solid var(--border-color)' }}>
        {(['overview', ...(isBundled ? [] : ['yaml'] as Tab[])] as Tab[]).map(t => (
          <button
            key={t}
            type="button"
            onClick={() => setTab(t)}
            className={`px-4 py-2.5 text-xs font-medium capitalize transition-colors border-b-2 -mb-px ${
              tab === t ? 'border-primary text-primary' : 'border-transparent hover:text-primary'
            }`}
            style={{ color: tab === t ? undefined : 'var(--text-muted)' }}
          >
            {t === 'yaml' ? 'YAML Editor' : 'Overview'}
          </button>
        ))}
      </div>

      {isBundled && (
        <div className="mx-6 mt-4 flex items-start gap-2 rounded-lg px-3 py-2 text-xs" style={{ background: 'var(--brand-muted)', color: 'var(--text-secondary)' }}>
          <AlertCircle size={14} className="text-primary shrink-0 mt-0.5" />
          Bundled profiles are read-only. Clone to create an editable copy you can customize and reference from your templates.
        </div>
      )}

      {tab === 'overview' ? (
        <div className="flex-1 min-h-0 flex flex-col overflow-hidden px-6 pt-4 pb-6 space-y-4">
            {profile.intro_prompt && (
              <div className="rounded-lg p-3 text-xs shrink-0" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)', color: 'var(--text-secondary)' }}>
                <p className="font-medium mb-1" style={{ color: 'var(--text-primary)' }}>Session intro</p>
                <p className="whitespace-pre-wrap">{profile.intro_prompt}</p>
              </div>
            )}
            <div className="flex items-center justify-between gap-2 shrink-0">
              <h2 className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>Setup steps ({profile.steps.length})</h2>
              {!isBundled && profileDirty && (
                <button
                  type="button"
                  onClick={handleSaveProfile}
                  disabled={profileSaving}
                  className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg bg-primary hover:bg-primary/90 text-white disabled:opacity-50"
                >
                  {profileSaving ? <Loader size={12} className="animate-spin" /> : <Save size={12} />}
                  Save steps
                </button>
              )}
            </div>
            <SetupProfileStepEditor
              profile={profile}
              readOnly={isBundled}
              onProfileChange={next => { setProfile(next); setProfileDirty(true); setSaveError(null) }}
            />
        </div>
      ) : (
        <div className="flex-1 flex flex-col overflow-hidden">
          <div className="px-6 py-2 flex items-center justify-between gap-2">
            <div className="flex items-center gap-2 text-xs" style={{ color: 'var(--text-muted)' }}>
              <FileText size={12} />
              Edit the profile definition as YAML. Save to apply changes.
            </div>
            <div className="flex items-center gap-2">
              {saveOk && <span className="text-xs text-green-400">Saved</span>}
              {saveError && <span className="text-xs text-red-400 max-w-xs truncate">{saveError}</span>}
              <button
                type="button"
                onClick={handleSaveYaml}
                disabled={saving || yamlLoading || !yamlDirty}
                className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg bg-primary hover:bg-primary/90 text-white transition-colors disabled:opacity-50"
              >
                {saving ? <Loader size={12} className="animate-spin" /> : <Save size={12} />}
                {saving ? 'Saving…' : 'Save YAML'}
              </button>
            </div>
          </div>
          <div className="flex-1 overflow-hidden px-6 pb-6">
            {yamlLoading ? (
              <div className="flex items-center justify-center h-full">
                <Loader size={24} className="animate-spin text-primary" />
              </div>
            ) : (
              <div className="h-full rounded-lg overflow-hidden border" style={{ borderColor: 'var(--border-color)' }}>
                <CodeMirror
                  value={yamlContent}
                  onChange={(value) => { setYamlContent(value); setYamlDirty(true); setSaveError(null) }}
                  height="100%"
                  extensions={[yaml()]}
                  theme={theme === 'dark' ? 'dark' : 'light'}
                  className="h-full text-sm"
                  basicSetup={{ lineNumbers: true, highlightActiveLine: true, foldGutter: true }}
                />
              </div>
            )}
          </div>
        </div>
      )}

      <SetupProfileDialog
        isOpen={cloneOpen}
        mode="clone"
        sourceName={profile.name}
        sourceKey={profileKey}
        submitting={cloning}
        error={cloneError}
        onClose={() => { if (!cloning) { setCloneOpen(false); setCloneError(null) } }}
        onSubmit={handleCloneSubmit}
      />
    </div>
  )
}
