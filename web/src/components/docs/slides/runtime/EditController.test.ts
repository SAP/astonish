import { afterEach, describe, expect, it, test, vi } from 'vitest'

import './index'
import { EditController, freeResize, proportionalResize, snapToAlign } from './EditController'

async function mount(): Promise<{ deck: HTMLElement; text: HTMLElement; bg: HTMLElement }> {
  document.body.innerHTML = `
    <ast-deck>
      <ast-slide id="s0" active>
        <ast-shape id="bg" x="0" y="0" w="1920" h="1080" decorative="true"></ast-shape>
        <ast-text id="headline" x="160" y="380" w="400" h="80">Title</ast-text>
      </ast-slide>
    </ast-deck>`
  const deck = document.querySelector('ast-deck') as HTMLElement
  await customElements.whenDefined('ast-deck')
  await customElements.whenDefined('ast-text')
  await new Promise(resolve => setTimeout(resolve, 0))
  deck.querySelector('ast-slide')?.setAttribute('active', '')
  const text = deck.querySelector('#headline') as HTMLElement
  const bg = deck.querySelector('#bg') as HTMLElement
  return { deck, text, bg }
}

function pointer(type: string, x: number, y: number): PointerEvent {
  return new PointerEvent(type, {
    bubbles: true,
    cancelable: true,
    clientX: x,
    clientY: y,
    button: 0,
    pointerId: 1,
  })
}

function shiftPointer(type: string, x: number, y: number): PointerEvent {
  return new PointerEvent(type, {
    bubbles: true,
    cancelable: true,
    clientX: x,
    clientY: y,
    button: 0,
    pointerId: 1,
    shiftKey: true,
  })
}

function stubHit(stack: Element[]): void {
  Object.defineProperty(document, 'elementsFromPoint', {
    configurable: true,
    writable: true,
    value: () => stack,
  })
}

afterEach(() => {
  document.body.replaceChildren()
  vi.restoreAllMocks()
})

describe('EditController', () => {
  it('skips full-bleed decorative backgrounds when hit-testing', async () => {
    const { deck, text, bg } = await mount()
    stubHit([bg, text, deck])
    const editor = new EditController(deck)
    editor.enable()
    deck.dispatchEvent(pointer('pointermove', 10, 10))
    expect(text.hasAttribute('data-edit-hover')).toBe(true)
    expect(bg.hasAttribute('data-edit-hover')).toBe(false)
    editor.disconnect()
  })

  it('drags a selected element and posts ast-edit-changed', async () => {
    const { deck, text } = await mount()
    const postMessage = vi.fn()
    Object.defineProperty(window, 'parent', { value: { postMessage }, configurable: true })
    stubHit([text, deck])

    const editor = new EditController(deck)
    editor.enable()
    expect(deck.hasAttribute('edit')).toBe(true)

    deck.dispatchEvent(pointer('pointerdown', 200, 400))
    expect(text.hasAttribute('data-edit-selected')).toBe(true)

    deck.dispatchEvent(pointer('pointermove', 260, 430))
    expect(Number(text.getAttribute('x'))).toBe(220)
    expect(Number(text.getAttribute('y'))).toBe(410)

    deck.dispatchEvent(pointer('pointerup', 260, 430))
    expect(postMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        type: 'ast-edit-changed',
        index: 0,
        moves: [{ id: 'headline', x: 220, y: 410 }],
      }),
      '*',
    )
    editor.disconnect()
  })

  it('shows corner handles and resizes images proportionally', async () => {
    document.body.innerHTML = `
      <ast-deck>
        <ast-slide id="s0" active>
          <ast-image id="photo" x="100" y="120" w="400" h="200" src="photo.png"></ast-image>
        </ast-slide>
      </ast-deck>`
    const deck = document.querySelector('ast-deck') as HTMLElement
    await customElements.whenDefined('ast-image')
    await new Promise(resolve => setTimeout(resolve, 0))
    deck.querySelector('ast-slide')?.setAttribute('active', '')
    const image = deck.querySelector('#photo') as HTMLElement
    const postMessage = vi.fn()
    Object.defineProperty(window, 'parent', { value: { postMessage }, configurable: true })
    stubHit([image, deck])

    const editor = new EditController(deck)
    editor.enable()
    deck.dispatchEvent(pointer('pointerdown', 200, 160))
    deck.dispatchEvent(pointer('pointerup', 200, 160))
    const handle = deck.querySelector<HTMLElement>('[data-resize-corner="se"]')!
    expect(handle).not.toBeNull()

    handle.dispatchEvent(pointer('pointerdown', 500, 320))
    deck.dispatchEvent(pointer('pointermove', 700, 380))
    deck.dispatchEvent(pointer('pointerup', 700, 380))

    expect(Number(image.getAttribute('w'))).toBe(600)
    expect(Number(image.getAttribute('h'))).toBe(300)
    expect(Number(image.getAttribute('w')) / Number(image.getAttribute('h'))).toBe(2)
    expect(postMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        type: 'ast-edit-changed',
        resizes: [{ id: 'photo', x: 100, y: 120, w: 600, h: 300 }],
      }),
      '*',
    )
    editor.disconnect()
  })

  it('keeps the opposite corner fixed when resizing from the northwest', () => {
    const resized = proportionalResize({ x: 100, y: 120, w: 400, h: 200 }, 'nw', -200, -20)
    expect(resized).toEqual({ x: -100, y: 20, w: 600, h: 300 })
  })

  it('reset restores the baseline position', async () => {
    const { deck, text } = await mount()
    stubHit([text, deck])
    Object.defineProperty(window, 'parent', { value: { postMessage: vi.fn() }, configurable: true })

    const editor = new EditController(deck)
    editor.enable()
    deck.dispatchEvent(pointer('pointerdown', 200, 400))
    deck.dispatchEvent(pointer('pointermove', 300, 500))
    deck.dispatchEvent(pointer('pointerup', 300, 500))
    expect(Number(text.getAttribute('x'))).not.toBe(160)

    editor.reset()
    expect(Number(deck.querySelector('#headline')?.getAttribute('x'))).toBe(160)
    expect(Number(deck.querySelector('#headline')?.getAttribute('y'))).toBe(380)
    editor.disconnect()
  })

  it('snaps left/center/right and top/bottom edges to siblings', () => {
    const left = snapToAlign(
      { x: 163, y: 12, w: 100, h: 40 },
      [{ x: 160, y: 200, w: 80, h: 40 }],
    )
    expect(left.x).toBe(160)
    expect(left.guides.some(g => g.axis === 'x' && g.pos === 160)).toBe(true)

    const center = snapToAlign(
      { x: 118, y: 10, w: 80, h: 40 },
      [{ x: 100, y: 200, w: 120, h: 40 }],
    )
    expect(center.x).toBe(120)
    expect(center.guides.some(g => g.axis === 'x' && g.pos === 160)).toBe(true)

    const top = snapToAlign(
      { x: 10, y: 203, w: 40, h: 80 },
      [{ x: 200, y: 200, w: 40, h: 80 }],
    )
    expect(top.y).toBe(200)
    expect(top.guides.some(g => g.axis === 'y' && g.pos === 200)).toBe(true)
  })

  it('draws an alignment guide while dragging near a sibling', async () => {
    document.body.innerHTML = `
      <ast-deck>
        <ast-slide id="s0" active>
          <ast-text id="a" x="160" y="100" w="200" h="40">A</ast-text>
          <ast-text id="b" x="160" y="400" w="200" h="40">B</ast-text>
        </ast-slide>
      </ast-deck>`
    const deck = document.querySelector('ast-deck') as HTMLElement
    await customElements.whenDefined('ast-text')
    await new Promise(resolve => setTimeout(resolve, 0))
    deck.querySelector('ast-slide')?.setAttribute('active', '')
    const a = deck.querySelector('#a') as HTMLElement
    stubHit([a, deck])
    Object.defineProperty(window, 'parent', { value: { postMessage: vi.fn() }, configurable: true })

    const editor = new EditController(deck)
    editor.enable()
    deck.dispatchEvent(pointer('pointerdown', 200, 120))
    deck.dispatchEvent(pointer('pointermove', 204, 120))
    const line = deck.querySelector('.ast-edit-guides line')
    expect(line).not.toBeNull()
    expect(line?.getAttribute('x1')).toBe('160')
    editor.disconnect()
  })

  it('deletes the selected object and posts deletes', async () => {
    const { deck, text } = await mount()
    const postMessage = vi.fn()
    Object.defineProperty(window, 'parent', { value: { postMessage }, configurable: true })
    stubHit([text, deck])
    const editor = new EditController(deck)
    editor.enable()
    deck.dispatchEvent(pointer('pointerdown', 200, 400))
    deck.dispatchEvent(pointer('pointerup', 200, 400))
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Delete', bubbles: true, cancelable: true }))
    expect(deck.querySelector('#headline')).toBeNull()
    expect(postMessage).toHaveBeenCalledWith(
      expect.objectContaining({ type: 'ast-edit-changed', deletes: ['headline'] }),
      '*',
    )
    editor.disconnect()
  })

  it('reset restores a deleted object', async () => {
    const { deck, text } = await mount()
    stubHit([text, deck])
    const editor = new EditController(deck)
    editor.enable()
    deck.dispatchEvent(pointer('pointerdown', 200, 400))
    deck.dispatchEvent(pointer('pointerup', 200, 400))
    editor.deleteSelection()
    expect(deck.querySelector('#headline')).toBeNull()
    editor.reset()
    expect(deck.querySelector('#headline')).not.toBeNull()
    editor.disconnect()
  })

  it('edits ast-text on double-click', async () => {
    const { deck, text } = await mount()
    const postMessage = vi.fn()
    Object.defineProperty(window, 'parent', { value: { postMessage }, configurable: true })
    stubHit([text, deck])
    const editor = new EditController(deck)
    editor.enable()
    deck.dispatchEvent(new MouseEvent('dblclick', { bubbles: true, clientX: 200, clientY: 400 }))
    expect(text.getAttribute('contenteditable')).toBe('true')
    text.textContent = 'Hello'
    text.dispatchEvent(new Event('input', { bubbles: true }))
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    expect(text.getAttribute('contenteditable')).toBeNull()
    expect(postMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        type: 'ast-edit-changed',
        texts: [{ id: 'headline', text: 'Hello' }],
      }),
      '*',
    )
    editor.disconnect()
  })

  it('starts text edit on a second click of the selected ast-text', async () => {
    const { deck, text } = await mount()
    stubHit([text, deck])
    const editor = new EditController(deck)
    editor.enable()
    deck.dispatchEvent(pointer('pointerdown', 200, 400))
    deck.dispatchEvent(pointer('pointerup', 200, 400))
    expect(text.getAttribute('contenteditable')).toBeNull()
    deck.dispatchEvent(pointer('pointerdown', 200, 400))
    deck.dispatchEvent(pointer('pointerup', 200, 400))
    expect(text.getAttribute('contenteditable')).toBe('true')
    editor.disconnect()
  })

  it('setAttr changes fill on selected shape', async () => {
    document.body.innerHTML = `
      <ast-deck>
        <ast-slide id="s0" active>
          <ast-shape id="s1" x="10" y="20" w="100" h="50" kind="rect" fill="blue"></ast-shape>
        </ast-slide>
      </ast-deck>`
    const deck = document.querySelector('ast-deck') as HTMLElement
    await customElements.whenDefined('ast-deck')
    await customElements.whenDefined('ast-shape')
    await new Promise(resolve => setTimeout(resolve, 0))
    deck.querySelector('ast-slide')?.setAttribute('active', '')
    const shape = deck.querySelector('#s1') as HTMLElement
    const postMessage = vi.fn()
    Object.defineProperty(window, 'parent', { value: { postMessage }, configurable: true })
    stubHit([shape, deck])

    const editor = new EditController(deck)
    editor.enable()
    // Select the shape
    deck.dispatchEvent(pointer('pointerdown', 50, 40))
    deck.dispatchEvent(pointer('pointerup', 50, 40))
    expect(shape.hasAttribute('data-edit-selected')).toBe(true)

    editor.setAttr('fill', '#ff0000')
    expect(shape.getAttribute('fill')).toBe('#ff0000')
    expect(postMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        type: 'ast-edit-changed',
        attrs: [{ id: 's1', attrs: { fill: '#ff0000' } }],
      }),
      '*',
    )
    editor.disconnect()
  })

  it('setAttr rejects disallowed attribute', async () => {
    document.body.innerHTML = `
      <ast-deck>
        <ast-slide id="s0" active>
          <ast-shape id="s1" x="10" y="20" w="100" h="50" kind="rect"></ast-shape>
        </ast-slide>
      </ast-deck>`
    const deck = document.querySelector('ast-deck') as HTMLElement
    await customElements.whenDefined('ast-shape')
    await new Promise(resolve => setTimeout(resolve, 0))
    deck.querySelector('ast-slide')?.setAttribute('active', '')
    const shape = deck.querySelector('#s1') as HTMLElement
    Object.defineProperty(window, 'parent', { value: { postMessage: vi.fn() }, configurable: true })
    stubHit([shape, deck])

    const editor = new EditController(deck)
    editor.enable()
    deck.dispatchEvent(pointer('pointerdown', 50, 40))
    deck.dispatchEvent(pointer('pointerup', 50, 40))

    editor.setAttr('onclick', 'alert(1)')
    expect(shape.hasAttribute('onclick')).toBe(false)
    editor.disconnect()
  })

  it('createElement adds a new ast-shape to the active slide', async () => {
    document.body.innerHTML = `
      <ast-deck>
        <ast-slide id="s0" active>
        </ast-slide>
      </ast-deck>`
    const deck = document.querySelector('ast-deck') as HTMLElement
    await customElements.whenDefined('ast-deck')
    await new Promise(resolve => setTimeout(resolve, 0))
    deck.querySelector('ast-slide')?.setAttribute('active', '')
    const postMessage = vi.fn()
    Object.defineProperty(window, 'parent', { value: { postMessage }, configurable: true })

    const editor = new EditController(deck)
    editor.enable()
    editor.createElement('ast-shape', 100, 200, 300, 150, { fill: 'red', kind: 'rect' })

    const slide = deck.querySelector('ast-slide') as HTMLElement
    const created = slide.querySelector('ast-shape') as HTMLElement
    expect(created).not.toBeNull()
    expect(created.id).toBe('user-shape-1')
    expect(created.getAttribute('x')).toBe('100')
    expect(created.getAttribute('y')).toBe('200')
    expect(created.getAttribute('w')).toBe('300')
    expect(created.getAttribute('h')).toBe('150')
    expect(created.getAttribute('fill')).toBe('red')
    expect(created.getAttribute('kind')).toBe('rect')
    expect(created.hasAttribute('data-edit-selected')).toBe(true)
    expect(postMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        type: 'ast-edit-changed',
        creates: [{ id: 'user-shape-1', tag: 'ast-shape', attrs: { x: '100', y: '200', w: '300', h: '150', fill: 'red', kind: 'rect' } }],
      }),
      '*',
    )
    editor.disconnect()
  })

  it('setZOrder brings element to front', async () => {
    document.body.innerHTML = `
      <ast-deck>
        <ast-slide id="s0" active>
          <ast-shape id="a" x="10" y="10" w="100" h="100"></ast-shape>
          <ast-shape id="b" x="50" y="50" w="100" h="100"></ast-shape>
        </ast-slide>
      </ast-deck>`
    const deck = document.querySelector('ast-deck') as HTMLElement
    await customElements.whenDefined('ast-shape')
    await new Promise(resolve => setTimeout(resolve, 0))
    deck.querySelector('ast-slide')?.setAttribute('active', '')
    const a = deck.querySelector('#a') as HTMLElement
    Object.defineProperty(window, 'parent', { value: { postMessage: vi.fn() }, configurable: true })
    stubHit([a, deck])

    const editor = new EditController(deck)
    editor.enable()
    // Select element 'a' (which is first child)
    deck.dispatchEvent(pointer('pointerdown', 50, 50))
    deck.dispatchEvent(pointer('pointerup', 50, 50))
    expect(a.hasAttribute('data-edit-selected')).toBe(true)

    editor.setZOrder('front')
    const slide = deck.querySelector('ast-slide') as HTMLElement
    expect(slide.lastElementChild).toBe(a)
    editor.disconnect()
  })

  it('reset clears attribute changes', async () => {
    document.body.innerHTML = `
      <ast-deck>
        <ast-slide id="s0" active>
          <ast-shape id="s1" x="10" y="20" w="100" h="50" fill="blue"></ast-shape>
        </ast-slide>
      </ast-deck>`
    const deck = document.querySelector('ast-deck') as HTMLElement
    await customElements.whenDefined('ast-shape')
    await new Promise(resolve => setTimeout(resolve, 0))
    deck.querySelector('ast-slide')?.setAttribute('active', '')
    const shape = deck.querySelector('#s1') as HTMLElement
    Object.defineProperty(window, 'parent', { value: { postMessage: vi.fn() }, configurable: true })
    stubHit([shape, deck])

    const editor = new EditController(deck)
    editor.enable()
    deck.dispatchEvent(pointer('pointerdown', 50, 40))
    deck.dispatchEvent(pointer('pointerup', 50, 40))

    editor.setAttr('fill', '#ff0000')
    expect(shape.getAttribute('fill')).toBe('#ff0000')

    editor.reset()
    // After reset, the slide innerHTML is restored from snapshot
    const restored = deck.querySelector('#s1') as HTMLElement
    expect(restored.getAttribute('fill')).toBe('blue')
    editor.disconnect()
  })

  test('clicking empty canvas with nothing selected still sends ast-edit-selected id:null', async () => {
    const { deck } = await mount()
    const parentPost = vi.fn()
    Object.defineProperty(window, 'parent', { value: { postMessage: parentPost }, configurable: true })
    // stubHit returns null for any click (miss)
    stubHit([])

    const editor = new EditController(deck)
    editor.enable()

    // Click empty canvas — nothing is selected beforehand
    deck.dispatchEvent(pointer('pointerdown', 500, 500))
    deck.dispatchEvent(pointer('pointerup', 500, 500))

    // Should have sent ast-edit-selected with id: null
    const selectedMsg = parentPost.mock.calls.find(
      (c: unknown[]) => (c[0] as { type?: string }).type === 'ast-edit-selected'
    )
    expect(selectedMsg).toBeTruthy()
    expect((selectedMsg![0] as { id: unknown }).id).toBeNull()
    editor.disconnect()
  })

  it('freeResize resizes independently in each direction', () => {
    const start = { x: 100, y: 100, w: 200, h: 100 }
    // Drag SE handle right and down
    const se = freeResize(start, 'se', 50, 30)
    expect(se).toEqual({ x: 100, y: 100, w: 250, h: 130 })

    // Drag NW handle left and up (negative dx, negative dy)
    const nw = freeResize(start, 'nw', -50, -30)
    expect(nw).toEqual({ x: 50, y: 70, w: 250, h: 130 })

    // Drag NE handle right and up
    const ne = freeResize(start, 'ne', 50, -30)
    expect(ne).toEqual({ x: 100, y: 70, w: 250, h: 130 })

    // Drag SW handle left and down
    const sw = freeResize(start, 'sw', -50, 30)
    expect(sw).toEqual({ x: 50, y: 100, w: 250, h: 130 })
  })

  it('freeResize enforces minimum size', () => {
    const start = { x: 100, y: 100, w: 200, h: 100 }
    // Drag SE handle far inward — should not go below min size (16)
    const shrunk = freeResize(start, 'se', -300, -300)
    expect(shrunk.w).toBe(16)
    expect(shrunk.h).toBe(16)
  })

  it('shows resize handles on all element types (not just images)', async () => {
    document.body.innerHTML = `
      <ast-deck>
        <ast-slide id="s0" active>
          <ast-shape id="rect1" x="100" y="100" w="200" h="150" kind="rect" fill="blue"></ast-shape>
        </ast-slide>
      </ast-deck>`
    const deck = document.querySelector('ast-deck') as HTMLElement
    await customElements.whenDefined('ast-shape')
    await new Promise(resolve => setTimeout(resolve, 0))
    deck.querySelector('ast-slide')?.setAttribute('active', '')
    const shape = deck.querySelector('#rect1') as HTMLElement
    Object.defineProperty(window, 'parent', { value: { postMessage: vi.fn() }, configurable: true })
    stubHit([shape, deck])

    const editor = new EditController(deck)
    editor.enable()
    // Select the shape
    deck.dispatchEvent(pointer('pointerdown', 200, 175))
    deck.dispatchEvent(pointer('pointerup', 200, 175))
    expect(shape.hasAttribute('data-edit-selected')).toBe(true)

    // All four corner handles should be present
    for (const corner of ['nw', 'ne', 'se', 'sw']) {
      expect(deck.querySelector(`[data-resize-corner="${corner}"]`)).not.toBeNull()
    }
    editor.disconnect()
  })

  it('renders rotation handle on selected element', async () => {
    document.body.innerHTML = `
      <ast-deck>
        <ast-slide id="s0" active>
          <ast-shape id="rect1" x="100" y="100" w="200" h="150" kind="rect" fill="blue"></ast-shape>
        </ast-slide>
      </ast-deck>`
    const deck = document.querySelector('ast-deck') as HTMLElement
    await customElements.whenDefined('ast-shape')
    await new Promise(resolve => setTimeout(resolve, 0))
    deck.querySelector('ast-slide')?.setAttribute('active', '')
    const shape = deck.querySelector('#rect1') as HTMLElement
    Object.defineProperty(window, 'parent', { value: { postMessage: vi.fn() }, configurable: true })
    stubHit([shape, deck])

    const editor = new EditController(deck)
    editor.enable()
    deck.dispatchEvent(pointer('pointerdown', 200, 175))
    deck.dispatchEvent(pointer('pointerup', 200, 175))

    // Rotation handle and connecting line should be present
    expect(deck.querySelector('[data-rotation-handle]')).not.toBeNull()
    expect(deck.querySelector('.ast-edit-rotation-line')).not.toBeNull()
    expect(deck.querySelector('.ast-edit-rotation-handle')).not.toBeNull()
    editor.disconnect()
  })

  it('free-resizes non-image elements (ast-shape) without keeping aspect ratio', async () => {
    document.body.innerHTML = `
      <ast-deck>
        <ast-slide id="s0" active>
          <ast-shape id="rect1" x="100" y="100" w="200" h="200" kind="rect" fill="blue"></ast-shape>
        </ast-slide>
      </ast-deck>`
    const deck = document.querySelector('ast-deck') as HTMLElement
    await customElements.whenDefined('ast-shape')
    await new Promise(resolve => setTimeout(resolve, 0))
    deck.querySelector('ast-slide')?.setAttribute('active', '')
    const shape = deck.querySelector('#rect1') as HTMLElement
    const postMessage = vi.fn()
    Object.defineProperty(window, 'parent', { value: { postMessage }, configurable: true })
    stubHit([shape, deck])

    const editor = new EditController(deck)
    editor.enable()
    // Select
    deck.dispatchEvent(pointer('pointerdown', 200, 200))
    deck.dispatchEvent(pointer('pointerup', 200, 200))

    const handle = deck.querySelector<HTMLElement>('[data-resize-corner="se"]')!
    expect(handle).not.toBeNull()

    // Resize: drag SE handle right only (dx=100, dy=0) — should NOT enforce aspect ratio
    handle.dispatchEvent(pointer('pointerdown', 300, 300))
    deck.dispatchEvent(pointer('pointermove', 400, 300))
    deck.dispatchEvent(pointer('pointerup', 400, 300))

    // Width changed but height should stay the same (free resize, not proportional)
    expect(Number(shape.getAttribute('w'))).toBe(300)
    expect(Number(shape.getAttribute('h'))).toBe(200)
    editor.disconnect()
  })

  it('rotation drag updates rot attribute and records attr change', async () => {
    document.body.innerHTML = `
      <ast-deck>
        <ast-slide id="s0" active>
          <ast-shape id="rect1" x="100" y="100" w="200" h="200" kind="rect" fill="blue"></ast-shape>
        </ast-slide>
      </ast-deck>`
    const deck = document.querySelector('ast-deck') as HTMLElement
    await customElements.whenDefined('ast-shape')
    await new Promise(resolve => setTimeout(resolve, 0))
    deck.querySelector('ast-slide')?.setAttribute('active', '')
    const shape = deck.querySelector('#rect1') as HTMLElement
    const postMessage = vi.fn()
    Object.defineProperty(window, 'parent', { value: { postMessage }, configurable: true })
    stubHit([shape, deck])

    const editor = new EditController(deck)
    editor.enable()
    // Select
    deck.dispatchEvent(pointer('pointerdown', 200, 200))
    deck.dispatchEvent(pointer('pointerup', 200, 200))

    const rotHandle = deck.querySelector<HTMLElement>('[data-rotation-handle]')!
    expect(rotHandle).not.toBeNull()

    // Start rotation drag from the handle
    rotHandle.dispatchEvent(pointer('pointerdown', 200, 60))
    // Move to a different angle — enough to pass the threshold
    deck.dispatchEvent(pointer('pointermove', 350, 200))
    // Verify data-edit-rotating attribute is set during drag
    expect(deck.hasAttribute('data-edit-rotating')).toBe(true)

    deck.dispatchEvent(pointer('pointerup', 350, 200))
    // After release, data-edit-rotating should be cleared
    expect(deck.hasAttribute('data-edit-rotating')).toBe(false)

    // The rot attribute should have been set to a non-zero value
    const rot = Number(shape.getAttribute('rot'))
    expect(rot).not.toBe(0)

    // Should have notified parent with attr changes including rot
    const changedMsg = postMessage.mock.calls.find(
      (c: unknown[]) => {
        const msg = c[0] as { type?: string; attrs?: { id: string; attrs: Record<string, string> }[] }
        return msg.type === 'ast-edit-changed' && msg.attrs?.some(a => a.id === 'rect1' && 'rot' in a.attrs)
      }
    )
    expect(changedMsg).toBeTruthy()
    editor.disconnect()
  })

  it('clears rotation state on slide change', async () => {
    document.body.innerHTML = `
      <ast-deck>
        <ast-slide id="s0" active>
          <ast-shape id="rect1" x="100" y="100" w="200" h="200" kind="rect" fill="blue"></ast-shape>
        </ast-slide>
        <ast-slide id="s1">
          <ast-text id="t1" x="50" y="50" w="300" h="80">Slide 2</ast-text>
        </ast-slide>
      </ast-deck>`
    const deck = document.querySelector('ast-deck') as HTMLElement
    await customElements.whenDefined('ast-shape')
    await new Promise(resolve => setTimeout(resolve, 0))
    deck.querySelector('ast-slide')?.setAttribute('active', '')
    const shape = deck.querySelector('#rect1') as HTMLElement
    Object.defineProperty(window, 'parent', { value: { postMessage: vi.fn() }, configurable: true })
    stubHit([shape, deck])

    const editor = new EditController(deck)
    editor.enable()
    // Select
    deck.dispatchEvent(pointer('pointerdown', 200, 200))
    deck.dispatchEvent(pointer('pointerup', 200, 200))
    expect(shape.hasAttribute('data-edit-selected')).toBe(true)

    // Simulate slide change
    deck.dispatchEvent(new Event('ast-deck-change', { bubbles: true }))
    // Selection and handles should be cleared
    expect(shape.hasAttribute('data-edit-selected')).toBe(false)
    expect(deck.querySelector('[data-resize-corner]')).toBeNull()
    expect(deck.querySelector('[data-rotation-handle]')).toBeNull()
    editor.disconnect()
  })

  it('Shift+resize forces proportional resize on non-image elements', async () => {
    document.body.innerHTML = `
      <ast-deck>
        <ast-slide id="s0" active>
          <ast-shape id="rect1" x="100" y="100" w="200" h="200" kind="rect" fill="blue"></ast-shape>
        </ast-slide>
      </ast-deck>`
    const deck = document.querySelector('ast-deck') as HTMLElement
    await customElements.whenDefined('ast-shape')
    await new Promise(resolve => setTimeout(resolve, 0))
    deck.querySelector('ast-slide')?.setAttribute('active', '')
    const shape = deck.querySelector('#rect1') as HTMLElement
    Object.defineProperty(window, 'parent', { value: { postMessage: vi.fn() }, configurable: true })
    stubHit([shape, deck])

    const editor = new EditController(deck)
    editor.enable()
    // Select
    deck.dispatchEvent(pointer('pointerdown', 200, 200))
    deck.dispatchEvent(pointer('pointerup', 200, 200))

    const handle = deck.querySelector<HTMLElement>('[data-resize-corner="se"]')!
    expect(handle).not.toBeNull()

    // Shift+resize: drag SE handle right only (dx=100, dy=0) — should enforce proportional
    handle.dispatchEvent(pointer('pointerdown', 300, 300))
    deck.dispatchEvent(shiftPointer('pointermove', 400, 300))
    deck.dispatchEvent(pointer('pointerup', 400, 300))

    // With proportional resize on a 1:1 element, both w and h should change equally
    const w = Number(shape.getAttribute('w'))
    const h = Number(shape.getAttribute('h'))
    expect(w).toBe(h)
    // The element grew (was 200×200, dragged 100px right)
    expect(w).toBeGreaterThan(200)
    editor.disconnect()
  })

  it('clicking empty canvas sends ast-edit-selected with clickX/clickY coordinates', async () => {
    const { deck } = await mount()
    const parentPost = vi.fn()
    Object.defineProperty(window, 'parent', { value: { postMessage: parentPost }, configurable: true })
    // stubHit returns null for any click (miss)
    stubHit([])

    const editor = new EditController(deck)
    editor.enable()

    // Click empty canvas at (500, 500)
    deck.dispatchEvent(pointer('pointerdown', 500, 500))

    // Find the ast-edit-selected message
    const selectedMsg = parentPost.mock.calls.find(
      (c: unknown[]) => (c[0] as { type?: string }).type === 'ast-edit-selected'
    )
    expect(selectedMsg).toBeTruthy()
    const msg = selectedMsg![0] as { id: unknown; clickX?: number; clickY?: number }
    expect(msg.id).toBeNull()
    expect(typeof msg.clickX).toBe('number')
    expect(typeof msg.clickY).toBe('number')
    editor.disconnect()
  })

  it('creating then moving an element produces creates (updated position) and NO moves for that element', async () => {
    document.body.innerHTML = `
      <ast-deck>
        <ast-slide id="s0" active>
        </ast-slide>
      </ast-deck>`
    const deck = document.querySelector('ast-deck') as HTMLElement
    await customElements.whenDefined('ast-deck')
    await new Promise(resolve => setTimeout(resolve, 0))
    deck.querySelector('ast-slide')?.setAttribute('active', '')
    const parentPost = vi.fn()
    Object.defineProperty(window, 'parent', { value: { postMessage: parentPost }, configurable: true })

    const editor = new EditController(deck)
    editor.enable()

    // Create element at position (100, 200)
    editor.createElement('ast-shape', 100, 200, 300, 150, { fill: 'red', kind: 'rect' })
    const slide = deck.querySelector('ast-slide') as HTMLElement
    const created = slide.querySelector('ast-shape') as HTMLElement
    expect(created).not.toBeNull()
    expect(created.id).toBe('user-shape-1')

    // Now drag it — simulate selecting and moving
    stubHit([created, deck])
    deck.dispatchEvent(pointer('pointerdown', 200, 250))
    // Move far enough to exceed DRAG_THRESHOLD
    deck.dispatchEvent(pointer('pointermove', 300, 350))
    deck.dispatchEvent(pointer('pointerup', 300, 350))

    // Find the last ast-edit-changed message
    const changedCalls = parentPost.mock.calls.filter(
      (c: unknown[]) => (c[0] as { type?: string }).type === 'ast-edit-changed'
    )
    expect(changedCalls.length).toBeGreaterThan(0)
    const lastChanged = changedCalls[changedCalls.length - 1][0] as {
      moves: { id: string }[]
      creates?: { id: string; attrs: Record<string, string> }[]
    }

    // Should NOT have the created element in moves
    const movedIds = lastChanged.moves.map((m: { id: string }) => m.id)
    expect(movedIds).not.toContain('user-shape-1')

    // Should have the created element in creates with updated position
    expect(lastChanged.creates).toBeDefined()
    const createEntry = lastChanged.creates!.find((c: { id: string }) => c.id === 'user-shape-1')
    expect(createEntry).toBeDefined()
    // The element was dragged, so position should differ from original (100, 200)
    const cx = Number(createEntry!.attrs.x)
    const cy = Number(createEntry!.attrs.y)
    expect(cx).not.toBe(100)
    expect(cy).not.toBe(200)
    editor.disconnect()
  })
})
