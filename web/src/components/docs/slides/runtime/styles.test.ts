import { describe, expect, it } from 'vitest'

import { runtimeStyles } from './styles'

// The print stylesheet is injected at runtime and, because it carries !important
// and is appended after the export document's <head> CSS, it is the authority
// for print layout. It MUST match the PDF paper set in
// pkg/docs/slides/export_pdf.go (20in x 11.25in == 1920x1080px at 96dpi).
// A mismatch (e.g. the old 12in x 6.75in) makes Chrome scale-to-fit — shrinking
// each slide with white margins — and spills the page-sized slide onto a
// trailing blank page. This test locks the print contract so that regression
// is caught in CI instead of only in an exported PDF.
describe('slides runtime print styles', () => {
  it('sizes the page and slides to the exact PDF paper (no scale-to-fit)', () => {
    expect(runtimeStyles).toContain('@page { size:20in 11.25in; margin:0; }')
    expect(runtimeStyles).toContain('width:20in!important')
    expect(runtimeStyles).toContain('height:11.25in!important')
  })

  it('paginates one slide per page without a trailing blank page', () => {
    expect(runtimeStyles).toContain('break-after:page')
    expect(runtimeStyles).toContain('break-inside:avoid')
    expect(runtimeStyles).toContain('ast-slide:last-of-type { break-after:auto; page-break-after:auto; }')
  })

  it('does not carry the stale 12in x 6.75in page size', () => {
    expect(runtimeStyles).not.toContain('12in 6.75in')
  })
})
