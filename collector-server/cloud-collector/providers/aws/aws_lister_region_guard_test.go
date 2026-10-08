package aws

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The account config carries the account's default region, not the region a
// call is about. A function that takes a region, builds a client from that
// config and never points the config at the region calls the default region
// every time: a scan stores another region's resources under the region it was
// asked for, and a lookup reports "not found" for anything that lives elsewhere.
// That has shipped more than once, a few functions at a time, so this reads the
// package source and fails on any function that does it.
//
// Only an assignment to the config itself, to a copy of it, or to a client
// option counts. Stamping the region on a result does not: that is what the
// defect looks like. A function that ignores its region on purpose names the
// parameter _, as the Route 53 lister does.
func TestRegionParamReachesTheClient(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !takesRegion(fn) {
				continue
			}
			if !loadsAccountConfig(fn.Body) || pointsConfigAtARegion(fn.Body) {
				continue
			}
			t.Errorf("%s: %s takes a region and builds a client from the account config without setting that region on it", fset.Position(fn.Pos()), funcKey(fn))
		}
	}
}

func takesRegion(fn *ast.FuncDecl) bool {
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if name.Name == "region" || name.Name == "regionName" {
				return true
			}
		}
	}
	return false
}

// loadsAccountConfig reports whether the body keeps the config it loads. A stub
// that discards it (`_, err := getAwsConfigFromAccount(...)`) builds no client.
func loadsAccountConfig(body *ast.BlockStmt) bool {
	return len(accountConfigVars(body)) > 0
}

// accountConfigVars returns the variables holding the account config: the ones
// assigned from getAwsConfigFromAccount, and copies of those.
func accountConfigVars(body *ast.BlockStmt) map[string]bool {
	vars := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) == 0 || len(assign.Rhs) != 1 {
			return true
		}
		target, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || target.Name == "_" {
			return true
		}
		switch rhs := assign.Rhs[0].(type) {
		case *ast.CallExpr:
			if callee, ok := rhs.Fun.(*ast.Ident); ok && callee.Name == "getAwsConfigFromAccount" {
				vars[target.Name] = true
			}
			if sel, ok := rhs.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Copy" {
				if source, ok := sel.X.(*ast.Ident); ok && vars[source.Name] {
					vars[target.Name] = true
				}
			}
		case *ast.Ident:
			if vars[rhs.Name] {
				vars[target.Name] = true
			}
		}
		return true
	})
	return vars
}

// pointsConfigAtARegion reports whether the body sets a region on the account
// config, on a copy of it, or on a client through an option function.
func pointsConfigAtARegion(body *ast.BlockStmt) bool {
	targets := accountConfigVars(body)
	ast.Inspect(body, func(n ast.Node) bool {
		if lit, ok := n.(*ast.FuncLit); ok {
			for _, field := range lit.Type.Params.List {
				for _, name := range field.Names {
					targets[name.Name] = true
				}
			}
		}
		return true
	})

	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Region" {
					continue
				}
				if owner, ok := sel.X.(*ast.Ident); ok && targets[owner.Name] {
					found = true
				}
			}
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "WithRegion" {
				found = true
			}
		}
		return true
	})
	return found
}

func funcKey(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}
