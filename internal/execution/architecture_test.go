package execution

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// Production code must reach subprocesses through this package. The local
// backend owns the constructors; the folder picker is an audited host UI
// exception documented in docs/execution-boundary.md.
var subprocessConstructorAllowlist = map[string]bool{
	filepath.Join("internal", "execution", "local.go"):                   true,
	filepath.Join("internal", "folderpicker", "folderpicker_darwin.go"):  true,
	filepath.Join("internal", "folderpicker", "folderpicker_windows.go"): true,
	filepath.Join("internal", "folderpicker", "folderpicker_other.go"):   true,
}

func TestSubprocessConstructorsStayInsideTheBoundary(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	fileSet := token.NewFileSet()
	scanned := 0

	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name := entry.Name(); path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		scanned++
		if subprocessConstructorAllowlist[relative] {
			return nil
		}
		file, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok || pkg.Name != "exec" {
				return true
			}
			if selector.Sel.Name != "Command" && selector.Sel.Name != "CommandContext" {
				return true
			}
			t.Errorf("%s:%d: exec.%s outside the execution boundary; route it through internal/execution",
				relative, fileSet.Position(call.Pos()).Line, selector.Sel.Name)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if scanned < 20 {
		t.Fatalf("scanned only %d production files; the walk is not covering the repository", scanned)
	}
}
