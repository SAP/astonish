package sandbox

import (
	"strings"
	"testing"
)

func TestBotHomeArchiveManifest(t *testing.T) {
	valid := func() *BotHomeArchiveManifest {
		return &BotHomeArchiveManifest{Version: 1, Binding: strings.Repeat("a", 64), Entries: []BotHomeArchiveEntry{{Root: BotHomeArchiveHome, Path: "report.bin", Size: 20 << 20, SHA256: strings.Repeat("b", 64), Mode: 0600}}}
	}
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*BotHomeArchiveManifest){
		func(m *BotHomeArchiveManifest) { m.Version = 0 },
		func(m *BotHomeArchiveManifest) { m.Binding = "" },
		func(m *BotHomeArchiveManifest) { m.Entries[0].Path = "../escape" },
		func(m *BotHomeArchiveManifest) { m.Entries[0].Path = ".ssh/key" },
		func(m *BotHomeArchiveManifest) { m.Entries[0].Root = "unknown" },
		func(m *BotHomeArchiveManifest) { m.Entries[0].Size = -1 },
		func(m *BotHomeArchiveManifest) { m.Entries[0].SHA256 = "bad" },
		func(m *BotHomeArchiveManifest) { m.Entries = append(m.Entries, m.Entries[0]) },
		func(m *BotHomeArchiveManifest) {
			m.Entries[0].Root = BotHomeArchivePrivate
			m.Entries[0].Size = 1 << 30
		},
	} {
		m := valid()
		mutate(m)
		if m.Validate() == nil {
			t.Fatalf("accepted invalid manifest: %+v", m)
		}
	}
}
