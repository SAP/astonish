import { afterEach, describe, expect, it, vi } from 'vitest'

import './index'

async function mount(markup: string): Promise<HTMLElement> {
  document.body.innerHTML = markup
  const deck = document.querySelector<HTMLElement>('ast-deck')!
  await customElements.whenDefined('ast-deck')
  await (deck as unknown as { updateComplete: Promise<unknown> }).updateComplete
  await new Promise(resolve => setTimeout(resolve, 0))
  return deck
}

afterEach(() => {
  document.body.replaceChildren()
  history.replaceState(null, '', location.pathname)
  delete document.documentElement.dataset.astRenderComplete
})

describe('slides runtime', () => {
  it('registers every public custom element deterministically', () => {
    for (const tag of ['ast-deck', 'ast-slide', 'ast-text', 'ast-shape', 'ast-image', 'ast-group', 'ast-table', 'ast-chart', 'ast-code', 'ast-icon', 'ast-notes', 'ast-fragment']) {
      expect(customElements.get(tag)).toBeTypeOf('function')
    }
  })

  it('navigates fragments before slides and emits state changes', async () => {
    const deck = await mount('<ast-deck><ast-slide id="one"><ast-fragment order="1">first</ast-fragment></ast-slide><ast-slide id="two"></ast-slide></ast-deck>')
    const changes = vi.fn()
    deck.addEventListener('ast-deck-change', changes)

    deck.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true }))
    await new Promise(resolve => setTimeout(resolve, 0))
    expect(document.querySelector('ast-fragment')).toHaveAttribute('revealed')
    expect(document.querySelector('#one')).toHaveAttribute('active')

    deck.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true }))
    await new Promise(resolve => setTimeout(resolve, 0))
    expect(document.querySelector('#two')).toHaveAttribute('active')
    expect(location.hash).toBe('#two')
    expect(changes).toHaveBeenCalled()
  })

  it('reveals all fragments and signals readiness in print mode', async () => {
    const ready = vi.fn()
    window.addEventListener('ast-render-complete', ready, { once: true })
    await mount('<ast-deck print><ast-slide id="one"><ast-fragment>first</ast-fragment></ast-slide></ast-deck>')
    expect(document.querySelector('ast-fragment')).toHaveAttribute('revealed')
    expect(document.documentElement.dataset.astRenderComplete).toBe('true')
    expect(ready).toHaveBeenCalledOnce()
  })

  it('applies logical geometry to positioned elements', async () => {
    await mount('<ast-deck><ast-slide id="one"><ast-text x="10" y="20" w="300" h="80">hello</ast-text></ast-slide></ast-deck>')
    const text = document.querySelector<HTMLElement>('ast-text')!
    await (text as unknown as { updateComplete: Promise<unknown> }).updateComplete
    expect(text.style.left).toBe('10px')
    expect(text.style.top).toBe('20px')
    expect(text.style.width).toBe('300px')
    expect(text.style.height).toBe('80px')
  })
})
