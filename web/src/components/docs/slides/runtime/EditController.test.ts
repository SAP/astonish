import { afterEach, describe, expect, it, vi } from 'vitest'

import './index'
import { EditController } from './EditController'

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

  it('drags a selected element and posts ast-edit-moved', async () => {
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
      {
        type: 'ast-edit-moved',
        index: 0,
        changes: [{ id: 'headline', x: 220, y: 410 }],
      },
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
    expect(Number(text.getAttribute('x'))).toBe(160)
    expect(Number(text.getAttribute('y'))).toBe(380)
    editor.disconnect()
  })
})
