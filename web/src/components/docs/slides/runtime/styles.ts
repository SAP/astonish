export const runtimeStyles = `
  ast-deck { display:block; position:relative; width:1920px; height:1080px; overflow:hidden; transform-origin:top left; background:var(--ast-surface,#fff); color:var(--ast-ink,#172033); }
  ast-slide { display:none; position:absolute; inset:0; width:1920px; height:1080px; overflow:hidden; }
  ast-slide[active] { display:block; }
  ast-text { white-space:pre-wrap; overflow:hidden; }
  ast-run { display:inline; }
  ast-shape svg { width:100%; height:100%; display:block; overflow:visible; }
  ast-image img { width:100%; height:100%; object-fit:var(--ast-image-fit,contain); }
  ast-notes { display:none; }
  ast-fragment:not([revealed]) { visibility:hidden; }
  @media print {
    @page { size:12in 6.75in; margin:0; }
    html, body { margin:0; padding:0; }
    ast-deck { transform:none!important; width:1920px; height:auto; overflow:visible; }
    ast-slide { display:block!important; position:relative; break-after:page; page-break-after:always; }
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
