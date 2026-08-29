export const runtimeStyles = `
  ast-deck { display:block; position:relative; width:1920px; height:1080px; overflow:hidden; transform-origin:top left; background:var(--ast-surface,#fff); color:var(--ast-ink,#172033); }
  /* Off-screen slides are display:none so their (potentially hundreds of)
     elements are never laid out; content-visibility:auto + contain-intrinsic-size
     lets the browser also skip rendering work for any slide that becomes
     visible-but-off-viewport (e.g. overview/scroll modes) without a layout
     pass over its whole subtree. See DeckController.applyState. */
  ast-slide { display:none; position:absolute; inset:0; width:1920px; height:1080px; overflow:hidden; content-visibility:auto; contain-intrinsic-size:1080px 1920px; }
  ast-slide[active] { display:block; content-visibility:visible; }
  /* overflow-x:clip (not hidden) keeps overflow-y visible so descenders
     ("g","y","world") are not shaved by a flush line box. overflow-clip-margin
     gives a little extra paint room for anti-aliased glyph edges. */
  ast-text { white-space:pre-wrap; overflow-wrap:break-word; overflow-x:clip; overflow-y:visible; overflow-clip-margin:0.32em; font-variant-ligatures:none; }
  ast-run { display:inline; }
  ast-shape svg { width:100%; height:100%; display:block; overflow:visible; }
  ast-image img { width:100%; height:100%; object-fit:var(--ast-image-fit,contain); }
  ast-notes { display:none; }
  ast-fragment:not([revealed]) { visibility:hidden; }
  ast-deck[edit] { cursor:default; user-select:none; }
  ast-deck[edit] img, ast-deck[edit] svg { -webkit-user-drag:none; user-select:none; }
  ast-deck[edit] [data-edit-hover] { outline:2px solid var(--ast-accent,#2563eb); outline-offset:2px; cursor:grab; }
  ast-deck[edit] [data-edit-selected] { outline:2px solid var(--ast-accent,#2563eb); outline-offset:2px; cursor:grab; }
  ast-deck[edit][data-edit-dragging],
  ast-deck[edit][data-edit-dragging] [data-edit-selected] { cursor:grabbing; user-select:none; }
  ast-deck[edit][data-edit-resizing] { user-select:none; }
  ast-deck[edit] ast-text[data-edit-text] { cursor:text; outline:2px solid var(--ast-accent,#2563eb); outline-offset:2px; caret-color:var(--ast-ink,#172033); user-select:text; }
  ast-deck[edit] .ast-edit-resize-handles { position:absolute; box-sizing:border-box; pointer-events:none; z-index:21; }
  ast-deck[edit] .ast-edit-resize-handle { position:absolute; width:var(--ast-edit-handle-size,24px); height:var(--ast-edit-handle-size,24px); border:3px solid var(--ast-surface,#fff); border-radius:50%; background:var(--ast-accent,#2563eb); box-shadow:0 0 0 2px color-mix(in srgb,var(--ast-ink,#172033) 35%,transparent); pointer-events:auto; touch-action:none; }
  ast-deck[edit] .ast-edit-resize-handle[data-resize-corner="nw"] { left:var(--ast-edit-handle-offset,-12px); top:var(--ast-edit-handle-offset,-12px); cursor:nwse-resize; }
  ast-deck[edit] .ast-edit-resize-handle[data-resize-corner="ne"] { right:var(--ast-edit-handle-offset,-12px); top:var(--ast-edit-handle-offset,-12px); cursor:nesw-resize; }
  ast-deck[edit] .ast-edit-resize-handle[data-resize-corner="se"] { right:var(--ast-edit-handle-offset,-12px); bottom:var(--ast-edit-handle-offset,-12px); cursor:nwse-resize; }
  ast-deck[edit] .ast-edit-resize-handle[data-resize-corner="sw"] { left:var(--ast-edit-handle-offset,-12px); bottom:var(--ast-edit-handle-offset,-12px); cursor:nesw-resize; }
  ast-deck[edit] .ast-edit-guides { position:absolute; left:0; top:0; width:1920px; height:1080px; pointer-events:none; z-index:20; overflow:visible; }
  ast-deck[edit] .ast-edit-guides line { stroke:#f43f5e; stroke-width:2; stroke-dasharray:8 6; fill:none; vector-effect:non-scaling-stroke; }
  @media print {
    /* Page + slide dimensions must match the PDF paper set in
       pkg/docs/slides/export_pdf.go (20in x 11.25in == 1920x1080px at 96dpi).
       Any mismatch makes Chrome scale-to-fit (white margins) and spill each
       page-sized slide onto a trailing blank page. */
    @page { size:20in 11.25in; margin:0; }
    html, body { margin:0; padding:0; width:20in; overflow:visible; }
    ast-deck { transform:none!important; position:static!important; width:20in; height:auto; overflow:visible; background:transparent!important; }
    ast-slide { display:block!important; position:relative!important; inset:auto!important; width:20in!important; height:11.25in!important; overflow:hidden; break-inside:avoid; break-after:page; page-break-after:always; }
    ast-slide:last-of-type { break-after:auto; page-break-after:auto; }
    ast-fragment { visibility:visible!important; }
  }
`

export function installRuntimeStyles(): void {
  if (document.getElementById('astonish-slides-runtime-styles')) return
  const style = document.createElement('style')
  style.id = 'astonish-slides-runtime-styles'
  style.textContent = runtimeStyles
  document.head.append(style)
}
