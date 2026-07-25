import { AlertCircle, CheckCircle2 } from 'lucide-react'
import { errorBg, successBg } from './sharedStyles'
import type { InlineErrorProps, InlineSuccessProps, StatusBadgeProps, RoleBadgeProps } from './sharedStyles'

// ---------------------------------------------------------------------------
// Shared UI components
// ---------------------------------------------------------------------------

export function InlineError({ msg }: InlineErrorProps) {
  if (!msg) return null
  return (
    <div className="flex items-center gap-2 p-3 rounded-lg text-sm" style={errorBg}>
      <AlertCircle size={14} /><span>{msg}</span>
    </div>
  )
}

export function InlineSuccess({ msg }: InlineSuccessProps) {
  if (!msg) return null
  return (
    <div className="flex items-center gap-2 p-3 rounded-lg text-sm" style={successBg}>
      <CheckCircle2 size={14} /><span>{msg}</span>
    </div>
  )
}

export function StatusBadge({ status }: StatusBadgeProps) {
  const isActive = status === 'active'
  const isSuspended = status === 'suspended'
  const color = isActive ? '#22c55e' : isSuspended ? '#f59e0b' : '#ef4444'
  return (
    <span
      className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs font-medium"
      style={{ background: `${color}20`, color }}
    >
      <span className="w-1.5 h-1.5 rounded-full" style={{ background: color }} />
      {status}
    </span>
  )
}

export function RoleBadge({ role }: RoleBadgeProps) {
  const colors: Record<string, { bg: string; fg: string }> = {
    superadmin: { bg: 'rgba(234, 179, 8, 0.15)', fg: '#eab308' },
    owner: { bg: 'var(--brand-muted)', fg: 'var(--brand)' },
    admin: { bg: 'rgba(59, 130, 246, 0.15)', fg: '#3b82f6' },
    member: { bg: 'rgba(107, 114, 128, 0.15)', fg: '#6b7280' },
  }
  const c = colors[role] || colors.member
  return (
    <span className="px-2 py-0.5 rounded-full text-xs font-medium" style={{ background: c.bg, color: c.fg }}>
      {role}
    </span>
  )
}
