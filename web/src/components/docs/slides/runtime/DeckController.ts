import type { AstFragment } from './AstFragment'
import type { AstSlide } from './AstSlide'
import type { DeckChangeDetail } from './types'

const NAVIGATION_KEYS = new Set(['ArrowRight', 'ArrowDown', 'PageDown', ' ', 'ArrowLeft', 'ArrowUp', 'PageUp', 'Home', 'End'])

export class DeckController {
  private index = 0
  private fragment = 0
  private touchStart?: { x: number; y: number }

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

  goTo(index: number): void {
    const slides = this.slides()
    const bounded = Math.max(0, Math.min(index, slides.length - 1))
    if (bounded === this.index && this.fragment === 0) return
    this.index = bounded
    this.fragment = 0
    this.applyState()
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
    slides.forEach((slide, index) => {
      const active = index === this.index
      if (slide.active && !active) slide.dispatchEvent(new CustomEvent('ast-slide-leave', { bubbles: true }))
      slide.active = active
      slide.toggleAttribute('aria-hidden', !active)
      if (!active) return
      slide.querySelectorAll<AstFragment>('ast-fragment').forEach((item, fragmentIndex) => {
        item.revealed = this.deck.hasAttribute('print') || fragmentIndex < this.fragment
      })
      slide.dispatchEvent(new CustomEvent('ast-slide-enter', { bubbles: true }))
    })
    const active = slides[this.index]
    const detail: DeckChangeDetail = { index: this.index, slideId: active.id, fragment: this.fragment }
    this.deck.dispatchEvent(new CustomEvent<DeckChangeDetail>('ast-deck-change', { detail, bubbles: true }))
    if (updateLocation && active.id && location.hash !== `#${active.id}`) history.replaceState(null, '', `#${active.id}`)
  }

  private indexFromLocation(): number {
    const id = decodeURIComponent(location.hash.slice(1))
    const index = this.slides().findIndex(slide => slide.id === id)
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
