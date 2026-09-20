package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestExportedNamesDocumented checks declarations and tests on every platform,
// including fields on JSON wire types and Windows-only API structures.
func TestExportedNamesDocumented(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		check := func(id *ast.Ident, doc, comment *ast.CommentGroup) {
			if ast.IsExported(id.Name) && doc == nil && comment == nil {
				t.Errorf("%s: exported %s needs a doc comment", fset.Position(id.Pos()), id.Name)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.FuncDecl:
				check(n.Name, n.Doc, nil)
				return false
			case *ast.GenDecl:
				for _, spec := range n.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						doc := s.Doc
						if doc == nil {
							doc = n.Doc
						}
						check(s.Name, doc, s.Comment)
					case *ast.ValueSpec:
						doc := s.Doc
						if doc == nil {
							doc = n.Doc
						}
						for _, id := range s.Names {
							check(id, doc, s.Comment)
						}
					}
				}
			case *ast.Field:
				for _, id := range n.Names {
					check(id, n.Doc, n.Comment)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
