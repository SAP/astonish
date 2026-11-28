package daemon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Guard the actual worker composition, not just the accounting constructor.
func TestBotKnowledgeWorkerRestoresAccounting(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "bot_knowledge_extraction.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, declaration := range file.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "reconcileKnowledge" {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok || pkg.Name != "bot" {
				return true
			}
			if selector.Sel.Name == "RestoreRunBudget" {
				t.Error("knowledge worker reinstates historic execution caps")
			}
			if selector.Sel.Name == "RestoreRunAccounting" {
				found = true
			}
			return true
		})
	}
	if !found {
		t.Fatal("knowledge worker does not restore cumulative accounting")
	}
}
