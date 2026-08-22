import { LitElement, noChange, type PropertyValues } from 'lit'

/** Base class for elements positioned on the fixed logical slide canvas. */
export abstract class PositionedElement extends LitElement {
  static override properties = {
    x: { type: Number, reflect: true }, y: { type: Number, reflect: true },
    w: { type: Number, reflect: true }, h: { type: Number, reflect: true },
    rotation: { type: Number, reflect: true },
  }
  x = 0
  y = 0
  w = 0
  h = 0
  rotation = 0

  protected override createRenderRoot(): HTMLElement | DocumentFragment { return this }
  protected override render(): unknown { return noChange }

  protected override updated(_changed?: PropertyValues): void {
    this.style.position = 'absolute'
    this.style.left = `${this.x}px`
    this.style.top = `${this.y}px`
    this.style.width = `${this.w}px`
    this.style.height = `${this.h}px`
    this.style.boxSizing = 'border-box'
    this.style.transform = this.rotation ? `rotate(${this.rotation}deg)` : ''
  }
}
