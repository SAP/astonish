package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const codeDelegateGraphGroup = "mcp:codegraph"

func (m *SubAgentManager) codeDelegateDefaults(task SubAgentTask) bool {
	return m.Config.CodeMode && task.ToolSelectionMode == ChildToolSelectionPreferred &&
		!task.CustomPrompt && task.OverrideTools == nil && task.OverrideToolsets == nil
}

func codeDelegateStructuralTool(name string) bool {
	switch name {
	case "code_definition", "code_references", "repo_map":
		return true
	default:
		return false
	}
}

// addCodeDelegateTools promotes only the code-intelligence baseline from the
// frozen invocation scope. It never consults ambient tool instances, loads an
// unrelated MCP group, or widens strict/custom/sandbox override boundaries.
func (s *ChildToolScope) addCodeDelegateTools(ctx context.Context) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	readCtx := &minimalReadonlyContext{Context: ctx}
	var warnings []string
	if s.deferred[codeDelegateGraphGroup] != nil && !IsMCPGroupInaccessible(ctx, codeDelegateGraphGroup) {
		// With metadata, avoid even enumerating a group whose tools are all denied.
		known, permitted := false, false
		for _, entry := range s.metadata {
			if entry.GroupName != codeDelegateGraphGroup {
				continue
			}
			bare, valid := childMetadataBareName(entry.ToolName, entry.GroupName)
			if !valid {
				continue
			}
			known = true
			key := entry.GroupName + "/" + bare
			if !s.denied[bare] && !s.denied[key] && !excludedChildTools[bare] && !childDiscoveryHelper(bare) && !isToolDisabled(readCtx, bare, key) {
				permitted = true
			}
		}
		if !known || permitted {
			if err := s.loadDeferred(readCtx, codeDelegateGraphGroup); err != nil {
				// Code intelligence is a preference, not a prerequisite for the task.
				warnings = append(warnings, fmt.Sprintf("Code delegate codegraph unavailable: %v", err))
			}
		}
	}
	for _, entry := range s.entries {
		name := entry.tool.Name()
		key := entry.group + "/" + name
		if entry.group != codeDelegateGraphGroup && !(entry.group == "core" && codeDelegateStructuralTool(name)) {
			continue
		}
		if s.denied[name] || s.denied[key] || excludedChildTools[name] || childDiscoveryHelper(name) ||
			isToolDisabled(readCtx, name, key) || IsMCPGroupInaccessible(ctx, entry.group) {
			continue
		}
		s.initial = append(s.initial, entry)
	}
	// Stable provider declarations and handoff names regardless of map iteration
	// or overlapping explicit preferences. Keep exact wrapped executable objects.
	sort.SliceStable(s.initial, func(i, j int) bool {
		return s.initial[i].group+"/"+s.initial[i].tool.Name() < s.initial[j].group+"/"+s.initial[j].tool.Name()
	})
	seen := map[string]bool{}
	initial := s.initial[:0]
	for _, entry := range s.initial {
		if !seen[entry.tool.Name()] {
			seen[entry.tool.Name()] = true
			initial = append(initial, entry)
		}
	}
	s.initial = initial
	return warnings
}

// codeDelegateGuidance uses only the resolved initial declarations, not the
// parent's prompt/history or a claim that a configured server must be usable.
func codeDelegateGuidance(scope *ChildToolScope) string {
	var graph, structural []string
	if scope != nil {
		for _, entry := range scope.initial {
			if entry.group == codeDelegateGraphGroup {
				graph = append(graph, "`"+entry.tool.Name()+"`")
			} else if entry.group == "core" && codeDelegateStructuralTool(entry.tool.Name()) {
				structural = append(structural, "`"+entry.tool.Name()+"`")
			}
		}
	}
	sort.Strings(graph)
	sort.Strings(structural)
	var b strings.Builder
	b.WriteString("\n## Code Investigation\n")
	b.WriteString("- Code-intelligence tools are PRIMARY for repository questions and edits, even when the handoff prefers only core or other groups. Preferences never override permissions.\n")
	if len(graph) > 0 {
		b.WriteString("- Use codegraph FIRST to locate the relevant implementation and trace definitions, callers, and tests. These codegraph tools are already available initially: " + strings.Join(graph, ", ") + ". Do not start with repository-wide grep or directory traversal.\n")
	} else {
		b.WriteString("- Codegraph is unavailable in this child's initial scope (not configured, not permitted, or could not be resolved). Do not claim graph coverage; continue with the permitted structural tools and narrow file reads instead.\n")
	}
	if len(structural) > 0 {
		b.WriteString("- Use structural tools next for gaps the graph did not answer: " + strings.Join(structural, ", ") + ". Prefer code_definition for declarations, code_references for callers, and repo_map for structural orientation when available.\n")
	} else {
		b.WriteString("- Structural tools are unavailable in this child's initial scope. Use only permitted discovery and narrowly scoped file tools; do not claim unavailable tools were used.\n")
	}
	b.WriteString("- Reserve grep_search for narrow literal/string/configuration gaps, not symbol discovery or broad repository exploration.\n")
	b.WriteString("- Source returned by codegraph counts as already read. Do not re-read those same regions; read only identified missing source or non-code configuration.\n")
	b.WriteString("- Stop exploring once the implementation scope, affected callers, and verification are clear; carry out the assigned task rather than repeating discovery.\n")
	return b.String()
}
