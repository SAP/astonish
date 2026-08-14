package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/SAP/astonish/pkg/config"
)

// Define styles
var (
	// Colors
	colorLLM    = lipgloss.Color("63")  // Blueish
	colorTool   = lipgloss.Color("214") // Orange
	colorInput  = lipgloss.Color("86")  // Cyan
	colorState  = lipgloss.Color("204") // Pink
	colorGray   = lipgloss.Color("240")
	colorEnd    = lipgloss.Color("196") // Red
	colorSystem = lipgloss.Color("255") // White

	// Styles for different node types
	llmStyle    = lipgloss.NewStyle().Foreground(colorLLM).Bold(true)
	toolStyle   = lipgloss.NewStyle().Foreground(colorTool).Bold(true)
	inputStyle  = lipgloss.NewStyle().Foreground(colorInput).Bold(true)
	stateStyle  = lipgloss.NewStyle().Foreground(colorState).Bold(true)
	systemStyle = lipgloss.NewStyle().Foreground(colorSystem).Bold(true)
	endStyle    = lipgloss.NewStyle().Foreground(colorEnd).Bold(true)

	// Other styles
	conditionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Italic(true)
	loopStyle      = lipgloss.NewStyle().Foreground(colorGray).Italic(true)
)

// Max lengths for truncation to prevent overflow
const (
	maxNodeNameLen  = 40
	maxConditionLen = 60
	indentSpacing   = "  "
	connectorMid    = "├─ "
	connectorLast   = "└─ "
)

// RenderCharmFlow prints the flow using Lipgloss styles in a tree-like structure
func RenderCharmFlow(cfg *config.AgentConfig) {
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("255")).
		Background(lipgloss.Color("63")).
		Padding(0, 1).
		Render(" 🔮 ASTONISH FLOW: " + cfg.Description + " ")
	fmt.Println(title)
	fmt.Println()

	// Track visited nodes to detect loops and avoid infinite recursion
	visited := make(map[string]bool)

	// Start recursion from root
	renderNodeRecursive(cfg, "START", "", false, visited, false)
	fmt.Println()
}

// renderChildren handles rendering the outgoing edges and nodes from a given node
func renderChildren(cfg *config.AgentConfig, currentNode string, prefix string, visited map[string]bool) {
	// Find outgoing flows
	var children []struct {
		cond string
		to   string
	}
	found := false
	for _, flow := range cfg.Flow {
		if flow.From == currentNode {
			if len(flow.Edges) > 0 {
				for _, e := range flow.Edges {
					children = append(children, struct{ cond, to string }{e.Condition, e.To})
				}
			} else if flow.To != "" {
				children = append(children, struct{ cond, to string }{"", flow.To})
			}
			found = true
			break
		}
	}
	if !found {
		return
	}

	// Check if linear
	isLinear := len(children) == 1 && children[0].cond == ""

	if isLinear {
		edge := children[0]
		if visited[edge.to] {
			loopLine := prefix + connectorLast + loopStyle.Render("⟳ Loop to "+truncateString(edge.to, maxNodeNameLen))
			fmt.Println(loopLine)
		} else {
			renderNodeRecursive(cfg, edge.to, prefix, true, visited, true)
		}
	} else {
		// Branching
		for i := 0; i < len(children); i++ {
			edge := children[i]
			isTail := i == len(children)-1

			connector := connectorMid
			if isTail {
				connector = connectorLast
			}

			if edge.cond != "" {
				truncCond := truncateString(edge.cond, maxConditionLen)
				condLine := prefix + connector + conditionStyle.Render("["+truncCond+"]")
				fmt.Println(condLine)

				condPrefix := prefix + indentSpacing
				if isTail {
					condPrefix = prefix + strings.Repeat(" ", len(indentSpacing))
				}

				if visited[edge.to] {
					loopLine := condPrefix + connectorLast + loopStyle.Render("⟳ Loop to "+truncateString(edge.to, maxNodeNameLen))
					fmt.Println(loopLine)
				} else {
					renderNodeRecursive(cfg, edge.to, condPrefix, true, visited, true)
				}
			} else {
				// Rare case: no condition but multiple children (treat as branching without cond)
				if visited[edge.to] {
					loopLine := prefix + connector + loopStyle.Render("⟳ Loop to "+truncateString(edge.to, maxNodeNameLen))
					fmt.Println(loopLine)
				} else {
					renderNodeRecursive(cfg, edge.to, prefix, isTail, visited, true)
				}
			}
		}
	}
}

// renderNodeRecursive renders a node and its children recursively
func renderNodeRecursive(cfg *config.AgentConfig, currentNode string, prefix string, tail bool, visited map[string]bool, useConnector bool) {
	isEnd := currentNode == "END"
	icon, nodeStyle := getIconAndStyle(currentNode, getNodeType(cfg, currentNode), isEnd)
	truncName := truncateString(currentNode, maxNodeNameLen)
	styledNode := nodeStyle.Render(icon + " " + truncName)

	var nodeLine string
	if useConnector {
		connector := connectorMid
		if tail {
			connector = connectorLast
		}
		nodeLine = prefix + connector + styledNode
	} else {
		nodeLine = prefix + styledNode
	}
	fmt.Println(nodeLine)

	if isEnd {
		return
	}

	visited[currentNode] = true
	defer delete(visited, currentNode)

	renderChildren(cfg, currentNode, prefix, visited)
}

func getIconAndStyle(nodeName string, nodeType string, isEnd bool) (string, lipgloss.Style) {
	if isEnd {
		return "🏁", endStyle
	}
	switch nodeType {
	case "llm":
		return "🤖", llmStyle
	case "tool":
		return "🛠️", toolStyle
	case "input":
		return "📥", inputStyle
	case "update_state":
		return "💾", stateStyle
	case "system":
		return "⚡", systemStyle
	default:
		return "📦", lipgloss.NewStyle().Foreground(colorGray).Bold(true)
	}
}

func getNodeType(cfg *config.AgentConfig, name string) string {
	node := getNode(cfg, name)
	if node != nil {
		return node.Type
	}
	return "system" // Default for START/END
}

func getNode(cfg *config.AgentConfig, name string) *config.Node {
	for i := range cfg.Nodes {
		if cfg.Nodes[i].Name == name {
			return &cfg.Nodes[i]
		}
	}
	return nil
}

func truncateString(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
}
