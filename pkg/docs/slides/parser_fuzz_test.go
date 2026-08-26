package slides

import "testing"

func FuzzParseSlide(f *testing.F) {
	f.Add(`<ast-slide id="s"><ast-text id="t" x="0" y="0" w="1" h="1">x</ast-text></ast-slide>`)
	f.Fuzz(func(t *testing.T, s string) { _, _, _ = ParseSlide(s) })
}
