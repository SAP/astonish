// Lossless template IR types, mirroring pkg/docs/slides/themes/template_model.go.
//
// These describe the high-fidelity model produced by a .pptx import and persisted
// on the imported template deck. They exist so the future in-browser template
// editor (contenteditable placeholders + fill collection, as in the pptx-html
// pilot) can consume the lossless model directly. This module is TYPES ONLY —
// no runtime code — and nothing imports it yet in this version.
//
// Units match the Go side: logical pixels on the fixed 1920x1080 canvas and
// #RRGGBB colors (not the pilot's inches).

export interface IRSize {
  w: number
  h: number
}

export type IRBackground =
  | { kind: 'solid'; color?: string }
  | { kind: 'image'; mediaKey: string }

export interface IRFill {
  color?: string
  /** 0..100 percent. */
  transparency?: number
}

export interface IRLine {
  color?: string
  width?: number
  dash?: 'solid' | 'dash' | 'dot'
}

export interface IRTextStyle {
  fontFace?: string
  fontSize?: number
  color?: string
  bold?: boolean
  italic?: boolean
  underline?: boolean
  align?: 'left' | 'center' | 'right'
  valign?: 'top' | 'middle' | 'bottom'
  transparency?: number
}

/** One SVG-path subpath in the object's own w x h unit box. */
export interface IRPathSeg {
  d: string
  fillNone?: boolean
  w: number
  h: number
}

interface IRChromeBase {
  x: number
  y: number
  w: number
  h: number
  rot?: number
  name?: string
}

export interface IRRectChrome extends IRChromeBase {
  kind: 'rect'
  fill?: IRFill
  line?: IRLine
  rectRadius?: number
}

export interface IREllipseChrome extends IRChromeBase {
  kind: 'ellipse'
  fill?: IRFill
  line?: IRLine
}

export interface IRLineChrome extends IRChromeBase {
  kind: 'line'
  line?: IRLine
  flipH?: boolean
  flipV?: boolean
}

export interface IRTextChrome extends IRChromeBase {
  kind: 'text'
  text: string
  style?: IRTextStyle
}

export interface IRImageChrome extends IRChromeBase {
  kind: 'image'
  mediaKey: string
}

export interface IRPathChrome extends IRChromeBase {
  kind: 'path'
  paths: IRPathSeg[]
  fill?: IRFill
  line?: IRLine
}

export type IRChrome =
  | IRRectChrome
  | IREllipseChrome
  | IRLineChrome
  | IRTextChrome
  | IRImageChrome
  | IRPathChrome

export type IRPlaceholderType = 'title' | 'body' | 'image' | 'chart' | 'table' | 'media'

export interface IRPlaceholder {
  name: string
  type: IRPlaceholderType
  x: number
  y: number
  w: number
  h: number
  style: IRTextStyle
  prompt?: string
  ooxmlType?: string
  idx?: number
}

export interface IRSlideNumber {
  x: number
  y: number
  w?: number
  h?: number
  style: IRTextStyle
}

/** One flattened layout OR one flattened sample slide (same shape). */
export interface IRLayout {
  id: string
  name?: string
  background: IRBackground
  objects?: IRChrome[]
  placeholders?: IRPlaceholder[]
  slideNumber?: IRSlideNumber
}

export interface IRWarning {
  code: string
  message: string
  layout?: string
}

/** The top-level lossless IR for one imported template. */
export interface TemplateModel {
  schema: number
  size: IRSize
  theme?: Record<string, string>
  layouts?: IRLayout[]
  slides?: IRLayout[]
  warnings?: IRWarning[]
}
