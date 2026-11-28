package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Decode SGR backgrounds at each printed cell, including spaces. Text-only
// assertions cannot detect holes in a colored surface.
func sidebarBackgroundCells(text string) ([][]string, []string) {
	return sidebarColorCells(text, false)
}

func sidebarColorCells(text string, foreground bool) ([][]string, []string) {
	code, reset := 48, 49
	if foreground {
		code, reset = 38, 39
	}
	sgr := regexp.MustCompile(`\x1b\[([0-9;:]*)m`)
	bg := "default"
	rows := [][]string{{}}
	plain := []string{""}
	for len(text) > 0 {
		if loc := sgr.FindStringSubmatchIndex(text); loc != nil && loc[0] == 0 {
			params := text[loc[2]:loc[3]]
			if params == "" {
				params = "0"
			}
			values := strings.Split(params, ";")
			for i := 0; i < len(values); i++ {
				n, _ := strconv.Atoi(values[i])
				switch {
				case n == 0 || n == reset:
					bg = "default"
				case (n == 38 || n == 48) && i+4 < len(values) && values[i+1] == "2":
					if n == code {
						bg = strings.Join(values[i+2:i+5], ",")
					}
					i += 4
				case (n == 38 || n == 48) && i+2 < len(values) && values[i+1] == "5":
					if n == code {
						bg = "index:" + values[i+2]
					}
					i += 2
				case foreground && (n >= 30 && n <= 37 || n >= 90 && n <= 97):
					bg = fmt.Sprintf("ansi:%d", n)
				}
			}
			text = text[loc[1]:]
			continue
		}
		r := []rune(text)[0]
		text = strings.TrimPrefix(text, string(r))
		if r == '\n' {
			rows = append(rows, []string{})
			plain = append(plain, "")
			continue
		}
		rows[len(rows)-1] = append(rows[len(rows)-1], bg)
		plain[len(plain)-1] += string(r)
	}
	return rows, plain
}

func TestPlanSidebarBackgroundCells(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	m := sidebarTestModel(140, 65)
	defer m.cancel()
	m.theme = DefaultTheme()
	s := m.tr.Items[0].PlanSnapshot
	s.Steps = s.Steps[:3]
	for i := range s.Steps {
		s.Steps[i].Status = "complete"
	}
	s.Document.Results = "verified"
	m.refreshViewport()
	// Include the final screen background pass which formerly injected black
	// between nested colored spans.
	text := m.View().Content
	rows, plain := sidebarBackgroundCells(text)
	// Styling the separator must not spill into either neighboring surface.
	divider := m.transcriptWidth()
	for y, line := range plain {
		runes := []rune(line)
		if len(runes) <= divider+1 || runes[divider] != '│' {
			continue
		}
		if rows[y][divider-1] != rows[y][divider] || rows[y][divider+1] != "16,25,34" {
			t.Fatalf("divider row %d changed adjacent backgrounds: %v", y, rows[y][divider-1:divider+2])
		}
	}
	// Inspect only sidebar columns in the fully composed screen, so unrelated
	// transcript text cannot satisfy the assertions.
	for y := range rows {
		start := min(m.transcriptWidth()+1, len(rows[y]))
		end := min(start+planSidebarWidth, len(rows[y]))
		rows[y] = rows[y][start:end]
		runes := []rune(plain[y])
		plain[y] = string(runes[min(start, len(runes)):min(end, len(runes))])
	}
	for _, label := range []string{"PLAN PROGRESS", "Results recorded", "100%", "3/3 phases complete", "PHASES"} {
		found := false
		for y, line := range plain {
			if !strings.Contains(line, label) {
				continue
			}
			found = true
			for x := 1; x < 27; x++ {
				if rows[y][x] != "16,25,34" {
					t.Errorf("%s cell %d bg=%s, want panel", label, x, rows[y][x])
				}
			}
		}
		if !found {
			t.Fatal("missing " + label)
		}
	}
	for i := 1; i <= 3; i++ {
		label := fmt.Sprintf("Phase %d description", i)
		found := false
		for y, line := range plain {
			if !strings.Contains(line, label) {
				continue
			}
			found = true
			for x := 2; x < 26; x++ {
				if rows[y][x] != "24,35,46" {
					t.Errorf("%s cell %d bg=%s, want card", label, x, rows[y][x])
				}
			}
		}
		if !found {
			t.Fatal("missing card " + label)
		}
	}
}
