import { useCallback, useEffect, useState } from 'react'
import { ChevronLeft, ChevronRight, Clapperboard, Film, Monitor, User } from 'lucide-react'
import ArtifactMediaPlayer from './ArtifactMediaPlayer'
import type { TutorialSceneSlideshowMessage } from './chatTypes'

interface TutorialSceneSlideshowCardProps {
  data: TutorialSceneSlideshowMessage
  sessionId?: string | null
  /** When true, use full parent width (harness panel) instead of max-w-4xl. */
  fillWidth?: boolean
}

function kindMeta(kind: string): { label: string; icon: typeof User; color: string } {
  switch (kind) {
    case 'avatar':
      return { label: 'A-Roll Scene', icon: User, color: 'var(--brand)' }
    case 'broll':
      return { label: 'B-Roll Scene', icon: Film, color: 'rgb(234, 179, 8)' }
    case 'screen':
      return { label: 'Screen Recording', icon: Monitor, color: 'rgb(34, 197, 94)' }
    default:
      return { label: kind || 'Scene', icon: Clapperboard, color: 'var(--text-muted)' }
  }
}

export default function TutorialSceneSlideshowCard({
  data,
  sessionId,
  fillWidth = false,
}: TutorialSceneSlideshowCardProps) {
  const scenes = data.scenes || []
  const [index, setIndex] = useState(0)

  const clampedIndex = scenes.length === 0 ? 0 : Math.min(index, scenes.length - 1)
  const scene = scenes[clampedIndex]
  const meta = scene ? kindMeta(scene.visual_kind) : kindMeta('screen')
  const Icon = meta.icon
  const voiceover = scene?.voiceover || scene?.narration || ''
  const isScreen = scene?.visual_kind === 'screen'
  const hasVideo = isScreen && !!scene?.path

  const goPrev = useCallback(() => {
    setIndex(i => Math.max(0, i - 1))
  }, [])
  const goNext = useCallback(() => {
    setIndex(i => Math.min(scenes.length - 1, i + 1))
  }, [scenes.length])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'ArrowLeft') goPrev()
      if (e.key === 'ArrowRight') goNext()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [goPrev, goNext])

  if (scenes.length === 0) {
    return (
      <div className="my-3 rounded-xl px-4 py-3 text-sm" style={{ border: '1px solid var(--border-color)', background: 'var(--bg-secondary)', color: 'var(--text-muted)' }}>
        No tutorial scenes in manifest.
      </div>
    )
  }

  return (
    <div
      className={fillWidth ? 'rounded-xl overflow-hidden w-full' : 'my-3 rounded-xl overflow-hidden w-full max-w-4xl'}
      style={{ border: '1px solid var(--border-color)', background: 'var(--bg-secondary)', boxShadow: 'var(--shadow-soft)' }}
    >
      <div className="px-4 py-3 flex items-center gap-2" style={{ borderBottom: '1px solid var(--border-color)' }}>
        <Clapperboard size={16} style={{ color: 'var(--brand)' }} />
        <div className="min-w-0 flex-1">
          <div className="text-sm font-semibold truncate" style={{ color: 'var(--text-primary)' }}>
            {data.title || data.drill || 'Tutorial scenes'}
          </div>
          <div className="text-[11px]" style={{ color: 'var(--text-muted)' }}>
            Suite: {data.suite || '—'} · Scene {clampedIndex + 1} of {scenes.length}
          </div>
        </div>
      </div>

      <div className="p-4 space-y-3">
        <div className="flex items-center gap-2 text-xs font-medium" style={{ color: meta.color }}>
          <Icon size={14} />
          {meta.label}
          {scene?.id ? (
            <span className="font-normal" style={{ color: 'var(--text-muted)' }}>
              · {scene.id}
            </span>
          ) : null}
        </div>

        {hasVideo ? (
          <ArtifactMediaPlayer
            path={scene.path!}
            fileName={scene.path!.split('/').pop() || 'scene.mp4'}
            kind="video"
            sessionId={sessionId}
          />
        ) : (
          <div
            className="rounded-lg flex flex-col items-center justify-center text-center px-6 py-12 min-h-[180px]"
            style={{ background: 'var(--bg-tertiary)', border: '1px dashed var(--border-color)' }}
          >
            <Icon size={32} style={{ color: meta.color, opacity: 0.85 }} />
            <div className="mt-3 text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>
              {meta.label}
            </div>
            {scene?.visual_description ? (
              <p className="mt-2 text-xs max-w-md" style={{ color: 'var(--text-secondary)' }}>
                {scene.visual_description}
              </p>
            ) : null}
            {isScreen && !scene?.path ? (
              <p className="mt-2 text-[11px]" style={{ color: 'var(--text-muted)' }}>
                Screen clip not recorded yet.
              </p>
            ) : null}
          </div>
        )}

        {voiceover ? (
          <div className="rounded-lg px-3 py-2.5" style={{ background: 'var(--bg-tertiary)' }}>
            <div className="text-[10px] uppercase tracking-wide mb-1" style={{ color: 'var(--text-muted)' }}>
              Narration
            </div>
            <p className="text-sm leading-relaxed" style={{ color: 'var(--text-secondary)' }}>
              {voiceover}
            </p>
          </div>
        ) : null}
      </div>

      <div
        className="px-4 py-3 flex items-center gap-3 flex-wrap"
        style={{ borderTop: '1px solid var(--border-color)' }}
      >
        <button
          type="button"
          onClick={goPrev}
          disabled={clampedIndex === 0}
          className="flex items-center gap-1 px-2.5 py-1.5 rounded-lg text-xs font-medium transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
          style={{ background: 'var(--surface-muted)', border: '1px solid var(--border-color)', color: 'var(--text-secondary)' }}
        >
          <ChevronLeft size={14} />
          Prev
        </button>
        <div className="flex items-center gap-1.5 flex-1 justify-center flex-wrap">
          {scenes.map((sc, i) => (
            <button
              key={sc.id || i}
              type="button"
              onClick={() => setIndex(i)}
              className="rounded-full transition-all cursor-pointer"
              style={{
                width: i === clampedIndex ? 10 : 8,
                height: i === clampedIndex ? 10 : 8,
                background: i === clampedIndex ? 'var(--accent)' : 'var(--border-color)',
              }}
              aria-label={`Scene ${i + 1}`}
            />
          ))}
        </div>
        <button
          type="button"
          onClick={goNext}
          disabled={clampedIndex >= scenes.length - 1}
          className="flex items-center gap-1 px-2.5 py-1.5 rounded-lg text-xs font-medium transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
          style={{ background: 'var(--surface-muted)', border: '1px solid var(--border-color)', color: 'var(--text-secondary)' }}
        >
          Next
          <ChevronRight size={14} />
        </button>
      </div>
    </div>
  )
}
