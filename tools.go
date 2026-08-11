//go:build tools

// Test change: this comment was added for testing and will be reverted.

package tools

import (
	_ "entgo.io/ent/entc"
	_ "entgo.io/ent/entc/gen"
	_ "entgo.io/ent/cmd/ent"
)
