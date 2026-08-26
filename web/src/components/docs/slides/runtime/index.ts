import { AstChart } from './AstChart'
import { AstCode } from './AstCode'
import { AstDeck } from './AstDeck'
import { AstFragment } from './AstFragment'
import { AstGroup } from './AstGroup'
import { AstIcon } from './AstIcon'
import { AstImage } from './AstImage'
import { AstNotes } from './AstNotes'
import { AstShape } from './AstShape'
import { AstSlide } from './AstSlide'
import { AstTable } from './AstTable'
import { AstText } from './AstText'
import { installRuntimeStyles } from './styles'

export { DeckController } from './DeckController'
export * from './types'

const definitions: ReadonlyArray<readonly [string, CustomElementConstructor]> = [
  ['ast-deck', AstDeck],
  ['ast-slide', AstSlide],
  ['ast-text', AstText],
  ['ast-shape', AstShape],
  ['ast-image', AstImage],
  ['ast-group', AstGroup],
  ['ast-notes', AstNotes],
  ['ast-table', AstTable],
  ['ast-chart', AstChart],
  ['ast-code', AstCode],
  ['ast-icon', AstIcon],
  ['ast-fragment', AstFragment],
]

export function registerSlidesRuntime(): void {
  if (typeof customElements === 'undefined') return
  for (const [name, constructor] of definitions) {
    if (!customElements.get(name)) customElements.define(name, constructor)
  }
  installRuntimeStyles()
}

registerSlidesRuntime()
