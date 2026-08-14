package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestOpenAICodexProductionIdentityHasNoStaticBypasses(t *testing.T) {
	t.Helper()
	serviceDir := "."
	openAIPkgDir := filepath.Join("..", "pkg", "openai")
	for _, dir := range []string{serviceDir, openAIPkgDir} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			fileSet := token.NewFileSet()
			parsed, err := parser.ParseFile(fileSet, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			ast.Inspect(parsed, func(node ast.Node) bool {
				switch typed := node.(type) {
				case *ast.BasicLit:
					if typed.Kind != token.STRING || name == "request.go" {
						return true
					}
					value, unquoteErr := strconv.Unquote(typed.Value)
					if unquoteErr == nil && strings.Contains(value, "codex-tui/") {
						t.Errorf("%s:%d constructs a codex-tui UA outside the unique openai request helper", path, fileSet.Position(typed.Pos()).Line)
					}
				case *ast.CallExpr:
					selector, ok := typed.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if (selector.Sel.Name == "Set" || selector.Sel.Name == "Add") && len(typed.Args) >= 2 && expressionContainsIdentifier(typed.Args[1], "codexCLIUserAgent") {
						t.Errorf("%s:%d writes codexCLIUserAgent directly to a header", path, fileSet.Position(typed.Pos()).Line)
					}
					if name != "openai_outbound_session_identity_facade.go" &&
						(selector.Sel.Name == "ResolveOpenAIOAuthIdentityPlan" || selector.Sel.Name == "ResolveOpenAIOAuthOutboundIdentity") {
						t.Errorf("%s:%d bypasses GetOrResolveOpenAIOAuthOutboundIdentity", path, fileSet.Position(typed.Pos()).Line)
					}
				}
				return true
			})
		}
	}
}

func expressionContainsIdentifier(expression ast.Expr, name string) bool {
	found := false
	ast.Inspect(expression, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if ok && identifier.Name == name {
			found = true
			return false
		}
		return !found
	})
	return found
}
