import { afterEach, describe, expect, it } from 'vitest'

import './index'
import { AstImage } from './AstImage'

async function mountImage(markup: string): Promise<AstImage> {
  document.body.innerHTML = markup
  await customElements.whenDefined('ast-image')
  const image = document.querySelector<AstImage>('ast-image')!
  await (image as unknown as { updateComplete: Promise<unknown> }).updateComplete
  return image
}

afterEach(() => {
  document.body.replaceChildren()
})

describe('AstImage flip transforms', () => {
  it('composes scaleX(-1) with rotate() when flip-h and rot are set', async () => {
    const image = await mountImage('<ast-image x="0" y="0" w="100" h="100" rot="30" flip-h="true" src="foo.png"></ast-image>')
    expect(image.style.transform).toContain('scaleX(-1)')
    expect(image.style.transform).toContain('rotate(')
    expect(image.style.transformOrigin).toBe('center')
  })

  it('emits no transform when neither rot nor flip attributes are present', async () => {
    const image = await mountImage('<ast-image x="0" y="0" w="100" h="100" src="foo.png"></ast-image>')
    expect(image.style.transform).toBe('')
    expect(image.style.transformOrigin).toBe('')
  })
})
