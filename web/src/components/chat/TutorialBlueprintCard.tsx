import { Check, RefreshCw, X, Clapperboard, User, Film, Monitor } from 'lucide-react'

export interface TutorialBlueprintSceneRow {
  id: string
  title: string
  voiceover: string
  visual_kind: string
  visual_description: string
  duration_hint_s?: number
}

export interface TutorialBlueprintPreviewMessage {
  type: 'tutorial_blueprint_preview'
  title: string
  suite: string
  blueprintYaml: string
  scenes: TutorialBlueprintSceneRow[]
}

export interface TutorialBlueprintApprovedMessage {
  type: 'tutorial_blueprint_approved'
  title: string
  suite: string
  blueprintYaml: string
  drillYaml?: string
  drillName?: string
  content?: string
}

interface TutorialBlueprintCardProps {
  data: TutorialBlueprintPreviewMessage
  isActive?: boolean
  onApprove: () => void
  onRequestChanges: () => void
  onCancel: () => void
}

function kindMeta(kind: string): { label: string; icon: typeof User; color: string } {
  switch (kind) {
    case 'avatar':
      return { label: 'A Roll', icon: User, color: 'var(--brand)' }
    case 'broll':
      return { label: 'B Roll', icon: Film, color: 'rgb(234, 179, 8)' }
    case 'screen':
      return { label: 'Screen', icon: Monitor, color: 'rgb(34, 197, 94)' }
    default:
      return { label: kind || 'Visual', icon: Clapperboard, color: 'var(--text-muted)' }
  }
}

export default function TutorialBlueprintCard({
  data,
  isActive = false,
  onApprove,
  onRequestChanges,
  onCancel,
}: TutorialBlueprintCardProps) {
  const scenes = data.scenes || []

  return (
    <div
      className="my-3 rounded-xl overflow-hidden w-full max-w-4xl"
      style={{ border: '1px solid var(--border-color)', background: 'var(--bg-secondary)', boxShadow: 'var(--shadow-soft)' }}
    >
      <div className="px-4 py-3 flex items-center gap-2" style={{ borderBottom: '1px solid var(--border-color)' }}>
        <Clapperboard size={16} style={{ color: 'var(--brand)' }} />
        <div className="min-w-0">
          <div className="text-sm font-semibold truncate" style={{ color: 'var(--text-primary)' }}>
            {data.title || 'Tutorial blueprint'}
          </div>
          <div className="text-[11px]" style={{ color: 'var(--text-muted)' }}>
            Suite: {data.suite || '—'} · {scenes.length} scene{scenes.length === 1 ? '' : 's'} · approve before generating screen clips
          </div>
        </div>
      </div>

      <div className="overflow-x-auto">
        <table className="w-full text-left text-sm border-collapse">
          <thead>
            <tr style={{ color: 'var(--text-muted)' }}>
              <th className="px-3 py-2 font-medium w-[18%]">Scene</th>
              <th className="px-3 py-2 font-medium w-[42%]">Voiceover</th>
              <th className="px-3 py-2 font-medium">Visual</th>
            </tr>
          </thead>
          <tbody>
            {scenes.map((sc, i) => {
              const meta = kindMeta(sc.visual_kind)
              const Icon = meta.icon
              const zebra = i % 2 === 0
              const rowBg = zebra ? 'var(--bg-tertiary)' : 'transparent'
              return (
                <tr key={sc.id || i} className="align-top" style={{ background: rowBg }}>
                  <td className="px-3 py-2 font-medium" style={{ color: 'var(--text-primary)' }}>
                    {sc.title || sc.id}
                    {sc.duration_hint_s ? (
                      <div className="text-[11px] font-normal" style={{ color: 'var(--text-muted)' }}>
                        ~{sc.duration_hint_s}s
                      </div>
                    ) : null}
                    <div className="text-[11px] font-normal flex items-center gap-1 mt-0.5" style={{ color: meta.color }}>
                      <Icon size={11} />
                      {meta.label}
                    </div>
                  </td>
                  <td className="px-3 py-2" style={{ color: 'var(--text-secondary)' }}>
                    {sc.voiceover}
                  </td>
                  <td className="px-3 py-2" style={{ color: 'var(--text-secondary)' }}>
                    <span className="font-medium" style={{ color: meta.color }}>{meta.label}:</span>{' '}
                    {sc.visual_description}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>

      {isActive && (
        <>
          <div className="px-4 py-3 flex items-center gap-2 flex-wrap" style={{ borderTop: '1px solid var(--border-color)' }}>
            <button
              onClick={onApprove}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors cursor-pointer"
              style={{ background: 'var(--brand)', border: '1px solid var(--brand-strong)', color: 'var(--brand-foreground, #fff)' }}
            >
              <Check size={13} />
              Approve &amp; generate
            </button>
            <button
              onClick={onRequestChanges}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors cursor-pointer"
              style={{ background: 'var(--accent-soft)', border: '1px solid var(--border-color)', color: 'var(--brand)' }}
            >
              <RefreshCw size={13} />
              Request changes
            </button>
            <button
              onClick={onCancel}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors cursor-pointer ml-auto"
              style={{ background: 'var(--surface-muted)', border: '1px solid var(--border-color)', color: 'var(--text-muted)' }}
            >
              <X size={13} />
              Cancel
            </button>
          </div>
          <div className="px-4 pb-3">
            <p className="text-[10px]" style={{ color: 'var(--text-muted)' }}>
              Only Screen rows become recorded UI clips. A-roll (avatar) and B-roll stay in the blueprint for a later provider step.
            </p>
          </div>
        </>
      )}
    </div>
  )
}
