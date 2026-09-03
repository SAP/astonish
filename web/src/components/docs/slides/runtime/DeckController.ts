import type { AstFragment } from './AstFragment'
import type { AstSlide } from './AstSlide'
import type { DeckChangeDetail, FragmentPolicy } from './types'

const NAVIGATION_KEYS = new Set(['ArrowRight', 'ArrowDown', 'PageDown', ' ', 'ArrowLeft', 'ArrowUp', 'PageUp', 'Home', 'End'])

export class DeckController {
  private index = 0
  private fragment = 0
  private touchStart?: { x: number; y: number }
  private presenter = new URLSearchParams(location.search).get('presenter') === '1'
  private enteredFullscreen = false
  private presenterClosed = false
  private presenterStart?: HTMLButtonElement

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
    if (this.presenter) {
      document.addEventListener('fullscreenchange', this.onFullscreenChange)
      window.addEventListener('keydown', this.onPresenterKeyDown)
    }
    this.applyState(false)
    if (this.presenter) void this.enterFullscreen()
  }

  disconnect(): void {
    this.deck.removeEventListener('keydown', this.onKeyDown)
    this.deck.removeEventListener('click', this.onClick)
    this.deck.removeEventListener('touchstart', this.onTouchStart)
    this.deck.removeEventListener('touchend', this.onTouchEnd)
    window.removeEventListener('hashchange', this.onHashChange)
    document.removeEventListener('fullscreenchange', this.onFullscreenChange)
    window.removeEventListener('keydown', this.onPresenterKeyDown)
    this.hidePresenterStart()
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
    // Performance: for large decks (hundreds of elements) we must not re-walk
    // and re-toggle every slide + fragment on each state change. Only mutate a
    // slide whose active/hidden state actually changes, and only re-reveal
    // fragments on the slide that is (or just became) active. Off-screen slides
    // stay display:none (see styles.ts) so their subtrees are never laid out
    // until first activation.
    slides.forEach((slide, index) => {
      const active = print || index === this.index
      const wasActive = slide.active
      if (wasActive === active) {
        // No active-state change: the only per-slide work still required is
        // updating fragment reveal on the slide that remains active (its
        // fragment index may have advanced). Keep aria-hidden in sync
        // idempotently so the very first pass still marks inactive slides
        // hidden (toggleAttribute is a no-op when already correct).
        if (active) this.applyFragments(slide, print)
        else if (!slide.hasAttribute('aria-hidden')) slide.setAttribute('aria-hidden', '')
        return
      }
      if (wasActive && !active) slide.dispatchEvent(new CustomEvent('ast-slide-leave', { bubbles: true }))
      slide.active = active
      slide.toggleAttribute('aria-hidden', !active)
      if (!active) return
      this.applyFragments(slide, print)
      slide.dispatchEvent(new CustomEvent('ast-slide-enter', { bubbles: true }))
    })
    const active = slides[this.index]
    const detail: DeckChangeDetail = { index: this.index, slideId: active.id, fragment: this.fragment }
    this.deck.dispatchEvent(new CustomEvent<DeckChangeDetail>('ast-deck-change', { detail, bubbles: true }))
    if (updateLocation && active.id && location.hash !== `#${active.id}`) history.replaceState(null, '', `#${active.id}`)
  }

  private applyFragments(slide: AstSlide, print: boolean): void {
    const ordered = [...slide.querySelectorAll<AstFragment>('ast-fragment')].sort((a, b) => a.order - b.order)
    ordered.forEach((item, fragmentIndex) => {
      const revealed = print || fragmentIndex < this.fragment
      if (item.revealed !== revealed) item.revealed = revealed
    })
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
    if (this.deck.hasAttribute('edit')) {
      const target = event.target as HTMLElement | null
      if (target?.isContentEditable) return
      if ([' ', 'Spacebar', 'Delete', 'Backspace', 'Enter', 'Escape'].includes(event.key)) return
    }
    if (!NAVIGATION_KEYS.has(event.key)) return
    event.preventDefault()
    if (event.key === 'Home') this.goTo(0)
    else if (event.key === 'End') this.goTo(this.slides().length - 1)
    else if (['ArrowLeft', 'ArrowUp', 'PageUp'].includes(event.key)) this.previous()
    else this.next()
  }

  private readonly onClick = (event: MouseEvent): void => {
    if (event.defaultPrevented || (event.target as Element).closest('a,button,input,textarea,select')) return
    // In edit mode, suppress ALL click-to-navigate. Navigation is handled by
    // the React overlay buttons (SlidesDeckView) so accidental canvas clicks
    // (or the tail-end of a drag/resize) never jump slides.
    if (this.deck.hasAttribute('edit')) return
    event.clientX < window.innerWidth / 3 ? this.previous() : this.next()
  }

  private readonly onPresenterStart = (event: MouseEvent): void => {
    event.preventDefault()
    event.stopPropagation()
    void this.enterFullscreen()
  }

  private readonly onPresenterKeyDown = (event: KeyboardEvent): void => {
    if (event.key === 'Escape') {
      event.preventDefault()
      void this.exitPresenter()
      return
    }
    if (event.target !== this.presenterStart) void this.enterFullscreen()
  }

  private readonly onFullscreenChange = (): void => {
    if (document.fullscreenElement) {
      this.enteredFullscreen = true
      this.hidePresenterStart()
      return
    }
    if (this.enteredFullscreen) this.closePresenter()
  }

  private async enterFullscreen(): Promise<void> {
    if (document.fullscreenElement) {
      this.hidePresenterStart()
      return
    }
    if (!document.documentElement.requestFullscreen) {
      this.showPresenterStart()
      return
    }
    try {
      await document.documentElement.requestFullscreen()
      this.enteredFullscreen = true
      this.hidePresenterStart()
    } catch {
      this.showPresenterStart()
    }
  }

  private showPresenterStart(): void {
    if (this.presenterStart || this.presenterClosed) return
    const button = document.createElement('button')
    button.type = 'button'
    button.textContent = 'Start slideshow'
    button.setAttribute('aria-label', 'Start slideshow in fullscreen')
    Object.assign(button.style, {
      position: 'fixed',
      top: '24px',
      left: '50%',
      zIndex: '2147483647',
      transform: 'translateX(-50%)',
      padding: '16px 24px',
      border: '2px solid currentColor',
      borderRadius: '12px',
      background: '#ffffff',
      color: '#172033',
      font: '600 20px/1.2 system-ui, sans-serif',
      cursor: 'pointer',
      boxShadow: '0 8px 30px rgb(0 0 0 / 25%)',
    })
    button.addEventListener('click', this.onPresenterStart)
    document.body.append(button)
    button.focus()
    this.presenterStart = button
  }

  private hidePresenterStart(): void {
    if (!this.presenterStart) return
    this.presenterStart.removeEventListener('click', this.onPresenterStart)
    this.presenterStart.remove()
    this.presenterStart = undefined
  }

  private async exitPresenter(): Promise<void> {
    if (document.fullscreenElement && document.exitFullscreen) {
      try {
        await document.exitFullscreen()
      } catch {
        // Closing the dedicated presenter tab remains the final fallback.
      }
    }
    this.closePresenter()
  }

  private closePresenter(): void {
    if (this.presenterClosed) return
    this.presenterClosed = true
    window.close()
  }

  private readonly onTouchStart = (event: TouchEvent): void => {
    if (this.deck.hasAttribute('edit')) return
    const touch = event.changedTouches[0]
    if (touch) this.touchStart = { x: touch.clientX, y: touch.clientY }
  }

  private readonly onTouchEnd = (event: TouchEvent): void => {
    if (this.deck.hasAttribute('edit')) return
    const touch = event.changedTouches[0]
    if (!touch || !this.touchStart) return
    const dx = touch.clientX - this.touchStart.x
    const dy = touch.clientY - this.touchStart.y
    this.touchStart = undefined
    if (Math.abs(dx) < 40 || Math.abs(dx) < Math.abs(dy)) return
    dx < 0 ? this.next() : this.previous()
  }
}
