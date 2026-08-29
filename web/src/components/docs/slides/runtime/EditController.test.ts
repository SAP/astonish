import { afterEach, describe, expect, it, vi } from 'vitest'

import './index'
import { EditController, snapToAlign } from './EditController'

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
})
