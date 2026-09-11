import { Checkbox } from '@/components/ui/checkbox'

export interface OAuthScopeOption {
  value: string
  label: string
  description: string
}

interface OAuthScopeSelectorProps {
  scopes: OAuthScopeOption[]
  value: string[]
  onChange: (value: string[]) => void
  disabled?: boolean
}

export default function OAuthScopeSelector({ scopes, value, onChange, disabled = false }: OAuthScopeSelectorProps) {
  const selected = new Set(value)
  const toggle = (scope: string, checked: boolean) => {
    const next = new Set(selected)
    if (checked) next.add(scope)
    else next.delete(scope)
    onChange(scopes.filter(option => next.has(option.value)).map(option => option.value))
  }

  return <fieldset disabled={disabled} className="space-y-2">
    <legend className="mb-1 text-sm" style={{ color: 'var(--text-secondary)' }}>Permissions</legend>
    {scopes.map(scope => <label key={scope.value} className="flex items-start gap-3 rounded-lg p-3" style={{ border: '1px solid var(--border-color)', color: 'var(--text-secondary)' }}>
      <Checkbox checked={selected.has(scope.value)} onCheckedChange={checked => toggle(scope.value, checked === true)} disabled={disabled} aria-label={scope.label} />
      <span><span className="block text-sm font-medium" style={{ color: 'var(--text-primary)' }}>{scope.label}</span><span className="block text-xs" style={{ color: 'var(--text-muted)' }}>{scope.description}</span><code className="block mt-1 text-[11px]" style={{ color: 'var(--text-muted)' }}>{scope.value}</code></span>
    </label>)}
  </fieldset>
}
