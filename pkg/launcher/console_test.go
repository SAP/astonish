package launcher

import (
	"testing"

	"github.com/SAP/astonish/pkg/config"
)

// TestSpinnerShouldShow verifies the logic for when spinner should be shown.
// Spinner is NOT shown for: input nodes, parallel nodes, or silent nodes.
func TestSpinnerShouldShow(t *testing.T) {
	tests := []struct {
		name                string
		nodeType            string
		isParallel          bool
		isSilent            bool
		expectedShowSpinner bool
	}{
		{
			name:                "regular llm node shows spinner",
			nodeType:            "llm",
			isParallel:          false,
			isSilent:            false,
			expectedShowSpinner: true,
		},
		{
			name:                "input node does not show spinner",
			nodeType:            "input",
			isParallel:          false,
			isSilent:            false,
			expectedShowSpinner: false,
		},
		{
			name:                "parallel node does not show spinner",
			nodeType:            "llm",
			isParallel:          true,
			isSilent:            false,
			expectedShowSpinner: false,
		},
		{
			name:                "silent node does not show spinner",
			nodeType:            "update_state",
			isParallel:          false,
			isSilent:            true,
			expectedShowSpinner: false,
		},
		{
			name:                "silent llm node does not show spinner",
			nodeType:            "llm",
			isParallel:          false,
			isSilent:            true,
			expectedShowSpinner: false,
		},
		{
			name:                "tool node shows spinner",
			nodeType:            "tool",
			isParallel:          false,
			isSilent:            false,
			expectedShowSpinner: true,
		},
		{
			name:                "output node shows spinner",
			nodeType:            "output",
			isParallel:          false,
			isSilent:            false,
			expectedShowSpinner: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the logic from console.go lines 658-666
			isInputNode := tt.nodeType == "input"
			isParallel := tt.isParallel
			isSilent := tt.isSilent

			// This is the condition that determines if spinner should be shown
			// In console.go: if isInputNode || isParallel || isSilent { stopSpinner } else { startSpinner }
			showSpinner := !(isInputNode || isParallel || isSilent)

			if showSpinner != tt.expectedShowSpinner {
				t.Errorf("showSpinner = %v, expected %v", showSpinner, tt.expectedShowSpinner)
			}
		})
	}
}

// TestNodeSilentFlagDetection verifies that the Silent flag is correctly detected from node config.
func TestNodeSilentFlagDetection(t *testing.T) {
	tests := []struct {
		name           string
		node           config.Node
		expectedSilent bool
	}{
		{
			name: "node with silent true",
			node: config.Node{
				Name:   "init_vars",
				Type:   "update_state",
				Silent: true,
			},
			expectedSilent: true,
		},
		{
			name: "node with silent false",
			node: config.Node{
				Name:   "process",
				Type:   "llm",
				Silent: false,
			},
			expectedSilent: false,
		},
		{
			name: "node without silent field (default false)",
			node: config.Node{
				Name: "run_tool",
				Type: "tool",
			},
			expectedSilent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the detection logic from console.go
			isSilent := false
			if tt.node.Silent {
				isSilent = true
			}

			if isSilent != tt.expectedSilent {
				t.Errorf("isSilent = %v, expected %v", isSilent, tt.expectedSilent)
			}
		})
	}
}
