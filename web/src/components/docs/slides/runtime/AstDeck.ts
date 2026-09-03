import { html, LitElement } from 'lit'

import { DeckController } from './DeckController'
import { EditController } from './EditController'
import { CANVAS_HEIGHT, CANVAS_WIDTH, type AstDeckElement, type DeckChangeDetail, type FragmentPolicy } from './types'

const RUNTIME_TAGS = ['ast-deck', 'ast-slide', 'ast-text', 'ast-shape', 'ast-image', 'ast-group', 'ast-table', 'ast-chart', 'ast-code', 'ast-icon', 'ast-notes', 'ast-fragment']

export class AstDeck extends LitElement implements AstDeckElement {
  private controller?: DeckController
  private editor?: EditController
  private observer?: ResizeObserver

  get currentIndex(): number { return this.controller?.currentIndex ?? 0 }
  get fragment(): number { return this.controller?.currentFragment ?? 0 }

  next(): void { this.controller?.next() }
  previous(): void { this.controller?.previous() }
  goTo(indexOrId: number | string): void { this.controller?.goTo(indexOrId) }
  enterPresenter(): Window | null { return this.controller?.enterPresenter() ?? null }
  enterPrint(policy?: FragmentPolicy): void { this.controller?.enterPrint(policy) }
  exitPrint(): void { this.controller?.exitPrint() }

  protected override createRenderRoot(): HTMLElement | DocumentFragment { return this }
  protected override render() { return html`<slot></slot>` }

  protected override firstUpdated(): void {
    if (!this.hasAttribute('tabindex')) this.tabIndex = 0
    if (this.hasAttribute('print')) this.setAttribute('data-print', 'true')
    this.controller = new DeckController(this)
    if (typeof ResizeObserver !== 'undefined') {
      this.observer = new ResizeObserver(() => this.scaleToParent())
      if (this.parentElement) this.observer.observe(this.parentElement)
    }
    // Cross-origin navigation channel. The embedding harness (SlidesDeckView)
    // renders this document in a sandboxed, opaque-origin iframe, so it cannot
    // touch our DOM or location directly. It posts { type: 'ast-nav', index }
    // to jump to a slide WITHOUT reloading the document — the strip/thumbnail
    // click path. We answer only same-window-parent messages of our own shape.
    window.addEventListener('message', this.onMessage)
    this.addEventListener('ast-deck-change', this.onDeckChange)
    this.scaleToParent()
    void this.signalReady()
  }

  override disconnectedCallback(): void {
    this.observer?.disconnect()
    this.controller?.disconnect()
    this.editor?.disconnect()
    window.removeEventListener('message', this.onMessage)
    this.removeEventListener('ast-deck-change', this.onDeckChange)
    super.disconnectedCallback()
  }

  private readonly onMessage = (event: MessageEvent): void => {
    if (event.source !== window.parent) return
    const data = event.data as { type?: string; index?: number; slideId?: string; enabled?: boolean; key?: string; value?: string; tag?: string; x?: number; y?: number; w?: number; h?: number; defaults?: Record<string, string>; direction?: string } | null
    if (!data?.type) return
    switch (data.type) {
      case 'ast-nav':
        if (typeof data.index === 'number') this.controller?.goTo(data.index)
        else if (typeof data.slideId === 'string') this.controller?.goTo(data.slideId)
        break
      case 'ast-edit-mode':
        if (this.hasAttribute('print')) return
        if (data.enabled) {
          this.editor ??= new EditController(this)
          this.editor.enable()
        } else {
          this.editor?.disable()
        }
        break
      case 'ast-edit-reset':
        this.editor?.reset()
        break
      case 'ast-edit-commit':
        this.editor?.commit()
        break
      case 'ast-edit-delete':
        this.editor?.deleteSelection()
        break
      case 'ast-edit-set-attr':
        if (typeof data.key === 'string' && typeof data.value === 'string') {
          this.editor?.setAttr(data.key, data.value)
        }
        break
      case 'ast-edit-create':
        if (typeof data.tag === 'string') {
          this.editor?.createElement(
            data.tag,
            Number(data.x) || 400, Number(data.y) || 200,
            Number(data.w) || 300, Number(data.h) || 200,
            (data.defaults as Record<string, string>) ?? {}
          )
        }
        break
      case 'ast-edit-z-order':
        if (typeof data.direction === 'string') {
          this.editor?.setZOrder(data.direction as 'front' | 'forward' | 'backward' | 'back')
        }
        break
      default:
        break
    }
  }

  // Tell the embedding harness which slide is showing. Click/keyboard nav
  // inside this opaque-origin iframe cannot touch React state otherwise, so
  // the bottom strip would stay stuck on slide 1.
  private readonly onDeckChange = (event: Event): void => {
    if (window.parent === window) return
    const detail = (event as CustomEvent<DeckChangeDetail>).detail
    if (!detail || typeof detail.index !== 'number') return
    window.parent.postMessage({ type: 'ast-deck-change', index: detail.index, slideId: detail.slideId }, '*')
  }

  private scaleToParent(): void {
    if (this.hasAttribute('print')) return
    const parent = this.parentElement
    if (!parent) return
    const scale = Math.min(parent.clientWidth / CANVAS_WIDTH, parent.clientHeight / CANVAS_HEIGHT)
    if (!Number.isFinite(scale) || scale <= 0) return
    this.style.transform = `scale(${scale})`
    this.style.left = `${Math.max(0, (parent.clientWidth - CANVAS_WIDTH * scale) / 2)}px`
    this.style.top = `${Math.max(0, (parent.clientHeight - CANVAS_HEIGHT * scale) / 2)}px`
  }

  private async signalReady(): Promise<void> {
    await Promise.all(RUNTIME_TAGS.map(tag => customElements.whenDefined(tag)))
    await this.updateComplete
    await Promise.all([...this.querySelectorAll<LitElement>('*')].map(element => element.updateComplete ?? Promise.resolve()))
    await document.fonts?.ready
    const images = [...this.querySelectorAll('img')]
    await Promise.all(images.map(image => image.complete ? Promise.resolve() : new Promise<void>(resolve => {
      image.addEventListener('load', () => resolve(), { once: true })
      image.addEventListener('error', () => resolve(), { once: true })
    })))
    document.documentElement.dataset.astRenderComplete = 'true'
    window.dispatchEvent(new CustomEvent('ast-render-complete'))
    this.dispatchEvent(new CustomEvent('ast-render-complete', { bubbles: true, composed: true }))
  }
}
