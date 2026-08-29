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
export const ALIGN_SNAP = 6

type Geom = { x: number; y: number; w: number; h: number }

export type AlignGuide = { axis: 'x' | 'y'; pos: number }

export type EditMove = { id: string; x: number; y: number }
export type EditResize = { id: string; x: number; y: number; w: number; h: number }
export type EditText = { id: string; text: string }
export type EditDraft = {
  moves: EditMove[]
  resizes: EditResize[]
  texts: EditText[]
  deletes: string[]
}

type DragState = {
  el: HTMLElement
  startX: number
  startY: number
  pointerX: number
  pointerY: number
  moved: boolean
  alreadySelected: boolean
}

type ResizeCorner = 'nw' | 'ne' | 'se' | 'sw'

type ResizeState = {
  el: HTMLElement
  corner: ResizeCorner
  start: Geom
  pointerX: number
  pointerY: number
  moved: boolean
}

type Baseline = { x: number; y: number; w: number; h: number; text: string }

const MIN_IMAGE_SIZE = 32

/** Canvas object editor for the harness iframe (not Present / fullscreen). */
export class EditController {
  private enabled = false
  private hover: HTMLElement | null = null
  private selected: HTMLElement | null = null
  private drag: DragState | null = null
  private resize: ResizeState | null = null
  private resizeHandles: HTMLElement | null = null
  private editing: HTMLElement | null = null
  private editingOriginal = ''
  private editingHTML = ''
  private baseline = new Map<string, Baseline>()
  private deleted = new Set<string>()
  private slideHTML = new Map<number, string>()
  private guides: SVGSVGElement | null = null

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
    this.deck.addEventListener('dblclick', this.onDblClick)
    this.deck.addEventListener('ast-deck-change', this.onSlideChange)
    this.deck.ownerDocument.addEventListener('keydown', this.onKeyDown, true)
  }

  disable(): void {
    if (!this.enabled) return
    this.enabled = false
    this.endTextEdit(false, false)
    this.clearHover()
    this.clearSelected()
    this.clearGuides()
    this.clearResizeHandles()
    this.drag = null
    this.resize = null
    this.deck.removeAttribute('edit')
    this.deck.removeAttribute('data-edit-dragging')
    this.deck.removeEventListener('pointerdown', this.onPointerDown)
    this.deck.removeEventListener('pointermove', this.onPointerMove)
    this.deck.removeEventListener('pointerup', this.onPointerUp)
    this.deck.removeEventListener('pointercancel', this.onPointerUp)
    this.deck.removeEventListener('dblclick', this.onDblClick)
    this.deck.removeEventListener('ast-deck-change', this.onSlideChange)
    this.deck.ownerDocument.removeEventListener('keydown', this.onKeyDown, true)
    this.guides?.remove()
    this.guides = null
  }

  disconnect(): void {
    this.disable()
    this.baseline.clear()
    this.deleted.clear()
    this.slideHTML.clear()
  }

  /** Restore last committed markup on the active slide. */
  reset(): void {
    this.endTextEdit(false, false)
    const index = this.slideIndex()
    const slide = this.activeSlide()
    const html = this.slideHTML.get(index)
    if (slide && html != null) slide.innerHTML = html
    this.clearDeleted(index)
    this.clearHover()
    this.clearSelected()
    this.clearGuides()
    this.deck.removeAttribute('data-edit-dragging')
    this.deck.removeAttribute('data-edit-resizing')
    this.drag = null
    this.resize = null
  }

  /** Treat current slide as the saved baseline (after Apply). */
  commit(): void {
    this.endTextEdit(true, false)
    this.snapshotSlide(this.slideIndex())
    this.drag = null
    this.resize = null
    this.clearGuides()
    this.positionResizeHandles()
    this.deck.removeAttribute('data-edit-dragging')
    this.deck.removeAttribute('data-edit-resizing')
  }

  /** Remove the selected object (toolbar Delete or keyboard). */
  deleteSelection(): void {
    this.deleteSelected()
  }

  private snapshotAll(): void {
    this.baseline.clear()
    this.deleted.clear()
    this.slideHTML.clear()
    this.slides().forEach((_, index) => this.snapshotSlide(index))
  }

  private snapshotSlide(index: number): void {
    const slide = this.slides()[index]
    if (!slide) return
    this.slideHTML.set(index, slide.innerHTML)
    this.clearDeleted(index)
    for (const el of editableChildren(slide)) {
      if (!el.id) continue
      const g = geom(el)
      this.baseline.set(baselineKey(index, el.id), { x: g.x, y: g.y, w: g.w, h: g.h, text: el.textContent ?? '' })
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

  private pendingDraft(slide: HTMLElement | null = this.activeSlide(), index: number = this.slideIndex()): EditDraft {
    const deletes = [...this.deleted].filter(key => key.startsWith(`${index}:`)).map(key => key.slice(`${index}:`.length))
    const moves: EditMove[] = []
    const resizes: EditResize[] = []
    const texts: EditText[] = []
    if (!slide) return { moves, resizes, texts, deletes }
    for (const el of editableChildren(slide)) {
      if (!el.id) continue
      const orig = this.baseline.get(baselineKey(index, el.id))
      const g = geom(el)
      const resized = Boolean(orig && (orig.w !== g.w || orig.h !== g.h))
      if (resized) {
        resizes.push({ id: el.id, x: g.x, y: g.y, w: g.w, h: g.h })
      } else if (!orig || orig.x !== g.x || orig.y !== g.y) {
        moves.push({ id: el.id, x: g.x, y: g.y })
      }
      const text = el.textContent ?? ''
      if (el.tagName === 'AST-TEXT' && orig && orig.text !== text) {
        texts.push({ id: el.id, text })
      }
    }
    return { moves, resizes, texts, deletes }
  }

  private notifyParent(slide?: HTMLElement | null, index?: number): void {
    if (window.parent === window) return
    const target = slide === undefined ? this.activeSlide() : slide
    const i = index === undefined ? this.slideIndex() : index
    window.parent.postMessage({ type: 'ast-edit-changed', index: i, ...this.pendingDraft(target, i) }, '*')
  }

  private notifySelection(): void {
    if (window.parent === window) return
    window.parent.postMessage({
      type: 'ast-edit-selected',
      index: this.slideIndex(),
      id: this.selected?.id ?? null,
      tag: this.selected?.tagName ?? null,
    }, '*')
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
      if (!this.editing) {
        try { this.deck.focus({ preventScroll: true }) } catch { this.deck.focus() }
      }
    }
    this.renderResizeHandles()
    this.notifySelection()
  }

  private clearSelected(): void {
    if (!this.selected) return
    this.selected.removeAttribute('data-edit-selected')
    this.selected = null
    this.clearResizeHandles()
    this.notifySelection()
  }

  private startTextEdit(el: HTMLElement): void {
    if (el.tagName !== 'AST-TEXT') return
    if (this.editing === el) return
    this.endTextEdit(true)
    this.editingOriginal = el.textContent ?? ''
    this.editingHTML = el.innerHTML
    this.editing = el
    flattenPlainText(el, this.editingOriginal)
    el.setAttribute('contenteditable', 'true')
    el.setAttribute('data-edit-text', '')
    el.setAttribute('spellcheck', 'false')
    el.addEventListener('input', this.onTextInput)
    el.focus()
    const range = document.createRange()
    range.selectNodeContents(el)
    const sel = window.getSelection()
    sel?.removeAllRanges()
    sel?.addRange(range)
  }

  private endTextEdit(commit: boolean, notify = true): void {
    const el = this.editing
    if (!el) return
    el.removeEventListener('input', this.onTextInput)
    this.editing = null
    const next = el.textContent ?? ''
    if (!commit || next === this.editingOriginal) {
      el.innerHTML = this.editingHTML
    } else {
      flattenPlainText(el, next)
    }
    el.removeAttribute('contenteditable')
    el.removeAttribute('data-edit-text')
    el.removeAttribute('spellcheck')
    this.editingOriginal = ''
    this.editingHTML = ''
    if (notify) {
      const slide = el.closest('ast-slide') as HTMLElement | null
      const i = slide ? this.slides().indexOf(slide) : this.slideIndex()
      this.notifyParent(slide, i >= 0 ? i : this.slideIndex())
    }
  }

  private readonly onTextInput = (): void => {
    this.notifyParent()
  }

  private deleteSelected(): void {
    const el = this.selected
    if (!el?.id) return
    if (this.editing) this.endTextEdit(false)
    const index = this.slideIndex()
    this.deleted.add(baselineKey(index, el.id))
    this.clearSelected()
    this.clearHover()
    el.remove()
    this.notifyParent()
  }

  private readonly onSlideChange = (): void => {
    if (this.editing) this.endTextEdit(true)
    this.drag = null
    this.resize = null
    this.clearHover()
    this.clearSelected()
    this.clearGuides()
    this.deck.removeAttribute('data-edit-dragging')
  }

  private readonly onPointerDown = (event: PointerEvent): void => {
    if (!this.enabled || event.button !== 0) return
    const handle = (event.target as Element | null)?.closest<HTMLElement>('[data-resize-corner]')
    if (handle && this.selected?.tagName === 'AST-IMAGE') {
      event.preventDefault()
      event.stopPropagation()
      this.resize = {
        el: this.selected,
        corner: handle.dataset.resizeCorner as ResizeCorner,
        start: geom(this.selected),
        pointerX: event.clientX,
        pointerY: event.clientY,
        moved: false,
      }
      try {
        this.deck.setPointerCapture(event.pointerId)
      } catch {
        /* jsdom */
      }
      return
    }
    if (this.editing) {
      const t = event.target as Node | null
      if (t && this.editing.contains(t)) return
      this.endTextEdit(true)
    }
    const hit = hitTest(this.deck, event.clientX, event.clientY)
    if (!hit) {
      this.clearSelected()
      return
    }
    event.preventDefault()
    event.stopPropagation()
    const alreadySelected = this.selected === hit
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
      alreadySelected,
    }
    try {
      this.deck.setPointerCapture(event.pointerId)
    } catch {
      /* jsdom */
    }
  }

  private readonly onPointerMove = (event: PointerEvent): void => {
    if (!this.enabled || this.editing) return
    if (this.resize) {
      const scale = canvasScale(this.deck)
      const dx = (event.clientX - this.resize.pointerX) / scale
      const dy = (event.clientY - this.resize.pointerY) / scale
      if (!this.resize.moved && Math.hypot(dx, dy) < DRAG_THRESHOLD) return
      this.resize.moved = true
      this.deck.setAttribute('data-edit-resizing', '')
      const next = proportionalResize(this.resize.start, this.resize.corner, dx, dy)
      setFullGeom(this.resize.el, next)
      this.positionResizeHandles()
      return
    }
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
    const raw = clampPos(this.drag.startX + dx, this.drag.startY + dy, g.w, g.h)
    const others = this.siblingBoxes(this.drag.el)
    const snapped = snapToAlign({ x: raw.x, y: raw.y, w: g.w, h: g.h }, others)
    const pos = clampPos(snapped.x, snapped.y, g.w, g.h)
    setGeom(this.drag.el, pos.x, pos.y)
    this.positionResizeHandles()
    this.renderGuides(snapped.guides)
  }

  private readonly onPointerUp = (event: PointerEvent): void => {
    if (this.resize) {
      const moved = this.resize.moved
      this.resize = null
      this.deck.removeAttribute('data-edit-resizing')
      try {
        this.deck.releasePointerCapture(event.pointerId)
      } catch {
        /* already released / jsdom */
      }
      if (moved) this.notifyParent()
      return
    }
    if (!this.drag) return
    const moved = this.drag.moved
    const alreadySelected = this.drag.alreadySelected
    const el = this.drag.el
    this.drag = null
    this.deck.removeAttribute('data-edit-dragging')
    this.clearGuides()
    try {
      this.deck.releasePointerCapture(event.pointerId)
    } catch {
      /* already released / jsdom */
    }
    if (moved) {
      this.notifyParent()
      return
    }
    if (alreadySelected && el.tagName === 'AST-TEXT' && el.isConnected) {
      this.startTextEdit(el)
    }
  }

  private readonly onDblClick = (event: MouseEvent): void => {
    if (!this.enabled) return
    const hit = hitTest(this.deck, event.clientX, event.clientY)
    if (!hit || hit.tagName !== 'AST-TEXT') return
    event.preventDefault()
    event.stopPropagation()
    this.drag = null
    this.setSelected(hit)
    this.startTextEdit(hit)
  }

  private readonly onKeyDown = (event: KeyboardEvent): void => {
    if (!this.enabled) return
    if (this.editing) {
      if (event.key === 'Escape') {
        event.preventDefault()
        this.endTextEdit(false)
        return
      }
      if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
        event.preventDefault()
        this.endTextEdit(true)
      }
      return
    }
    if (event.key === 'Escape') {
      this.clearSelected()
      return
    }
    if ((event.key === 'Delete' || event.key === 'Backspace') && this.selected) {
      event.preventDefault()
      event.stopPropagation()
      this.deleteSelected()
      return
    }
    if (event.key === 'Enter' && this.selected?.tagName === 'AST-TEXT') {
      event.preventDefault()
      this.startTextEdit(this.selected)
    }
  }

  private renderResizeHandles(): void {
    this.clearResizeHandles()
    if (this.selected?.tagName !== 'AST-IMAGE') return
    const overlay = document.createElement('div')
    overlay.className = 'ast-edit-resize-handles'
    overlay.setAttribute('aria-hidden', 'true')
    for (const corner of ['nw', 'ne', 'se', 'sw'] as ResizeCorner[]) {
      const handle = document.createElement('span')
      handle.className = 'ast-edit-resize-handle'
      handle.dataset.resizeCorner = corner
      overlay.append(handle)
    }
    this.deck.append(overlay)
    this.resizeHandles = overlay
    this.positionResizeHandles()
  }

  private positionResizeHandles(): void {
    if (!this.resizeHandles || !this.selected) return
    const g = geom(this.selected)
    const scale = canvasScale(this.deck)
    const handleSize = Math.round(24 / scale)
    Object.assign(this.resizeHandles.style, {
      left: `${g.x}px`, top: `${g.y}px`, width: `${g.w}px`, height: `${g.h}px`,
      '--ast-edit-handle-size': `${handleSize}px`,
      '--ast-edit-handle-offset': `${Math.round(handleSize / -2)}px`,
    })
  }

  private clearResizeHandles(): void {
    this.resizeHandles?.remove()
    this.resizeHandles = null
  }

  private siblingBoxes(moving: HTMLElement): Geom[] {
    const slide = this.activeSlide()
    if (!slide) return []
    return editableChildren(slide)
      .filter(el => el !== moving)
      .map(geom)
  }

  private ensureGuides(): SVGSVGElement {
    if (this.guides?.isConnected) return this.guides
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
    svg.setAttribute('class', 'ast-edit-guides')
    svg.setAttribute('viewBox', `0 0 ${CANVAS_WIDTH} ${CANVAS_HEIGHT}`)
    svg.setAttribute('width', String(CANVAS_WIDTH))
    svg.setAttribute('height', String(CANVAS_HEIGHT))
    svg.setAttribute('aria-hidden', 'true')
    this.deck.append(svg)
    this.guides = svg
    return svg
  }

  private renderGuides(guides: AlignGuide[]): void {
    const svg = this.ensureGuides()
    svg.replaceChildren()
    for (const g of guides) {
      const line = document.createElementNS('http://www.w3.org/2000/svg', 'line')
      if (g.axis === 'x') {
        line.setAttribute('x1', String(g.pos))
        line.setAttribute('x2', String(g.pos))
        line.setAttribute('y1', '0')
        line.setAttribute('y2', String(CANVAS_HEIGHT))
      } else {
        line.setAttribute('y1', String(g.pos))
        line.setAttribute('y2', String(g.pos))
        line.setAttribute('x1', '0')
        line.setAttribute('x2', String(CANVAS_WIDTH))
      }
      svg.append(line)
    }
  }

  private clearGuides(): void {
    this.guides?.replaceChildren()
  }

  private clearDeleted(index: number): void {
    const prefix = `${index}:`
    for (const key of [...this.deleted]) {
      if (key.startsWith(prefix)) this.deleted.delete(key)
    }
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

function setFullGeom(el: HTMLElement, value: Geom): void {
  const node = el as HTMLElement & { x: number; y: number; w: number; h: number }
  node.x = value.x
  node.y = value.y
  node.w = value.w
  node.h = value.h
  el.setAttribute('x', String(value.x))
  el.setAttribute('y', String(value.y))
  el.setAttribute('w', String(value.w))
  el.setAttribute('h', String(value.h))
}

export function proportionalResize(start: Geom, corner: ResizeCorner, dx: number, dy: number): Geom {
  if (start.w <= 0 || start.h <= 0) return start
  const ratio = start.w / start.h
  const horizontal = corner.endsWith('e') ? dx : -dx
  const vertical = corner.startsWith('s') ? dy : -dy
  const scaleFromX = (start.w + horizontal) / start.w
  const scaleFromY = (start.h + vertical) / start.h
  const scale = Math.max(MIN_IMAGE_SIZE / start.w, MIN_IMAGE_SIZE / start.h, Math.abs(scaleFromX - 1) >= Math.abs(scaleFromY - 1) ? scaleFromX : scaleFromY)
  const w = Math.max(MIN_IMAGE_SIZE, Math.round(start.w * scale))
  const h = Math.max(MIN_IMAGE_SIZE, Math.round(w / ratio))
  return {
    x: corner.endsWith('w') ? start.x + start.w - w : start.x,
    y: corner.startsWith('n') ? start.y + start.h - h : start.y,
    w,
    h,
  }
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

function edges(b: Geom) {
  return { l: b.x, r: b.x + b.w, t: b.y, b: b.y + b.h, cx: b.x + b.w / 2, cy: b.y + b.h / 2 }
}

/** Snap a moving box to siblings' left/right/top/bottom/center edges. */
export function snapToAlign(moving: Geom, others: Geom[], threshold = ALIGN_SNAP): { x: number; y: number; guides: AlignGuide[] } {
  const m = edges(moving)
  let bestDx = 0
  let bestDy = 0
  let bestAbsX = threshold + 1
  let bestAbsY = threshold + 1
  for (const other of others) {
    const e = edges(other)
    for (const from of [m.l, m.cx, m.r]) {
      for (const to of [e.l, e.cx, e.r]) {
        const d = to - from
        const a = Math.abs(d)
        if (a < bestAbsX) {
          bestAbsX = a
          bestDx = d
        }
      }
    }
    for (const from of [m.t, m.cy, m.b]) {
      for (const to of [e.t, e.cy, e.b]) {
        const d = to - from
        const a = Math.abs(d)
        if (a < bestAbsY) {
          bestAbsY = a
          bestDy = d
        }
      }
    }
  }
  const x = bestAbsX <= threshold ? Math.round(moving.x + bestDx) : moving.x
  const y = bestAbsY <= threshold ? Math.round(moving.y + bestDy) : moving.y
  const snapped = edges({ ...moving, x, y })
  const seen = new Set<string>()
  const guides: AlignGuide[] = []
  const add = (axis: 'x' | 'y', pos: number) => {
    const key = `${axis}:${Math.round(pos)}`
    if (seen.has(key)) return
    seen.add(key)
    guides.push({ axis, pos: Math.round(pos) })
  }
  for (const other of others) {
    const e = edges(other)
    for (const pos of [snapped.l, snapped.cx, snapped.r]) {
      if ([e.l, e.cx, e.r].some(t => Math.abs(t - pos) <= 0.51)) add('x', pos)
    }
    for (const pos of [snapped.t, snapped.cy, snapped.b]) {
      if ([e.t, e.cy, e.b].some(t => Math.abs(t - pos) <= 0.51)) add('y', pos)
    }
  }
  return { x, y, guides }
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

function flattenPlainText(el: HTMLElement, text: string): void {
  const node = el as HTMLElement & { replacePlainText?: (value: string) => void }
  if (typeof node.replacePlainText === 'function') {
    node.replacePlainText(text)
    return
  }
  el.textContent = text
}
