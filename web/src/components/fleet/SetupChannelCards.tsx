import type { SetupField, SetupProfile } from '../../api/fleetChat'

interface SetupChannelCardsProps {
  profile?: SetupProfile | null
  channelField?: SetupField
  value: string
  onChange: (value: string) => void
}

export default function SetupChannelCards({ profile, channelField, value, onChange }: SetupChannelCardsProps) {
  const options = channelField?.options || []
  const channelTypes = profile?.channel_types

  return (
    <div className="grid gap-2 sm:grid-cols-2">
      {options.map(opt => {
        const meta = channelTypes?.[opt.value]
        const selected = value === opt.value || (!value && opt.value === (channelField?.default as string))
        return (
          <button
            key={opt.value}
            type="button"
            onClick={() => onChange(opt.value)}
            className={`text-left p-3 rounded-lg border transition-colors ${selected ? 'border-primary bg-primary/10' : 'hover:bg-white/5'}`}
            style={{ borderColor: selected ? undefined : 'var(--border-color)' }}
          >
            <div className="text-sm font-medium" style={{ color: selected ? 'var(--brand)' : 'var(--text-primary)' }}>
              {meta?.label || opt.label}
            </div>
            {meta?.description && (
              <p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>{meta.description}</p>
            )}
          </button>
        )
      })}
    </div>
  )
}
