export const CANVAS_WIDTH = 1920
export const CANVAS_HEIGHT = 1080

export type FragmentPolicy = 'step' | 'final'

export type DeckChangeDetail = {
  index: number
  slideId: string
  fragment: number
}

export type SlideLifecycleDetail = DeckChangeDetail & {
  slide: AstSlideElement
}

export interface AstSlideElement extends HTMLElement {
  active: boolean
}

export interface AstFragmentElement extends HTMLElement {
  order: number
  revealed: boolean
}

export interface AstDeckElement extends HTMLElement {
  currentIndex: number
  fragment: number
  next(): void
  previous(): void
  goTo(indexOrId: number | string): void
  enterPresenter(): Window | null
  enterPrint(policy?: FragmentPolicy): void
  exitPrint(): void
}
