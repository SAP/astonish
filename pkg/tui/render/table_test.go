package render

import (
	"charm.land/lipgloss/v2"
	"strings"
	"testing"
)

func TestMarkdown_Table(t *testing.T) {
	src := strings.TrimSpace(`
Here is the table:

| Name | Type | Scope |
|------|------|-------|
| openstack-keystone | openstack_keystone | personal |
| openstack-keystone | openstack_keystone | team |

Done.
`)
	st := DefaultStyles()
	out := Markdown(src, 80, st)

	// Should not leave raw GFM pipes for the body.
	if strings.Contains(out, "|------|") || strings.Contains(out, "|------") {
		t.Fatalf("raw separator still present:\n%s", out)
	}
	// Aligned table uses │ or box separators.
	if !strings.Contains(out, "│") && !strings.Contains(out, "Name") {
		t.Fatalf("expected table formatting:\n%s", out)
	}
	if !strings.Contains(out, "openstack-keystone") {
		t.Fatalf("missing cell:\n%s", out)
	}
	if !strings.Contains(out, "Here is the table") {
		t.Fatalf("missing prose:\n%s", out)
	}
	if !strings.Contains(out, "Done") {
		t.Fatalf("missing trailing prose:\n%s", out)
	}
}

func TestMarkdown_HorizontalRule(t *testing.T) {
	st := DefaultStyles()
	out := Markdown("before\n---\nafter", 40, st)
	if strings.Contains(out, "---") && !strings.Contains(out, "─") {
		// raw --- should be replaced by box-drawing rule
		t.Fatalf("expected rule, got:\n%s", out)
	}
	if !strings.Contains(out, "─") {
		t.Fatalf("expected horizontal rule:\n%s", out)
	}
}

func TestIsTableSeparator(t *testing.T) {
	if !isTableSeparator([]string{"---", ":---", "---:", ":---:"}) {
		t.Fatal("expected separator")
	}
	if isTableSeparator([]string{"Name", "Type"}) {
		t.Fatal("not a separator")
	}
}

func TestFramedTableUnicodeWrapRaggedStreaming(t *testing.T) {
	st := Styles{NoColor: true, TableLayout: TableLayoutFramed}
	rows := []string{`| ID | Notes | Extra |`, `| --- | --- | --- |`, `| alpha | **界界** é 👩‍💻 escaped\|pipe longwordABCDEFGHIJKLMN |`, `| beta | incomplete`}
	for _, width := range []int{120, 40, 20} {
		out := renderTable(rows, width, st)
		if !strings.HasPrefix(out, "┌") || !strings.Contains(out, "└") || strings.Contains(out, "…") || strings.Contains(out, "\x1b") {
			t.Fatalf("invalid frame width %d: %s", width, out)
		}
		flat := strings.NewReplacer("\n", "", "│", "", " ", "").Replace(out)
		for _, want := range []string{"alpha", "beta", "界界", "é", "👩‍💻", "escaped|pipe", "longwordABCDEFGHIJKLMN", "incomplete"} {
			if !strings.Contains(flat, want) {
				t.Fatalf("lost %q width %d: %s", want, width, out)
			}
		}
		for _, line := range strings.Split(out, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("overflow %d > %d: %q", got, width, line)
			}
		}
	}
	if renderTable(nil, 40, st) != "" {
		t.Fatal("empty table not empty")
	}
	if strings.HasPrefix(renderTable(rows, 120, Styles{NoColor: true}), "┌") {
		t.Fatal("zero styles changed legacy layout")
	}
	if !strings.Contains(renderTable(rows, 20, st), "ID:") {
		t.Fatal("narrow rows need labels")
	}
}
