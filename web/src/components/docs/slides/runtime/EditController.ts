import { CANVAS_HEIGHT, CANVAS_WIDTH } from './types'

const EDITABLE_TAGS = new Set([
  'AST-TEXT',
  'AST-SHAPE',
  'AST-IMAGE',
  'AST-GROUP',
  'AST-TABLE',
  'AST-CHART',
  'AST-CODE',
  'AST-ICON',
])

const DRAG_THRESHOLD = 4
const MIN_VISIBLE = 8

type Geom = { x: number; y: number; w: number; h: number }

type DragState = {
  el: HTMLElement
  startX: number
  startY: number
  pointerX: number
  pointerY: number
  moved: boolean
}

export type EditMove = { id: string; x: number; y: number }

/** Canvas object mover for the harness iframe (not Present / fullscreen). */
export class EditController {
  private enabled = false
  private hover: HTMLElement | null = null
  private selected: HTMLElement | null = null
  private drag: DragState | null = null
  private baseline = new Map<string, { x: number; y: number }>()

  constructor(private readonly deck: HTMLElement) {}

  enable(): void {
    if (this.enabled) return
    this.enabled = true
    this.deck.setAttribute('edit', '')
    this.snapshotAll()
    this.deck.addEventListener('pointerdown', this.onPointerDown)
    this.deck.addEventListener('pointermove', this.onPointerMove)
    this.deck.addEventListener('pointerup', this.onPointerUp)
    this.deck.addEventListener('pointercancel', this.onPointerUp)
    this.deck.addEventListener('lostpointercapture', this.onPointerUp)
  }

  disable(): void {
    if (!this.enabled) return
    this.enabled = false
    this.clearHover()
    this.clearSelected()
    this.drag = null
    this.deck.removeAttribute('edit')
    this.deck.removeAttribute('data-edit-dragging')
    this.deck.removeEventListener('pointerdown', this.onPointerDown)
    this.deck.removeEventListener('pointermove', this.onPointerMove)
    this.deck.removeEventListener('pointerup', this.onPointerUp)
    this.deck.removeEventListener('pointercancel', this.onPointerUp)
    this.deck.removeEventListener('lostpointercapture', this.onPointerUp)
  }

  disconnect(): void {
    this.disable()
    this.baseline.clear()
  }

  /** Restore last committed positions on the active slide. */
  reset(): void {
    const slide = this.activeSlide()
    if (!slide) return
    for (const el of editableChildren(slide)) {
      const id = el.id
      const orig = this.baseline.get(baselineKey(this.slideIndex(), id))
      if (!orig) continue
      setGeom(el, orig.x, orig.y)
    }
    this.clearHover()
    this.deck.removeAttribute('data-edit-dragging')
    this.drag = null
  }

  /** Treat current positions as the saved baseline (after Apply). */
  commit(): void {
    this.snapshotSlide(this.slideIndex())
    this.drag = null
    this.deck.removeAttribute('data-edit-dragging')
  }

  private snapshotAll(): void {
    this.baseline.clear()
    const slides = [...this.deck.querySelectorAll<HTMLElement>(':scope > ast-slide')]
    slides.forEach((_, index) => this.snapshotSlide(index))
  }

  private snapshotSlide(index: number): void {
    const slide = this.slides()[index]
    if (!slide) return
    for (const el of editableChildren(slide)) {
      if (!el.id) continue
      const g = geom(el)
      this.baseline.set(baselineKey(index, el.id), { x: g.x, y: g.y })
    }
  }

  private slides(): HTMLElement[] {
    return [...this.deck.querySelectorAll<HTMLElement>(':scope > ast-slide')]
  }

  private activeSlide(): HTMLElement | null {
    return this.deck.querySelector(':scope > ast-slide[active]')
  }

  private slideIndex(): number {
    const slides = this.slides()
    const active = this.activeSlide()
    if (!active) return 0
    const i = slides.indexOf(active)
    return i >= 0 ? i : 0
  }

  private pendingMoves(): EditMove[] {
    const slide = this.activeSlide()
    if (!slide) return []
    const index = this.slideIndex()
    const out: EditMove[] = []
    for (const el of editableChildren(slide)) {
      if (!el.id) continue
      const g = geom(el)
      const orig = this.baseline.get(baselineKey(index, el.id))
      if (!orig || orig.x !== g.x || orig.y !== g.y) {
        out.push({ id: el.id, x: g.x, y: g.y })
      }
    }
    return out
  }

  private setHover(el: HTMLElement | null): void {
    if (this.hover === el) return
    this.hover?.removeAttribute('data-edit-hover')
    this.hover = el
    if (el && el !== this.selected) el.setAttribute('data-edit-hover', '')
  }

  private clearHover(): void {
    this.hover?.removeAttribute('data-edit-hover')
    this.hover = null
  }

  private setSelected(el: HTMLElement | null): void {
    if (this.selected === el) return
    this.selected?.removeAttribute('data-edit-selected')
    this.selected = el
    if (el) {
      el.removeAttribute('data-edit-hover')
      el.setAttribute('data-edit-selected', '')
    }
  }

  private clearSelected(): void {
    this.selected?.removeAttribute('data-edit-selected')
    this.selected = null
  }

  private readonly onPointerDown = (event: PointerEvent): void => {
    if (!this.enabled || event.button !== 0) return
    const hit = hitTest(this.deck, event.clientX, event.clientY)
    if (!hit) {
      this.clearSelected()
      return
    }
    event.preventDefault()
    event.stopPropagation()
    this.setSelected(hit)
    this.clearHover()
    const g = geom(hit)
    this.drag = {
      el: hit,
      startX: g.x,
      startY: g.y,
      pointerX: event.clientX,
      pointerY: event.clientY,
      moved: false,
    }
    try {
      this.deck.setPointerCapture(event.pointerId)
    } catch {
      /* jsdom */
    }
  }

  private readonly onPointerMove = (event: PointerEvent): void => {
    if (!this.enabled) return
    if (!this.drag) {
      const hit = hitTest(this.deck, event.clientX, event.clientY)
      this.setHover(hit)
      return
    }
    const scale = canvasScale(this.deck)
    const dx = (event.clientX - this.drag.pointerX) / scale
    const dy = (event.clientY - this.drag.pointerY) / scale
    if (!this.drag.moved && Math.hypot(dx, dy) < DRAG_THRESHOLD) return
    this.drag.moved = true
    this.deck.setAttribute('data-edit-dragging', '')
    const g = geom(this.drag.el)
    const next = clampPos(this.drag.startX + dx, this.drag.startY + dy, g.w, g.h)
    setGeom(this.drag.el, next.x, next.y)
  }

  private readonly onPointerUp = (event: PointerEvent): void => {
    if (!this.drag) return
    const moved = this.drag.moved
    this.drag = null
    this.deck.removeAttribute('data-edit-dragging')
    try {
      this.deck.releasePointerCapture(event.pointerId)
    } catch {
      /* already released / jsdom */
    }
    if (!moved) return
    const changes = this.pendingMoves()
    if (changes.length === 0) return
    if (window.parent === window) return
    window.parent.postMessage({ type: 'ast-edit-moved', index: this.slideIndex(), changes }, '*')
  }
}

function baselineKey(index: number, id: string): string {
  return `${index}:${id}`
}

function geom(el: HTMLElement): Geom {
  const node = el as HTMLElement & { x?: number; y?: number; w?: number; h?: number }
  return {
    x: Number(node.x ?? el.getAttribute('x') ?? 0),
    y: Number(node.y ?? el.getAttribute('y') ?? 0),
    w: Number(node.w ?? el.getAttribute('w') ?? 0),
    h: Number(node.h ?? el.getAttribute('h') ?? 0),
  }
}

function setGeom(el: HTMLElement, x: number, y: number): void {
  const node = el as HTMLElement & { x: number; y: number }
  node.x = x
  node.y = y
  el.setAttribute('x', String(x))
  el.setAttribute('y', String(y))
}

function canvasScale(deck: HTMLElement): number {
  const width = deck.getBoundingClientRect().width
  if (!Number.isFinite(width) || width <= 0) return 1
  return width / CANVAS_WIDTH
}

function clampPos(x: number, y: number, w: number, h: number): { x: number; y: number } {
  const nx = Math.max(MIN_VISIBLE - w, Math.min(x, CANVAS_WIDTH - MIN_VISIBLE))
  const ny = Math.max(MIN_VISIBLE - h, Math.min(y, CANVAS_HEIGHT - MIN_VISIBLE))
  return { x: Math.round(nx), y: Math.round(ny) }
}

function editableChildren(slide: HTMLElement): HTMLElement[] {
  return [...slide.children].filter((el): el is HTMLElement => isEditable(el))
}

function isEditable(el: Element): el is HTMLElement {
  if (!(el instanceof HTMLElement)) return false
  if (!EDITABLE_TAGS.has(el.tagName)) return false
  if (!el.id) return false
  if (isFullBleedBackground(el)) return false
  return true
}

function isFullBleedBackground(el: HTMLElement): boolean {
  if (!el.hasAttribute('decorative')) return false
  const g = geom(el)
  return g.w >= CANVAS_WIDTH * 0.9 && g.h >= CANVAS_HEIGHT * 0.9
}

export function hitTest(deck: HTMLElement, clientX: number, clientY: number): HTMLElement | null {
  const slide = deck.querySelector<HTMLElement>(':scope > ast-slide[active]')
  if (!slide) return null
  const stack =
    typeof document.elementsFromPoint === 'function'
      ? document.elementsFromPoint(clientX, clientY)
      : []
  for (const node of stack) {
    const top = directChildOf(slide, node)
    if (top && isEditable(top)) return top
  }
  return null
}

function directChildOf(slide: HTMLElement, node: Element): HTMLElement | null {
  let cur: Element | null = node
  while (cur && cur !== slide && cur.parentElement !== slide) {
    cur = cur.parentElement
  }
  if (cur && cur.parentElement === slide && cur instanceof HTMLElement) return cur
  return null
}
