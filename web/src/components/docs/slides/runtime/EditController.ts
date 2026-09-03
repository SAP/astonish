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

const EDITABLE_ATTRS = new Set([
  'fill', 'fill-token', 'line', 'line-token', 'line-width', 'opacity',
  'rot', 'font', 'font-token', 'size', 'weight', 'align', 'color',
  'color-token', 'geom', 'kind', 'anchor', 'x', 'y', 'w', 'h',
])

const DRAG_THRESHOLD = 4
const MIN_VISIBLE = 8
export const ALIGN_SNAP = 6

type Geom = { x: number; y: number; w: number; h: number }

export type AlignGuide = { axis: 'x' | 'y'; pos: number }

export type EditMove = { id: string; x: number; y: number }
export type EditResize = { id: string; x: number; y: number; w: number; h: number }
export type EditText = { id: string; text: string }
export type EditAttr = { id: string; attrs: Record<string, string> }
export type EditCreate = { id: string; tag: string; attrs: Record<string, string>; text?: string }
export type EditDraft = {
  moves: EditMove[]
  resizes: EditResize[]
  texts: EditText[]
  deletes: string[]
  attrs?: EditAttr[]
  creates?: EditCreate[]
}

export type SelectionMetadata = {
  id: string
  tag: string
  x: number; y: number; w: number; h: number
  rotation: number
  fill: string; stroke: string; strokeWidth: number; opacity: number
  font: string; fontSize: number; fontWeight: string; align: string; color: string
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

type RotationState = {
  el: HTMLElement
  startAngle: number      // angle (deg) from element center to initial pointer
  startRot: number        // element's rot attribute at drag start
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
  private rotation: RotationState | null = null
  private resizeHandles: HTMLElement | null = null
  private editing: HTMLElement | null = null
  private editingOriginal = ''
  private editingHTML = ''
  private baseline = new Map<string, Baseline>()
  private deleted = new Set<string>()
  private slideHTML = new Map<number, string>()
  private attrChanges = new Map<string, Record<string, string>>()
  private created = new Map<string, { tag: string; attrs: Record<string, string>; text?: string }>()
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
    this.rotation = null
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
    this.attrChanges.clear()
    this.created.clear()
  }

  /** Restore last committed markup on the active slide. */
  reset(): void {
    this.endTextEdit(false, false)
    const index = this.slideIndex()
    const slide = this.activeSlide()
    const html = this.slideHTML.get(index)
    if (slide && html != null) slide.innerHTML = html
    this.clearDeleted(index)
    this.attrChanges.clear()
    this.created.clear()
    this.clearHover()
    this.clearSelected()
    this.clearGuides()
    this.deck.removeAttribute('data-edit-dragging')
    this.deck.removeAttribute('data-edit-resizing')
    this.deck.removeAttribute('data-edit-rotating')
    this.drag = null
    this.resize = null
    this.rotation = null
  }

  /** Treat current slide as the saved baseline (after Apply). */
  commit(): void {
    this.endTextEdit(true, false)
    this.snapshotSlide(this.slideIndex())
    this.attrChanges.clear()
    this.created.clear()
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

  /** Set an allowed attribute on the selected element. */
  setAttr(key: string, value: string): void {
    if (!this.selected || !EDITABLE_ATTRS.has(key)) return
    this.selected.setAttribute(key, value)
    const id = this.selected.id
    if (id) {
      const existing = this.attrChanges.get(id) ?? {}
      existing[key] = value
      this.attrChanges.set(id, existing)
    }
    this.notifyParent()
    this.notifySelection()
  }

  /** Create a new element on the active slide. */
  createElement(tag: string, x: number, y: number, w: number, h: number, defaults: Record<string, string>): void {
    const slide = this.activeSlide()
    if (!slide) return
    const allowed = new Set(['ast-shape', 'ast-text', 'ast-image'])
    if (!allowed.has(tag)) return
    const prefix = tag.replace('ast-', '')
    const id = this.generateId(prefix)
    const el = document.createElement(tag)
    el.id = id
    el.setAttribute('x', String(Math.round(x)))
    el.setAttribute('y', String(Math.round(y)))
    el.setAttribute('w', String(Math.round(w)))
    el.setAttribute('h', String(Math.round(h)))
    for (const [k, v] of Object.entries(defaults)) {
      el.setAttribute(k, v)
    }
    if (tag === 'ast-text' && !el.textContent) {
      el.textContent = 'Text'
    }
    slide.appendChild(el)
    // Strip transient attributes (e.g. blob: src URLs for images) from the
    // persisted create record — the server derives `src` from `asset-ref`.
    const persistAttrs: Record<string, string> = { x: String(Math.round(x)), y: String(Math.round(y)), w: String(Math.round(w)), h: String(Math.round(h)), ...defaults }
    delete persistAttrs.src
    this.created.set(id, { tag, attrs: persistAttrs, text: tag === 'ast-text' ? (el.textContent ?? '') : undefined })
    this.snapshotSlide(this.slideIndex())
    this.setSelected(el)
    this.notifyParent()
  }

  /** Reorder the selected element within its parent. */
  setZOrder(direction: 'front' | 'forward' | 'backward' | 'back'): void {
    const el = this.selected
    if (!el) return
    const parent = el.parentElement
    if (!parent) return
    switch (direction) {
      case 'front':
        parent.appendChild(el)
        break
      case 'back': {
        const first = parent.firstElementChild
        if (first && first !== el) parent.insertBefore(el, first)
        break
      }
      case 'forward': {
        const next = el.nextElementSibling
        if (next) next.after(el)
        break
      }
      case 'backward': {
        const prev = el.previousElementSibling
        if (prev) parent.insertBefore(el, prev)
        break
      }
    }
    this.positionResizeHandles()
    this.notifyParent()
  }

  private generateId(prefix: string): string {
    const slide = this.activeSlide()
    const existing = new Set<string>()
    if (slide) {
      for (const el of editableChildren(slide)) {
        if (el.id) existing.add(el.id)
      }
    }
    for (const id of this.created.keys()) existing.add(id)
    let n = 1
    while (existing.has(`user-${prefix}-${n}`)) n++
    return `user-${prefix}-${n}`
  }

  private snapshotAll(): void {
    this.baseline.clear()
    this.deleted.clear()
    this.slideHTML.clear()
    this.attrChanges.clear()
    this.created.clear()
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
    // Only include deletes for elements that existed before this session (not newly created ones)
    const deletes = [...this.deleted]
      .filter(key => key.startsWith(`${index}:`))
      .map(key => key.slice(`${index}:`.length))
      .filter(id => !this.created.has(id))
    const moves: EditMove[] = []
    const resizes: EditResize[] = []
    const texts: EditText[] = []
    if (!slide) return { moves, resizes, texts, deletes }
    const createdIds = new Set(this.created.keys())
    for (const el of editableChildren(slide)) {
      if (!el.id) continue
      if (createdIds.has(el.id)) continue  // skip — position captured in creates
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

    // Attribute changes — skip elements that only exist client-side (in created)
    // because their attrs are captured in the creates array and the backend
    // cannot look them up by id yet.
    const attrs: EditAttr[] = []
    for (const [elId, changes] of this.attrChanges) {
      if (this.deleted.has(`${index}:${elId}`)) continue
      if (this.created.has(elId)) continue  // merged into creates below
      attrs.push({ id: elId, attrs: changes })
    }

    // Created elements — use current DOM geometry so drags after creation are captured
    const creates: EditCreate[] = []
    for (const [elId, info] of this.created) {
      // Skip elements that were created and then deleted in the same session
      if (this.deleted.has(`${index}:${elId}`)) continue
      const el = slide?.querySelector(`#${CSS.escape(elId)}`) as HTMLElement | null
      const updatedAttrs = { ...info.attrs }
      if (el) {
        const g = geom(el)
        updatedAttrs.x = String(g.x)
        updatedAttrs.y = String(g.y)
        updatedAttrs.w = String(g.w)
        updatedAttrs.h = String(g.h)
        const rot = el.getAttribute('rot')
        if (rot && rot !== '0') updatedAttrs.rot = rot
      }
      // Merge any attribute changes made after creation (e.g. fill, font, rot)
      const extraAttrs = this.attrChanges.get(elId)
      if (extraAttrs) Object.assign(updatedAttrs, extraAttrs)
      const text = info.tag === 'ast-text' && el ? (el.textContent ?? '') : info.text
      creates.push({ id: elId, tag: info.tag, attrs: updatedAttrs, text })
    }

    return { moves, resizes, texts, deletes, ...(attrs.length ? { attrs } : {}), ...(creates.length ? { creates } : {}) }
  }

  private notifyParent(slide?: HTMLElement | null, index?: number): void {
    if (window.parent === window) return
    const target = slide === undefined ? this.activeSlide() : slide
    const i = index === undefined ? this.slideIndex() : index
    window.parent.postMessage({ type: 'ast-edit-changed', index: i, ...this.pendingDraft(target, i) }, '*')
  }

  private notifySelection(clickX?: number, clickY?: number): void {
    if (window.parent === window) return
    const el = this.selected
    if (!el) {
      window.parent.postMessage({
        type: 'ast-edit-selected',
        index: this.slideIndex(),
        id: null,
        tag: null,
        clickX,
        clickY,
      }, '*')
      return
    }
    const g = geom(el)
    const rotation = Number(el.getAttribute('rot')) || 0
    // Shape properties (AstShape)
    const fill = el.getAttribute('fill') || el.getAttribute('fill-token') || ''
    const stroke = el.getAttribute('line') || el.getAttribute('line-token') || ''
    const strokeWidth = Number(el.getAttribute('line-width')) || 0
    const opacity = Number(el.getAttribute('opacity'))
    // Text properties (AstText)
    const font = el.getAttribute('font') || el.getAttribute('font-token') || ''
    const fontSize = Number(el.getAttribute('size')) || 0
    const fontWeight = el.getAttribute('weight') || ''
    const align = el.getAttribute('align') || ''
    const color = el.getAttribute('color') || el.getAttribute('color-token') || ''

    window.parent.postMessage({
      type: 'ast-edit-selected',
      index: this.slideIndex(),
      id: el.id ?? null,
      tag: el.tagName ?? null,
      x: g.x, y: g.y, w: g.w, h: g.h,
      rotation,
      fill, stroke, strokeWidth, opacity: Number.isNaN(opacity) ? 1 : opacity,
      font, fontSize, fontWeight, align, color,
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

  private clearSelected(notify = true): void {
    if (!this.selected) return
    this.selected.removeAttribute('data-edit-selected')
    this.selected = null
    this.clearResizeHandles()
    if (notify) this.notifySelection()
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
    this.rotation = null
    this.clearHover()
    this.clearSelected()
    this.clearGuides()
    this.deck.removeAttribute('data-edit-dragging')
    this.deck.removeAttribute('data-edit-rotating')
  }

  private readonly onPointerDown = (event: PointerEvent): void => {
    if (!this.enabled || event.button !== 0) return
    // Rotation handle
    const rotHandle = (event.target as Element | null)?.closest<HTMLElement>('[data-rotation-handle]')
    if (rotHandle && this.selected) {
      event.preventDefault()
      event.stopPropagation()
      const g = geom(this.selected)
      const scale = canvasScale(this.deck)
      const rect = this.deck.getBoundingClientRect()
      const cx = rect.left + (g.x + g.w / 2) * scale
      const cy = rect.top + (g.y + g.h / 2) * scale
      const startAngle = Math.atan2(event.clientY - cy, event.clientX - cx) * 180 / Math.PI
      const startRot = Number(this.selected.getAttribute('rot')) || 0
      this.rotation = { el: this.selected, startAngle, startRot, moved: false }
      try {
        this.deck.setPointerCapture(event.pointerId)
      } catch { /* jsdom */ }
      return
    }
    // Resize corner handle — works for ALL element types
    const handle = (event.target as Element | null)?.closest<HTMLElement>('[data-resize-corner]')
    if (handle && this.selected) {
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
      // Always notify parent of the deselect — even when nothing was selected.
      // The parent uses this "id: null" message to trigger shape creation when a
      // drawing tool is active. Include canvas-space click coordinates so the
      // parent can place a newly created element at the pointer position.
      const scale = canvasScale(this.deck)
      const rect = this.deck.getBoundingClientRect()
      const canvasX = (event.clientX - rect.left) / scale
      const canvasY = (event.clientY - rect.top) / scale
      this.clearSelected(false)
      this.notifySelection(canvasX, canvasY)
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
    // Rotation drag
    if (this.rotation) {
      const g = geom(this.rotation.el)
      const scale = canvasScale(this.deck)
      const rect = this.deck.getBoundingClientRect()
      const cx = rect.left + (g.x + g.w / 2) * scale
      const cy = rect.top + (g.y + g.h / 2) * scale
      const currentAngle = Math.atan2(event.clientY - cy, event.clientX - cx) * 180 / Math.PI
      const delta = currentAngle - this.rotation.startAngle
      let rot = Math.round(this.rotation.startRot + delta)
      // Normalize to 0..359
      rot = ((rot % 360) + 360) % 360
      // Snap to 15° increments when close
      const snapped = Math.round(rot / 15) * 15
      if (Math.abs(rot - snapped) <= 3) rot = snapped % 360
      if (!this.rotation.moved && Math.abs(delta) < 2) return
      this.rotation.moved = true
      this.deck.setAttribute('data-edit-rotating', '')
      this.rotation.el.setAttribute('rot', String(rot))
      return
    }
    if (this.resize) {
      const scale = canvasScale(this.deck)
      const dx = (event.clientX - this.resize.pointerX) / scale
      const dy = (event.clientY - this.resize.pointerY) / scale
      if (!this.resize.moved && Math.hypot(dx, dy) < DRAG_THRESHOLD) return
      this.resize.moved = true
      this.deck.setAttribute('data-edit-resizing', '')
      // Images always resize proportionally; other elements use free resize
      // unless Shift is held, which forces proportional for any element type.
      const useProportional = this.resize.el.tagName === 'AST-IMAGE' || event.shiftKey
      const next = useProportional
        ? proportionalResize(this.resize.start, this.resize.corner, dx, dy)
        : freeResize(this.resize.start, this.resize.corner, dx, dy)
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
    if (this.rotation) {
      const moved = this.rotation.moved
      const el = this.rotation.el
      this.rotation = null
      this.deck.removeAttribute('data-edit-rotating')
      try {
        this.deck.releasePointerCapture(event.pointerId)
      } catch { /* already released / jsdom */ }
      if (moved) {
        // Record rotation as an attr change
        const rot = el.getAttribute('rot') || '0'
        if (el.id) {
          const existing = this.attrChanges.get(el.id) ?? {}
          existing['rot'] = rot
          this.attrChanges.set(el.id, existing)
        }
        this.notifyParent()
        this.notifySelection()
      }
      return
    }
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
    if (!this.selected) return
    const overlay = document.createElement('div')
    overlay.className = 'ast-edit-resize-handles'
    overlay.setAttribute('aria-hidden', 'true')
    for (const corner of ['nw', 'ne', 'se', 'sw'] as ResizeCorner[]) {
      const handle = document.createElement('span')
      handle.className = 'ast-edit-resize-handle'
      handle.dataset.resizeCorner = corner
      overlay.append(handle)
    }
    // Rotation handle: a circle above top-center connected by a thin line
    const rotLine = document.createElement('span')
    rotLine.className = 'ast-edit-rotation-line'
    overlay.append(rotLine)
    const rotHandle = document.createElement('span')
    rotHandle.className = 'ast-edit-rotation-handle'
    rotHandle.dataset.rotationHandle = ''
    overlay.append(rotHandle)
    this.deck.append(overlay)
    this.resizeHandles = overlay
    this.positionResizeHandles()
  }

  private positionResizeHandles(): void {
    if (!this.resizeHandles || !this.selected) return
    const g = geom(this.selected)
    const scale = canvasScale(this.deck)
    const handleSize = Math.round(24 / scale)
    const rotOffset = Math.round(40 / scale)
    Object.assign(this.resizeHandles.style, {
      left: `${g.x}px`, top: `${g.y}px`, width: `${g.w}px`, height: `${g.h}px`,
      '--ast-edit-handle-size': `${handleSize}px`,
      '--ast-edit-handle-offset': `${Math.round(handleSize / -2)}px`,
      '--ast-edit-rot-offset': `${rotOffset}px`,
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

const MIN_ELEMENT_SIZE = 16

/** Free (non-proportional) resize for shapes and text. */
export function freeResize(start: Geom, corner: ResizeCorner, dx: number, dy: number): Geom {
  let x = start.x
  let y = start.y
  let w = start.w
  let h = start.h

  if (corner.endsWith('e')) {
    w = Math.max(MIN_ELEMENT_SIZE, Math.round(start.w + dx))
  } else {
    w = Math.max(MIN_ELEMENT_SIZE, Math.round(start.w - dx))
    x = start.x + start.w - w
  }

  if (corner.startsWith('s')) {
    h = Math.max(MIN_ELEMENT_SIZE, Math.round(start.h + dy))
  } else {
    h = Math.max(MIN_ELEMENT_SIZE, Math.round(start.h - dy))
    y = start.y + start.h - h
  }

  return { x, y, w, h }
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
