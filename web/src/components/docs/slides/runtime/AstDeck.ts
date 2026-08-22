import { html, LitElement } from 'lit'

import { DeckController } from './DeckController'
import { CANVAS_HEIGHT, CANVAS_WIDTH } from './types'

const RUNTIME_TAGS = ['ast-deck', 'ast-slide', 'ast-text', 'ast-shape', 'ast-image', 'ast-group', 'ast-table', 'ast-chart', 'ast-code', 'ast-icon', 'ast-notes', 'ast-fragment']

export class AstDeck extends LitElement {
  private controller?: DeckController
  private observer?: ResizeObserver

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
    this.scaleToParent()
    void this.signalReady()
  }

  override disconnectedCallback(): void {
    this.observer?.disconnect()
    this.controller?.disconnect()
    super.disconnectedCallback()
  }

  private scaleToParent(): void {
    if (this.hasAttribute('print')) return
    const parent = this.parentElement
    if (!parent) return
    const scale = Math.min(parent.clientWidth / CANVAS_WIDTH, parent.clientHeight / CANVAS_HEIGHT)
    if (Number.isFinite(scale) && scale > 0) this.style.transform = `scale(${scale})`
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
