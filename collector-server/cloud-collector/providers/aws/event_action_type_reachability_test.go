package aws

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAllRunbookActionTypesAreReachable guards against aws_runbook.yaml
// referencing an action type with no case in executeAction's top-level
// switch on actionDef.Type — the exact bug behind #38393, where
// aws_get_cloudtrail_events sat nested inside aws_create_alarm's own
// switch alarmType instead of being a sibling case there, making every
// rule using it fail with "unsupported action type".
func TestAllRunbookActionTypesAreReachable(t *testing.T) {
	reachable := reachableActionTypes(t)

	rules, err := GetEventRules("")
	require.NoError(t, err)

	used := map[string]bool{}
	for _, rule := range rules.Rules {
		for _, action := range rule.Actions {
			used[action.Type] = true
		}
	}
	require.NotEmpty(t, used, "expected aws_runbook.yaml to define at least one action")

	for actionType := range used {
		assert.True(t, reachable[actionType],
			"action type %q is used in aws_runbook.yaml but has no case in executeAction's top-level switch on actionDef.Type", actionType)
	}
}

// reachableActionTypes parses this package's source (excluding test files)
// and returns the string-literal case labels of the switch on actionDef.Type
// inside executeAction. Parsing the source, rather than hand-maintaining a
// duplicate list here, means this test can't itself drift out of sync with
// the switch it's checking.
//
// Files are listed and parsed individually (rather than via parser.ParseDir,
// deprecated since Go 1.25) since this package has no build-tag-gated files
// for that deprecation's caveat to matter.
func reachableActionTypes(t *testing.T) map[string]bool {
	t.Helper()

	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	reachable := map[string]bool{}
	found := false
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)

		ast.Inspect(file, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok || sw.Tag == nil {
				return true
			}
			sel, ok := sw.Tag.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Type" {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "actionDef" {
				return true
			}
			found = true
			for _, stmt := range sw.Body.List {
				cc, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range cc.List {
					lit, ok := expr.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					unquoted, uerr := strconv.Unquote(lit.Value)
					require.NoError(t, uerr)
					reachable[unquoted] = true
				}
			}
			return false
		})
	}
	require.True(t, found, "could not find `switch actionDef.Type { ... }` in package aws")
	return reachable
}
