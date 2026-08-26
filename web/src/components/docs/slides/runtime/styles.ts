export const runtimeStyles = `
  ast-deck { display:block; position:relative; width:1920px; height:1080px; overflow:hidden; transform-origin:top left; background:var(--ast-surface,#fff); color:var(--ast-ink,#172033); }
  /* Off-screen slides are display:none so their (potentially hundreds of)
     elements are never laid out; content-visibility:auto + contain-intrinsic-size
     lets the browser also skip rendering work for any slide that becomes
     visible-but-off-viewport (e.g. overview/scroll modes) without a layout
     pass over its whole subtree. See DeckController.applyState. */
  ast-slide { display:none; position:absolute; inset:0; width:1920px; height:1080px; overflow:hidden; content-visibility:auto; contain-intrinsic-size:1080px 1920px; }
  ast-slide[active] { display:block; content-visibility:visible; }
  ast-text { white-space:pre-wrap; overflow:hidden; }
  ast-run { display:inline; }
  ast-shape svg { width:100%; height:100%; display:block; overflow:visible; }
  ast-image img { width:100%; height:100%; object-fit:var(--ast-image-fit,contain); }
  ast-notes { display:none; }
  ast-fragment:not([revealed]) { visibility:hidden; }
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
