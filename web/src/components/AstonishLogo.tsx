import { cn } from '@/lib/utils'

type AstonishLogoSize = 'sm' | 'md' | 'lg'

const sizeClasses: Record<
  AstonishLogoSize,
  { plate: string; mark: string }
> = {
  /** TopBar / compact chrome */
  sm: {
    plate: 'size-7 rounded-lg',
    mark: 'size-5',
  },
  /** Mid surfaces (sheets, cards) */
  md: {
    plate: 'size-10 rounded-xl',
    mark: 'size-7',
  },
  /** Chat home hero */
  lg: {
    plate: 'size-[4.5rem] rounded-[1.25rem] sm:size-24 sm:rounded-[1.5rem]',
    mark: 'size-12 sm:size-16',
  },
}

interface AstonishLogoProps {
  /** sm = header, md = default, lg = home hero */
  size?: AstonishLogoSize
  /** When false, skip the brand-tinted plate (mark only). */
  withPlate?: boolean
  className?: string
  /** Decorative by default; set alt for standalone landmark use. */
  alt?: string
}

/**
 * Theme-aware Astonish mark: CSS mask recolors the body with brand tokens;
 * white eyes are a separate layer (a single mask cannot spare white by color).
 */
export default function AstonishLogo({
  size = 'md',
  withPlate = true,
  className,
  alt = '',
}: AstonishLogoProps) {
  const { plate, mark } = sizeClasses[size]

  const markEl = (
    <div className={cn('relative', mark)} aria-hidden={alt ? undefined : true}>
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
      <img
        src="/astonish-logo-eyes.svg"
        alt=""
        className="absolute inset-0 size-full object-contain"
        draggable={false}
      />
    </div>
  )

  if (!withPlate) {
    return (
      <div className={cn('inline-flex items-center justify-center', className)} role={alt ? 'img' : undefined} aria-label={alt || undefined}>
        {markEl}
      </div>
    )
  }

  return (
    <div
      className={cn(
        'inline-flex shrink-0 items-center justify-center',
        plate,
        className,
      )}
      style={{
        background: 'color-mix(in oklab, var(--brand) 16%, var(--card))',
        boxShadow: size === 'lg' ? '0 14px 36px -14px var(--accent-glow)' : '0 6px 16px -8px var(--accent-glow)',
        border: '1px solid color-mix(in oklab, var(--brand) 28%, transparent)',
      }}
      role={alt ? 'img' : undefined}
      aria-label={alt || undefined}
      aria-hidden={alt ? undefined : true}
    >
      {markEl}
    </div>
  )
}
