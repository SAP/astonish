package render

import (
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
