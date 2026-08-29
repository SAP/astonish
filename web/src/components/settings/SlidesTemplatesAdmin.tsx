import { useCallback, useEffect, useRef, useState } from 'react'
import { FileUp, Presentation, Trash2 } from 'lucide-react'

import {
  deleteSlidesTemplate,
  importSlidesTemplate,
  listSlidesTemplates,
  type DocsScope,
  type SlidesTemplate,
} from '@/api/slides'
import QuestionOptionThumb from '@/components/chat/questions/QuestionOptionThumb'
import ThumbnailFrame from '@/components/chat/questions/ThumbnailFrame'
import { templateCoverThumbnail } from '@/components/slides/templateCover'

interface SlidesTemplatesAdminProps {
  scope: Exclude<DocsScope, 'personal'>
  theme?: string
}

const SCOPE_LABEL: Record<string, string> = {
  platform: 'Platform',
  org: 'Organization',
  team: 'Team',
}

/**
 * Admin catalog for importing PPTX templates at platform / org / team.
 * Delete applies only to templates owned at this scope.
 */
export default function SlidesTemplatesAdmin({ scope }: SlidesTemplatesAdminProps) {
  const [templates, setTemplates] = useState<SlidesTemplate[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [importing, setImporting] = useState(false)
  const [error, setError] = useState('')
  const [deleteConfirm, setDeleteConfirm] = useState<SlidesTemplate | null>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const load = useCallback(async () => {
    try {
      const data = await listSlidesTemplates(scope)
      setTemplates(data.templates || [])
      setError('')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load templates')
    } finally {
      setLoading(false)
    }
  }, [scope])

  useEffect(() => { void load() }, [load])

  const handleImport = useCallback(async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file) return
    setImporting(true)
    setError('')
    try {
      await importSlidesTemplate(file, scope)
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to import template')
    } finally {
      setImporting(false)
    }
  }, [load, scope])

  const handleDelete = useCallback(async (tpl: SlidesTemplate) => {
    setBusy(true)
    setError('')
    try {
      await deleteSlidesTemplate(tpl.name, scope)
      setDeleteConfirm(null)
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete template')
    } finally {
      setBusy(false)
    }
  }, [load, scope])

  const label = SCOPE_LABEL[scope] || scope

  return (
    <div className="flex h-full flex-col gap-4" data-testid="slides-templates-admin">
      <div>
        <h2 className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>Slides Templates</h2>
        <p className="mt-1 text-xs" style={{ color: 'var(--text-muted)' }}>
          Import a .pptx for {label.toLowerCase()} members. Everyone at this level can use it in chat and the Templates library.
        </p>
      </div>

      {error ? (
        <div className="rounded-md px-3 py-2 text-xs text-red-400" style={{ border: '1px solid var(--border-color)' }} role="alert">
          {error}
        </div>
      ) : null}

      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={() => fileInputRef.current?.click()}
          disabled={importing || busy}
          className="inline-flex cursor-pointer items-center gap-1.5 rounded-md px-2.5 py-1.5 text-xs disabled:opacity-60"
          style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)', color: 'var(--text-secondary)' }}
          data-testid="slides-templates-admin-import"
        >
          <FileUp size={14} />
          {importing ? 'Importing…' : 'Import .pptx'}
        </button>
        <input
          ref={fileInputRef}
          type="file"
          accept=".pptx,application/vnd.openxmlformats-officedocument.presentationml.presentation"
          className="hidden"
          onChange={e => { void handleImport(e) }}
          data-testid="slides-templates-admin-input"
        />
        <span className="text-xs" style={{ color: 'var(--text-muted)' }}>{templates.length} template{templates.length === 1 ? '' : 's'}</span>
      </div>

      {loading ? (
        <div className="text-sm" style={{ color: 'var(--text-muted)' }}>Loading…</div>
      ) : templates.length === 0 ? (
        <div className="rounded-lg p-6 text-sm" style={{ border: '1px dashed var(--border-color)', color: 'var(--text-muted)' }}>
          No {label.toLowerCase()} templates yet. Import a .pptx to share one.
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {templates.map(tpl => {
            const title = tpl.label || tpl.name
            const thumbnail = templateCoverThumbnail(tpl)
            return (
              <div
                key={`${tpl.scope}-${tpl.name}`}
                className="flex flex-col gap-3 rounded-xl p-3"
                style={{ border: '1px solid var(--border-color)', background: 'var(--bg-secondary)' }}
                data-testid="slides-templates-admin-card"
              >
                <ThumbnailFrame>
                  {thumbnail ? (
                    <QuestionOptionThumb thumbnail={thumbnail} label={title} />
                  ) : (
                    <div className="flex h-full w-full items-center justify-center" aria-hidden="true">
                      <Presentation size={20} style={{ color: 'var(--text-muted)' }} />
                    </div>
                  )}
                </ThumbnailFrame>
                <div className="flex items-start justify-between gap-2">
                  <span className="min-w-0 truncate text-sm font-medium" style={{ color: 'var(--text-primary)' }}>{title}</span>
                  <span className="rounded-full px-1.5 py-0.5 text-[10px] font-medium" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
                    {label}
                  </span>
                </div>
                <button
                  type="button"
                  onClick={() => setDeleteConfirm(tpl)}
                  disabled={busy}
                  className="ml-auto flex cursor-pointer items-center gap-1 rounded px-2 py-1 text-[11px] disabled:opacity-60"
                  style={{ color: '#f87171', background: 'rgba(239, 68, 68, 0.1)' }}
                  data-testid="slides-templates-admin-delete"
                >
                  <Trash2 size={11} /> Delete
                </button>
              </div>
            )
          })}
        </div>
      )}

      {deleteConfirm ? (
        <div className="fixed inset-0 z-50 flex items-center justify-center" style={{ background: 'rgba(0,0,0,0.5)' }}>
          <div className="mx-4 w-full max-w-sm rounded-xl p-5" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
            <p className="text-sm" style={{ color: 'var(--text-primary)' }}>
              Delete “{deleteConfirm.label || deleteConfirm.name}” from {label.toLowerCase()} templates?
            </p>
            <div className="mt-4 flex justify-end gap-2">
              <button type="button" onClick={() => setDeleteConfirm(null)} className="rounded px-3 py-1.5 text-xs" style={{ color: 'var(--text-secondary)' }}>
                Cancel
              </button>
              <button
                type="button"
                onClick={() => { void handleDelete(deleteConfirm) }}
                className="rounded px-3 py-1.5 text-xs font-medium text-white"
                style={{ background: '#dc2626' }}
                data-testid="slides-templates-admin-delete-confirm"
              >
                Delete
              </button>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  )
}
