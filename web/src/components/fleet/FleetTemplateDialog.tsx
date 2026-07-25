import { useEffect, useRef, useState } from 'react'
import { Copy, Loader, Plus, X } from 'lucide-react'

export type FleetTemplateDialogMode = 'clone' | 'create'

export interface FleetTemplateDialogResult {
  key: string
  name: string
}

interface FleetTemplateDialogProps {
  isOpen: boolean
  mode: FleetTemplateDialogMode
  /** Source template name when cloning. */
  sourceName?: string
  /** Source template key when cloning (used for default key suggestion). */
  sourceKey?: string
  submitting?: boolean
  error?: string | null
  onClose: () => void
  onSubmit: (result: FleetTemplateDialogResult) => void | Promise<void>
}

function slugifyKey(value: string): string {
  return value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9\s_-]/g, '')
    .replace(/[\s_]+/g, '-')
    .replace(/-+/g, '-')
    .replace(/^-|-$/g, '')
}

export default function FleetTemplateDialog({
  isOpen,
  mode,
  sourceName = '',
  sourceKey = '',
  submitting = false,
  error = null,
  onClose,
  onSubmit,
}: FleetTemplateDialogProps) {
  const [name, setName] = useState('')
  const [key, setKey] = useState('')
  const [keyTouched, setKeyTouched] = useState(false)
  const [localError, setLocalError] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (!isOpen) return
    const defaultName = mode === 'clone'
      ? (sourceName ? `${sourceName} Copy` : 'Template Copy')
      : ''
    const defaultKey = mode === 'clone'
      ? (sourceKey ? `${sourceKey}-copy` : slugifyKey(defaultName) || 'template-copy')
      : ''
    setName(defaultName)
    setKey(defaultKey)
    setKeyTouched(false)
    setLocalError('')
    requestAnimationFrame(() => inputRef.current?.focus())
  }, [isOpen, mode, sourceName, sourceKey])

  if (!isOpen) return null

  const handleNameChange = (value: string) => {
    setName(value)
    setLocalError('')
    if (!keyTouched) {
      if (mode === 'create') {
        setKey(slugifyKey(value))
      } else if (mode === 'clone' && value.trim()) {
        setKey(slugifyKey(value) || `${sourceKey || 'template'}-copy`)
      }
    }
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    const trimmedName = name.trim()
    const trimmedKey = key.trim()
    if (!trimmedName) {
      setLocalError('Please enter a template name')
      return
    }
    if (!trimmedKey) {
      setLocalError('Please enter a template key')
      return
    }
    if (!/^[a-z0-9][a-z0-9_-]*$/.test(trimmedKey)) {
      setLocalError('Key must start with a letter or number and use only lowercase letters, numbers, hyphens, or underscores')
      return
    }
    await onSubmit({ key: trimmedKey, name: trimmedName })
  }

  const title = mode === 'clone' ? 'Clone Template' : 'New Template'
  const subtitle = mode === 'clone'
    ? `Create an editable copy of ${sourceName || sourceKey || 'this template'}`
    : 'Start from scratch — a blank custom template you can edit'
  const submitLabel = mode === 'clone' ? 'Clone Template' : 'Create Template'
  const SubmitIcon = mode === 'clone' ? Copy : Plus
  const displayError = localError || error

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center"
      onKeyDown={(e) => { if (e.key === 'Escape' && !submitting) onClose() }}
    >
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={() => { if (!submitting) onClose() }} />

      <div
        className="relative w-full max-w-md mx-4 rounded-2xl shadow-2xl overflow-hidden"
        style={{ background: 'var(--bg-secondary)' }}
      >
        <div
          className="px-6 py-5"
          style={{ background: 'linear-gradient(135deg, var(--brand) 0%, var(--brand-strong) 100%)' }}
        >
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-3">
              <div className="w-10 h-10 rounded-xl bg-white/20 flex items-center justify-center">
                <SubmitIcon size={20} className="text-white" />
              </div>
              <div>
                <h2 className="text-lg font-semibold text-white">{title}</h2>
                <p className="text-sm text-white/70">{subtitle}</p>
              </div>
            </div>
            <button
              type="button"
              onClick={onClose}
              disabled={submitting}
              className="p-2 rounded-lg hover:bg-white/10 transition-colors disabled:opacity-50"
            >
              <X size={20} className="text-white" />
            </button>
          </div>
        </div>

        <form onSubmit={handleSubmit} className="p-6 space-y-5">
          <div>
            <label className="block text-sm font-medium mb-2" style={{ color: 'var(--text-secondary)' }}>
              Template Name <span className="text-red-400">*</span>
            </label>
            <input
              ref={inputRef}
              type="text"
              value={name}
              disabled={submitting}
              onChange={(e) => handleNameChange(e.target.value)}
              placeholder={mode === 'clone' ? 'e.g. Software Dev — Acme' : 'e.g. Incident Response'}
              className="w-full px-4 py-3 rounded-xl border text-base transition-all focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-60"
              style={{
                background: 'var(--bg-primary)',
                borderColor: displayError ? '#EF4444' : 'var(--border-color)',
                color: 'var(--text-primary)',
              }}
            />
          </div>

          <div>
            <label className="block text-sm font-medium mb-2" style={{ color: 'var(--text-secondary)' }}>
              Template Key <span className="text-red-400">*</span>
            </label>
            <input
              type="text"
              value={key}
              disabled={submitting}
              onChange={(e) => {
                setKeyTouched(true)
                setKey(e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, ''))
                setLocalError('')
              }}
              placeholder="e.g. software-dev-acme"
              className="w-full px-4 py-3 rounded-xl border text-base font-mono transition-all focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-60"
              style={{
                background: 'var(--bg-primary)',
                borderColor: displayError ? '#EF4444' : 'var(--border-color)',
                color: 'var(--text-primary)',
              }}
            />
            <p className="text-xs mt-1.5" style={{ color: 'var(--text-muted)' }}>
              Stored as key <code className="px-1 py-0.5 rounded bg-primary/15 text-primary">{key || '…'}</code>
              {' '}— this is the stable ID used by plans and APIs.
            </p>
            {displayError && (
              <p className="text-xs mt-1.5 text-red-400">{displayError}</p>
            )}
          </div>

          <div className="flex gap-3 pt-2">
            <button
              type="button"
              onClick={onClose}
              disabled={submitting}
              className="flex-1 px-4 py-3 rounded-xl text-sm font-medium transition-colors disabled:opacity-50"
              style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={submitting}
              className="flex-1 flex items-center justify-center gap-2 px-4 py-3 rounded-xl text-sm font-medium text-white transition-colors disabled:opacity-50"
              style={{ background: 'linear-gradient(135deg, var(--brand) 0%, var(--brand-strong) 100%)' }}
            >
              {submitting ? <Loader size={18} className="animate-spin" /> : <SubmitIcon size={18} />}
              {submitting ? 'Working…' : submitLabel}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
