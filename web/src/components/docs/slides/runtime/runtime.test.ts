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

  it('posts ast-deck-change to the parent window when the active slide changes', async () => {
    const postMessage = vi.fn()
    Object.defineProperty(window, 'parent', { value: { postMessage }, configurable: true })
    const deck = await mount('<ast-deck><ast-slide id="one"></ast-slide><ast-slide id="two"></ast-slide></ast-deck>')
    ;(deck as unknown as { goTo: (n: number) => void }).goTo(1)
    await new Promise(resolve => setTimeout(resolve, 0))
    expect(postMessage).toHaveBeenCalledWith(
      expect.objectContaining({ type: 'ast-deck-change', index: 1, slideId: 'two' }),
      '*',
    )
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

  it('renders a rotated shape with a rotate() transform', async () => {
    await mount('<ast-deck><ast-slide id="one"><ast-shape id="s" kind="rect" x="0" y="0" w="100" h="100" rot="45" fill="#ff0000"></ast-shape></ast-slide></ast-deck>')
    const shape = document.querySelector<HTMLElement>('ast-shape')!
    await (shape as unknown as { updateComplete: Promise<unknown> }).updateComplete
    expect(shape.style.transform).toBe('rotate(45deg)')
    expect(shape.style.transformOrigin).toBe('center')
  })

  it('renders a gradient shape as an inline svg with a gradient definition', async () => {
    await mount('<ast-deck><ast-slide id="one"><ast-shape id="s" kind="rect" x="0" y="0" w="200" h="120" geom="roundRect">' +
      '<script type="application/json">{"kind":"linear","angle":90,"stops":[{"pos":0,"color":"#000000"},{"pos":100,"color":"#ffffff"}]}</script>' +
      '</ast-shape></ast-slide></ast-deck>')
    const shape = document.querySelector<HTMLElement>('ast-shape')!
    await (shape as unknown as { updateComplete: Promise<unknown> }).updateComplete
    expect(shape.querySelector('svg')).not.toBeNull()
    expect(shape.querySelector('linearGradient')).not.toBeNull()
    expect(shape.querySelectorAll('stop').length).toBe(2)
    const path = shape.querySelector('path')!
    expect(path.getAttribute('fill')).toMatch(/^url\(#/)
  })

  it('renders a custom-path shape with the supplied path d', async () => {
    await mount('<ast-deck><ast-slide id="one"><ast-shape id="s" kind="rect" x="0" y="0" w="100" h="100" path="M0 0 L50 50 L0 100 Z" line="#00ff00"></ast-shape></ast-slide></ast-deck>')
    const shape = document.querySelector<HTMLElement>('ast-shape')!
    await (shape as unknown as { updateComplete: Promise<unknown> }).updateComplete
    const path = shape.querySelector('path')!
    expect(path.getAttribute('d')).toBe('M0 0 L50 50 L0 100 Z')
    expect(path.getAttribute('stroke')).toBe('#00ff00')
  })

  it('renders multi-run text as styled spans', async () => {
    await mount('<ast-deck><ast-slide id="one"><ast-text id="t" x="0" y="0" w="400" h="80">' +
      '<ast-run b u color="#ff0000" font="Georgia" size="24">Bold</ast-run>' +
      '<ast-run i>Ital</ast-run>' +
      '</ast-text></ast-slide></ast-deck>')
    const text = document.querySelector<HTMLElement>('ast-text')!
    await (text as unknown as { updateComplete: Promise<unknown> }).updateComplete
    const spans = text.querySelectorAll('span')
    expect(spans.length).toBe(2)
    expect(spans[0]?.textContent).toBe('Bold')
    expect(spans[0]?.style.fontWeight).toBe('700')
    expect(spans[0]?.style.textDecoration).toBe('underline')
    expect(spans[0]?.style.color).toBe('rgb(255, 0, 0)')
    expect(spans[0]?.style.fontFamily).toBe('Georgia')
    expect(spans[0]?.style.fontSize).toBe('24px')
    expect(spans[1]?.textContent).toBe('Ital')
    expect(spans[1]?.style.fontStyle).toBe('italic')
  })
})
