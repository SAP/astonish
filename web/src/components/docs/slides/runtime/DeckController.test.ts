import { afterEach, describe, expect, it, vi } from 'vitest'

import './index'
import type { AstDeckElement, AstFragmentElement, AstSlideElement } from './types'

const mountDeck = async () => {
  const host = document.createElement('div')
  Object.defineProperties(host, { clientWidth: { value: 960 }, clientHeight: { value: 540 } })
  host.innerHTML = `<ast-deck no-history>
    <ast-slide id="one"><ast-fragment order="2">B</ast-fragment><ast-fragment order="1">A</ast-fragment></ast-slide>
    <ast-slide id="two"><ast-notes>Notes</ast-notes></ast-slide>
  </ast-deck>`
  document.body.append(host)
  await Promise.resolve()
  await new Promise(resolve => setTimeout(resolve, 0))
  return host.querySelector('ast-deck') as AstDeckElement
}

afterEach(() => {
  document.body.replaceChildren()
  history.replaceState(null, '', '#')
  vi.restoreAllMocks()
})

describe('DeckController', () => {
  it('scales the fixed canvas and advances ordered fragments before slides', async () => {
    const deck = await mountDeck()
    const slides = [...deck.querySelectorAll('ast-slide')] as AstSlideElement[]
    const fragments = [...deck.querySelectorAll('ast-fragment')] as AstFragmentElement[]

    expect(deck.style.transform).toBe('scale(0.5)')
    expect(slides[0].active).toBe(true)
    deck.next()
    expect(fragments[1].revealed).toBe(true)
    expect(deck.fragment).toBe(1)
    deck.next()
    deck.next()
    expect(slides[1].active).toBe(true)
    expect(deck.currentIndex).toBe(1)
  })

  it('handles keyboard navigation and slide lifecycle events', async () => {
    const deck = await mountDeck()
    const leave = vi.fn()
    const enter = vi.fn()
    deck.querySelector('#one')?.addEventListener('ast-slide-leave', leave)
    deck.querySelector('#two')?.addEventListener('ast-slide-enter', enter)

    deck.dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true }))
    expect(deck.currentIndex).toBe(1)
    expect(leave).toHaveBeenCalledOnce()
    expect(enter).toHaveBeenCalledOnce()
  })

  it('reveals every slide and fragment during final print mode then restores state', async () => {
    const deck = await mountDeck()
    deck.enterPrint('final')
    expect(deck.hasAttribute('print')).toBe(true)
    expect([...deck.querySelectorAll<AstSlideElement>('ast-slide')].every(slide => slide.active)).toBe(true)
    expect([...deck.querySelectorAll<AstFragmentElement>('ast-fragment')].every(fragment => fragment.revealed)).toBe(true)

    deck.exitPrint()
    expect(deck.hasAttribute('print')).toBe(false)
    expect(deck.querySelectorAll('ast-slide[active]')).toHaveLength(1)
  })
})
