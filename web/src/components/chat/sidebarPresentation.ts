// Shared presentation tokens for Bot and Chat navigation (not transcript rendering).
export const sidebarHeaderClass = 'flex items-center justify-between px-[18px] pt-4 pb-3'
export const sidebarHeadingClass = 'text-[13px] font-semibold text-foreground'
export const sidebarActionClass = 'size-7 text-muted-foreground'
export const sidebarRowClass = 'group w-full rounded-[var(--radius-md)] text-left transition-colors'
export const sidebarRowPaddingClass = 'px-3 py-2.5'
export const sidebarTitleClass = 'truncate text-[13px] font-medium text-foreground'
export const sidebarMetadataClass = 'mt-1 flex items-center gap-1.5 text-[11px] text-[color:var(--text-faint)] tabular-nums'
export const sidebarSeparatorClass = 'size-0.5 rounded-full bg-[color:var(--text-faint)] opacity-60'
export const sidebarTrashClass = 'cursor-pointer rounded p-1 text-destructive opacity-0 transition-all group-hover:opacity-100 group-focus-within:opacity-100 focus-visible:opacity-100 focus-visible:outline focus-visible:outline-ring hover:bg-destructive/20'

export function formatTimeAgo(dateStr: string): string {
  const date = new Date(dateStr)
  if (!dateStr || !Number.isFinite(date.getTime())) return ''
  const mins = Math.floor((Date.now() - date.getTime()) / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 7) return `${days}d ago`
  return date.toLocaleDateString()
}
