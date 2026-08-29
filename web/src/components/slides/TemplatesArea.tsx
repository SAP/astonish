import { useState, useEffect, useCallback, useRef } from 'react'
import { ArrowLeft, Copy, Trash2, Palette, FileUp, Presentation } from 'lucide-react'
import {
  listSlidesTemplates,
  deleteSlidesTemplate,
  duplicateSlidesTemplate,
  recolorSlidesTemplate,
  importSlidesTemplate,
  type SlidesTemplate,
  type DocsScope,
} from '@/api/slides'
import QuestionOptionThumb from '@/components/chat/questions/QuestionOptionThumb'
import ThumbnailFrame from '@/components/chat/questions/ThumbnailFrame'
import { templateCoverThumbnail } from './templateCover'

interface TemplatesAreaProps {
  /** Navigate back to the deck list / other hash routes. */
  onNavigate?: (path: string) => void
  /** Surface a transient toast (reuses the SlidesView toaster). */
  showToast: (message: string, type: 'success' | 'error') => void
}

/** Resolve the effective scope for a template row's mutations. */
function templateScope(tpl: SlidesTemplate): DocsScope {
  return tpl.scope === 'team' ? 'team' : 'personal'
}

function ScopeBadge({ scope }: { scope?: string }) {
  const label =
    scope === 'builtin' ? 'Built-in'
      : scope === 'platform' ? 'Platform'
        : scope === 'org' ? 'Organization'
          : scope === 'team' ? 'Team'
            : 'Personal'
  const palette =
    scope === 'builtin'
      ? { bg: 'var(--bg-tertiary)', fg: 'var(--text-muted)' }
      : scope === 'platform'
        ? { bg: 'rgba(245, 158, 11, 0.15)', fg: '#f59e0b' }
        : scope === 'org'
          ? { bg: 'rgba(59, 130, 246, 0.15)', fg: '#60a5fa' }
          : scope === 'team'
            ? { bg: 'rgba(16, 185, 129, 0.15)', fg: '#34d399' }
            : { bg: 'rgba(99, 102, 241, 0.15)', fg: '#818cf8' }
  return (
    <span
      className="rounded-full px-1.5 py-0.5 text-[10px] font-medium"
      style={{ background: palette.bg, color: palette.fg }}
      data-testid="template-scope-badge"
    >
      {label}
    </span>
  )
}

/** Inline color editor seeded from a template's palette tokens. */
function RecolorForm({
  tokens,
  onSubmit,
  onCancel,
  busy,
}: {
  tokens?: Record<string, string>
  onSubmit: (tokens: Record<string, string>) => void
  onCancel: () => void
  busy: boolean
}) {
  const keys = ['surface', 'ink', 'accent'] as const
  const [values, setValues] = useState<Record<string, string>>({
    surface: tokens?.surface || '#ffffff',
    ink: tokens?.ink || '#111111',
    accent: tokens?.accent || '#2563eb',
  })
  return (
    <div className="mt-3 flex flex-col gap-2 rounded-md p-2" style={{ background: 'var(--bg-tertiary)' }}>
      {keys.map(k => (
        <label key={k} className="flex items-center justify-between gap-2 text-[11px]" style={{ color: 'var(--text-secondary)' }}>
          <span className="capitalize">{k}</span>
          <input
            type="color"
            value={values[k]}
            onChange={e => setValues(v => ({ ...v, [k]: e.target.value }))}
            className="h-6 w-10 cursor-pointer rounded border"
            style={{ borderColor: 'var(--border-color)', background: 'transparent' }}
            data-testid={`template-recolor-${k}`}
          />
        </label>
      ))}
      <div className="mt-1 flex justify-end gap-2">
        <button
          onClick={onCancel}
          disabled={busy}
          className="cursor-pointer rounded px-2 py-1 text-[11px]"
          style={{ color: 'var(--text-secondary)', background: 'var(--bg-secondary)' }}
        >
          Cancel
        </button>
        <button
          onClick={() => onSubmit(values)}
          disabled={busy}
          className="cursor-pointer rounded px-2 py-1 text-[11px] text-white disabled:opacity-60"
          style={{ background: 'var(--accent, #2563eb)' }}
          data-testid="template-recolor-save"
        >
          {busy ? 'Saving…' : 'Save colors'}
        </button>
      </div>
    </div>
  )
}

function TemplateCard({
  tpl,
  onDuplicate,
  onDelete,
  onRecolor,
  busy,
}: {
  tpl: SlidesTemplate
  onDuplicate: (tpl: SlidesTemplate) => void
  onDelete: (tpl: SlidesTemplate) => void
  onRecolor: (tpl: SlidesTemplate, tokens: Record<string, string>) => void
  busy: boolean
}) {
  const isPersonal = !tpl.scope || tpl.scope === 'personal'
  const [editing, setEditing] = useState(false)
  const title = tpl.label || tpl.name
  const thumbnail = templateCoverThumbnail(tpl)

  return (
    <div
      className="flex flex-col gap-3 rounded-xl p-3"
      style={{ border: '1px solid var(--border-color)', background: 'var(--bg-secondary)' }}
      data-testid="template-card"
    >
      <div data-testid="template-cover">
        <ThumbnailFrame>
          {thumbnail ? (
            <QuestionOptionThumb thumbnail={thumbnail} label={title} />
          ) : (
            <div className="flex h-full w-full items-center justify-center" aria-hidden="true">
              <Presentation size={20} style={{ color: 'var(--text-muted)' }} />
            </div>
          )}
        </ThumbnailFrame>
      </div>

      <div className="flex items-start justify-between gap-2">
        <span className="min-w-0 truncate text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
          {title}
        </span>
        <ScopeBadge scope={tpl.scope} />
      </div>

      <div className="mt-auto flex items-center gap-2 pt-2" style={{ borderTop: '1px solid var(--border-color)' }}>
        <button
          onClick={() => onDuplicate(tpl)}
          disabled={busy}
          className="flex cursor-pointer items-center gap-1 rounded px-2 py-1 text-[11px] disabled:opacity-60"
          style={{ color: 'var(--text-secondary)', background: 'var(--bg-tertiary)' }}
          title="Duplicate into a new editable template"
          data-testid="template-duplicate"
        >
          <Copy size={11} /> Duplicate
        </button>
        {isPersonal && (
          <button
            onClick={() => setEditing(v => !v)}
            disabled={busy}
            className="flex cursor-pointer items-center gap-1 rounded px-2 py-1 text-[11px] disabled:opacity-60"
            style={{ color: 'var(--text-secondary)', background: 'var(--bg-tertiary)' }}
            title="Edit palette colors"
            data-testid="template-recolor"
          >
            <Palette size={11} /> Colors
          </button>
        )}
        {isPersonal && (
          <button
            onClick={() => onDelete(tpl)}
            disabled={busy}
            className="ml-auto flex cursor-pointer items-center gap-1 rounded px-2 py-1 text-[11px] disabled:opacity-60"
            style={{ color: '#f87171', background: 'rgba(239, 68, 68, 0.1)' }}
            title="Delete template"
            data-testid="template-delete"
          >
            <Trash2 size={11} /> Delete
          </button>
        )}
      </div>

      {editing && (
        <RecolorForm
          tokens={tpl.tokens}
          busy={busy}
          onCancel={() => setEditing(false)}
          onSubmit={tokens => {
            onRecolor(tpl, tokens)
            setEditing(false)
          }}
        />
      )}
    </div>
  )
}

/**
 * Dedicated Templates management area (deep-linked at #/slides/templates).
 * Each card is a cover thumbnail + name (same visual as the chat template
 * picker), with duplicate (all) and delete/recolor (scoped only).
 */
export default function TemplatesArea({ onNavigate, showToast }: TemplatesAreaProps) {
  const [templates, setTemplates] = useState<SlidesTemplate[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [deleteConfirm, setDeleteConfirm] = useState<SlidesTemplate | null>(null)
  const [importing, setImporting] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const load = useCallback(async () => {
    try {
      const data = await listSlidesTemplates()
      setTemplates(data.templates || [])
    } catch {
      // Best-effort; leave list empty on failure.
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { load() }, [load])

  const notifyUpdated = useCallback(() => {
    window.dispatchEvent(new CustomEvent('astonish:slides-updated'))
  }, [])

  const handleDuplicate = useCallback(async (tpl: SlidesTemplate) => {
    setBusy(true)
    try {
      const { template } = await duplicateSlidesTemplate(tpl.name, { newName: `${tpl.name}-copy` }, 'personal')
      await load()
      notifyUpdated()
      showToast(`Duplicated as "${template.label || template.name}"`, 'success')
    } catch (err) {
      showToast(`Failed to duplicate: ${err instanceof Error ? err.message : 'Unknown error'}`, 'error')
    } finally {
      setBusy(false)
    }
  }, [load, notifyUpdated, showToast])

  const handleDelete = useCallback(async (tpl: SlidesTemplate) => {
    setBusy(true)
    try {
      await deleteSlidesTemplate(tpl.name, templateScope(tpl))
      await load()
      notifyUpdated()
      showToast(`Deleted "${tpl.label || tpl.name}"`, 'success')
    } catch (err) {
      showToast(`Failed to delete: ${err instanceof Error ? err.message : 'Unknown error'}`, 'error')
    } finally {
      setBusy(false)
      setDeleteConfirm(null)
    }
  }, [load, notifyUpdated, showToast])

  const handleRecolor = useCallback(async (tpl: SlidesTemplate, tokens: Record<string, string>) => {
    setBusy(true)
    try {
      await recolorSlidesTemplate(tpl.name, tokens, templateScope(tpl))
      await load()
      notifyUpdated()
      showToast(`Updated colors for "${tpl.label || tpl.name}"`, 'success')
    } catch (err) {
      showToast(`Failed to recolor: ${err instanceof Error ? err.message : 'Unknown error'}`, 'error')
    } finally {
      setBusy(false)
    }
  }, [load, notifyUpdated, showToast])

  const handleImportClick = useCallback(() => {
    fileInputRef.current?.click()
  }, [])

  const handleImportFile = useCallback(async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file) return
    setImporting(true)
    try {
      const { template } = await importSlidesTemplate(file)
      await load()
      notifyUpdated()
      showToast(`Imported template "${template.label || template.name}"`, 'success')
    } catch (err) {
      showToast(`Failed to import template: ${err instanceof Error ? err.message : 'Unknown error'}`, 'error')
    } finally {
      setImporting(false)
    }
  }, [load, notifyUpdated, showToast])

  return (
    <div className="flex-1 overflow-auto p-6" style={{ background: 'var(--bg-primary)' }} data-testid="templates-area">
      <div className="mx-auto max-w-5xl">
        <div className="mb-6 flex items-center gap-3">
          <button
            onClick={() => onNavigate?.('/slides')}
            className="flex cursor-pointer items-center gap-1 rounded px-2 py-1 text-xs transition-colors"
            style={{ color: 'var(--text-secondary)' }}
          >
            <ArrowLeft size={14} /> Back
          </button>
          <h1 className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>Templates</h1>
          <span className="rounded-full px-2 py-0.5 text-xs" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
            {templates.length}
          </span>
          <button
            onClick={handleImportClick}
            disabled={importing}
            className="ml-auto flex cursor-pointer items-center gap-1.5 rounded-md px-2.5 py-1.5 text-xs transition-colors disabled:cursor-default disabled:opacity-60"
            style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)', color: 'var(--text-secondary)' }}
            title="Import a .pptx as a slide template. Re-import an existing file to pick up designed content patterns from its example slides."
            data-testid="template-import-button"
          >
            <FileUp size={14} />
            {importing ? 'Importing…' : 'Import .pptx'}
          </button>
          <input
            ref={fileInputRef}
            type="file"
            accept=".pptx"
            onChange={handleImportFile}
            className="hidden"
            data-testid="template-import-input"
          />
        </div>

        {loading ? (
          <span className="text-sm" style={{ color: 'var(--text-muted)' }}>Loading templates…</span>
        ) : templates.length === 0 ? (
          <p className="text-sm" style={{ color: 'var(--text-muted)' }}>No templates available.</p>
        ) : (
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
            {templates.map(tpl => (
              <TemplateCard
                key={`${tpl.scope || 'builtin'}-${tpl.name}`}
                tpl={tpl}
                busy={busy}
                onDuplicate={handleDuplicate}
                onDelete={t => setDeleteConfirm(t)}
                onRecolor={handleRecolor}
              />
            ))}
          </div>
        )}

        {deleteConfirm && (
          <div className="fixed inset-0 z-50 flex items-center justify-center" style={{ background: 'rgba(0,0,0,0.5)' }}>
            <div className="mx-4 w-full max-w-sm rounded-xl p-6" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
              <h3 className="mb-2 text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>Delete Template</h3>
              <p className="mb-4 text-xs" style={{ color: 'var(--text-muted)' }}>
                Are you sure you want to delete <strong>{deleteConfirm.label || deleteConfirm.name}</strong>? This cannot be undone.
              </p>
              <div className="flex justify-end gap-2">
                <button
                  onClick={() => setDeleteConfirm(null)}
                  className="cursor-pointer rounded px-3 py-1.5 text-xs"
                  style={{ color: 'var(--text-secondary)', background: 'var(--bg-tertiary)' }}
                >
                  Cancel
                </button>
                <button
                  onClick={() => handleDelete(deleteConfirm)}
                  className="cursor-pointer rounded px-3 py-1.5 text-xs text-white"
                  style={{ background: '#ef4444' }}
                  data-testid="template-delete-confirm"
                >
                  Delete
                </button>
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
