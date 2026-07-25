import {
  BookOpen,
  Grid3X3,
  MessageSquare,
  Terminal,
  Users,
  Wrench,
  type LucideIcon,
} from 'lucide-react'

interface HomePageProps {
  onSuggestionClick?: (text: string) => void
  userDisplayName?: string
}

const suggestions = [
  'What can you help me with?',
  'Review my GitHub PR',
  'Automate a deploy',
  'Track team goals',
]

const tiles: { title: string; description: string; Icon: LucideIcon; gradient: string }[] = [
  { title: 'Conversation', description: 'Ask, plan, or think out loud.', Icon: MessageSquare, gradient: 'linear-gradient(135deg, var(--brand), var(--brand-strong))' },
  { title: 'Tools & Actions', description: 'Run commands, touch files, browse.', Icon: Wrench, gradient: 'linear-gradient(135deg, var(--accent2), var(--brand-strong))' },
  { title: 'Knowledge', description: 'Your memories, quietly recalled.', Icon: BookOpen, gradient: 'linear-gradient(135deg, var(--accent3), var(--brand))' },
  { title: 'Fleet Plans', description: 'Multi-agent teams that ship.', Icon: Users, gradient: 'linear-gradient(135deg, var(--brand), var(--accent3))' },
  { title: 'Slash Commands', description: '/status, /drill, /help.', Icon: Terminal, gradient: 'linear-gradient(135deg, var(--accent2), var(--brand))' },
  { title: 'Apps & Flows', description: 'Reusable interfaces on tap.', Icon: Grid3X3, gradient: 'linear-gradient(135deg, var(--accent3), var(--accent2))' },
]

function firstName(displayName?: string): string {
  if (!displayName?.trim()) return 'there'
  return displayName.trim().split(/\s+/)[0]
}

/**
 * Theme-aware logo: CSS mask recolors the body only; white eyes are a separate
 * layer (a single mask can't spare white — it only sees alpha).
 */
function ThemedLogo() {
  return (
    <div
      className="mx-auto flex size-[4.5rem] items-center justify-center rounded-[1.25rem] sm:size-24 sm:rounded-[1.5rem]"
      style={{
        background: 'color-mix(in oklab, var(--brand) 16%, var(--card))',
        boxShadow: '0 14px 36px -14px var(--accent-glow)',
        border: '1px solid color-mix(in oklab, var(--brand) 28%, transparent)',
      }}
      aria-hidden
    >
      <div className="relative size-12 sm:size-16">
        {/* Body — brand gradient via mask (body asset has no eyes) */}
        <span
          className="absolute inset-0"
          style={{
            background: 'linear-gradient(135deg, var(--brand), var(--accent2) 55%, var(--accent3))',
            WebkitMaskImage: 'url(/astonish-logo-body.svg)',
            maskImage: 'url(/astonish-logo-body.svg)',
            WebkitMaskSize: 'contain',
            maskSize: 'contain',
            WebkitMaskRepeat: 'no-repeat',
            maskRepeat: 'no-repeat',
            WebkitMaskPosition: 'center',
            maskPosition: 'center',
          }}
        />
        {/* Eyes — pure white layer on top (mask cannot spare white by color) */}
        <img
          src="/astonish-logo-eyes.svg"
          alt=""
          className="absolute inset-0 size-full object-contain"
          draggable={false}
        />
      </div>
    </div>
  )
}

export default function HomePage({ onSuggestionClick, userDisplayName }: HomePageProps) {
  const name = firstName(userDisplayName)

  return (
    <div className="flex flex-1 flex-col items-center justify-center overflow-y-auto px-6 py-6">
      <div className="flex w-full max-w-[900px] flex-col items-center gap-5">
        <div className="hero-card-nova w-full px-6 py-5 text-center shadow-none sm:px-8 sm:py-6">
          <ThemedLogo />
          <h1 className="font-display mt-3 text-[26px] leading-snug tracking-[-0.03em] text-[color:var(--text-primary)] sm:text-[32px]">
            Hello, {name}.{' '}
            <em className="text-gradient-nova not-italic" style={{ fontStyle: 'italic' }}>
              Ready when you are.
            </em>
          </h1>
          <p className="mx-auto mt-2 max-w-[480px] text-[13px] leading-relaxed text-[color:var(--text-muted)] text-pretty sm:text-[14px]">
            I&apos;m here to run tasks, wrangle knowledge, and spin up little agent teams — whatever the day throws at us.
          </p>
          <div className="mt-4 flex flex-wrap items-center justify-center gap-2">
            {suggestions.map((text) => (
              <button
                key={text}
                type="button"
                onClick={() => onSuggestionClick?.(text)}
                className="rounded-full border border-[color:var(--border-soft)] bg-[color:var(--pill-bg)] px-3.5 py-1.5 text-[12px] text-[color:var(--text-strong,var(--text-primary))] transition-[border-color,background-color,box-shadow] duration-200 ease-[cubic-bezier(0.4,0,0.2,1)] hover:border-[color:var(--brand)] hover:bg-[color:var(--item-active)] hover:shadow-[0_0_0_1px_var(--brand)] sm:text-[13px]"
              >
                {text}
              </button>
            ))}
          </div>
        </div>

        <div className="grid w-full grid-cols-1 gap-2.5 sm:grid-cols-2 sm:gap-3">
          {tiles.map(({ title, description, Icon, gradient }) => (
            <div
              key={title}
              className="group flex items-start gap-3 rounded-[16px] border border-[color:var(--card-border)] px-3.5 py-3.5 shadow-none transition-[transform,border-color,box-shadow] duration-[240ms] ease-[cubic-bezier(0.4,0,0.2,1)] hover:-translate-y-0.5 hover:border-[color:var(--brand)] hover:shadow-[0_0_0_1px_var(--brand),0_20px_40px_-20px_var(--accent-glow)]"
              style={{
                background: 'var(--card-surface)',
              }}
            >
              <div
                className="flex size-9 shrink-0 items-center justify-center rounded-xl text-white shadow-[0_8px_20px_-8px_var(--accent-glow)]"
                style={{ background: gradient }}
              >
                <Icon size={17} strokeWidth={1.75} />
              </div>
              <div className="min-w-0 pt-0.5">
                <h3 className="text-[13px] font-semibold tracking-[-0.01em] text-[color:var(--text-primary)]">
                  {title}
                </h3>
                <p className="mt-0.5 text-[12px] leading-relaxed text-[color:var(--text-muted)]">
                  {description}
                </p>
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
