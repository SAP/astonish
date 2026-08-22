import type { AstFragment } from './AstFragment'
import type { AstSlide } from './AstSlide'
import type { DeckChangeDetail, FragmentPolicy } from './types'

const NAVIGATION_KEYS = new Set(['ArrowRight', 'ArrowDown', 'PageDown', ' ', 'ArrowLeft', 'ArrowUp', 'PageUp', 'Home', 'End'])

export class DeckController {
  private index = 0
  private fragment = 0
  private touchStart?: { x: number; y: number }

  get currentIndex(): number {
    return this.index
  }

  get currentFragment(): number {
    return this.fragment
  }

  constructor(private readonly deck: HTMLElement) {
    this.index = this.indexFromLocation()
    this.deck.addEventListener('keydown', this.onKeyDown)
    this.deck.addEventListener('click', this.onClick)
    this.deck.addEventListener('touchstart', this.onTouchStart, { passive: true })
    this.deck.addEventListener('touchend', this.onTouchEnd, { passive: true })
    window.addEventListener('hashchange', this.onHashChange)
    this.applyState(false)
  }

  disconnect(): void {
    this.deck.removeEventListener('keydown', this.onKeyDown)
    this.deck.removeEventListener('click', this.onClick)
    this.deck.removeEventListener('touchstart', this.onTouchStart)
    this.deck.removeEventListener('touchend', this.onTouchEnd)
    window.removeEventListener('hashchange', this.onHashChange)
  }

  next(): void {
    const fragments = this.fragments()
    if (this.fragment < fragments.length) {
      this.fragment += 1
      this.applyState()
      return
    }
    this.goTo(this.index + 1)
  }

  previous(): void {
    if (this.fragment > 0) {
      this.fragment -= 1
      this.applyState()
      return
    }
    const previous = this.index - 1
    if (previous < 0) return
    this.index = previous
    this.fragment = this.fragments().length
    this.applyState()
  }

  goTo(indexOrId: number | string): void {
    const slides = this.slides()
    let index: number
    if (typeof indexOrId === 'string') {
      const found = slides.findIndex(slide => slide.id === indexOrId)
      index = found >= 0 ? found : 0
    } else {
      index = indexOrId
    }
    const bounded = Math.max(0, Math.min(index, slides.length - 1))
    if (bounded === this.index && this.fragment === 0) return
    this.index = bounded
    this.fragment = 0
    this.applyState()
  }

  enterPresenter(): Window | null {
    const url = new URL(location.href)
    url.searchParams.set('presenter', '1')
    return window.open(url.toString(), 'ast-presenter')
  }

  enterPrint(policy: FragmentPolicy = 'final'): void {
    this.deck.setAttribute('print', policy)
    this.applyState(false)
  }

  exitPrint(): void {
    this.deck.removeAttribute('print')
    this.applyState(false)
  }

  private slides(): AstSlide[] {
    return [...this.deck.querySelectorAll<AstSlide>(':scope > ast-slide')]
  }

  private fragments(): AstFragment[] {
    return [...(this.slides()[this.index]?.querySelectorAll<AstFragment>('ast-fragment') ?? [])]
      .sort((a, b) => a.order - b.order)
  }

  private applyState(updateLocation = true): void {
    const slides = this.slides()
    if (slides.length === 0) return
    this.index = Math.max(0, Math.min(this.index, slides.length - 1))
    const print = this.deck.hasAttribute('print')
    slides.forEach((slide, index) => {
      const active = print || index === this.index
      if (slide.active && !active) slide.dispatchEvent(new CustomEvent('ast-slide-leave', { bubbles: true }))
      slide.active = active
      slide.toggleAttribute('aria-hidden', !active)
      if (!active) return
      const ordered = [...slide.querySelectorAll<AstFragment>('ast-fragment')].sort((a, b) => a.order - b.order)
      ordered.forEach((item, fragmentIndex) => {
        item.revealed = print || fragmentIndex < this.fragment
      })
      slide.dispatchEvent(new CustomEvent('ast-slide-enter', { bubbles: true }))
    })
    const active = slides[this.index]
    const detail: DeckChangeDetail = { index: this.index, slideId: active.id, fragment: this.fragment }
    this.deck.dispatchEvent(new CustomEvent<DeckChangeDetail>('ast-deck-change', { detail, bubbles: true }))
    if (updateLocation && active.id && location.hash !== `#${active.id}`) history.replaceState(null, '', `#${active.id}`)
  }

  private indexFromLocation(): number {
    const raw = decodeURIComponent(location.hash.slice(1))
    if (!raw) return 0
    const slides = this.slides()
    // Positional navigation: `#slide-<n>` (1-based) resolves via data-index.
    // This is stable regardless of author-set markup ids, which may not be
    // positional (or unique) — used by the embedded slide strip.
    const positional = /^slide-(\d+)$/.exec(raw)
    if (positional) {
      const oneBased = Number(positional[1])
      const byDataIndex = slides.findIndex(slide => slide.dataset.index === String(oneBased))
      if (byDataIndex >= 0) return byDataIndex
      const bounded = oneBased - 1
      if (bounded >= 0 && bounded < slides.length) return bounded
    }
    const index = slides.findIndex(slide => slide.id === raw)
    return index >= 0 ? index : 0
  }

  private readonly onHashChange = (): void => {
    this.index = this.indexFromLocation()
    this.fragment = 0
    this.applyState(false)
  }

  private readonly onKeyDown = (event: KeyboardEvent): void => {
    if (!NAVIGATION_KEYS.has(event.key)) return
    event.preventDefault()
    if (event.key === 'Home') this.goTo(0)
    else if (event.key === 'End') this.goTo(this.slides().length - 1)
    else if (['ArrowLeft', 'ArrowUp', 'PageUp'].includes(event.key)) this.previous()
    else this.next()
  }

  private readonly onClick = (event: MouseEvent): void => {
    if (event.defaultPrevented || (event.target as Element).closest('a,button,input,textarea,select')) return
    event.clientX < window.innerWidth / 3 ? this.previous() : this.next()
  }

  private readonly onTouchStart = (event: TouchEvent): void => {
    const touch = event.changedTouches[0]
    if (touch) this.touchStart = { x: touch.clientX, y: touch.clientY }
  }

  private readonly onTouchEnd = (event: TouchEvent): void => {
    const touch = event.changedTouches[0]
    if (!touch || !this.touchStart) return
    const dx = touch.clientX - this.touchStart.x
    const dy = touch.clientY - this.touchStart.y
    this.touchStart = undefined
    if (Math.abs(dx) < 40 || Math.abs(dx) < Math.abs(dy)) return
    dx < 0 ? this.next() : this.previous()
  }
}
