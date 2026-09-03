import { afterEach, describe, expect, it, vi } from 'vitest'

import './index'
import { EditController, proportionalResize, snapToAlign } from './EditController'

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
})
