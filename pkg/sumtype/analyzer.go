package sumtype

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
	"log"
	"os"
	"path/filepath"
	"regexp"
)

var Analyzer = &analysis.Analyzer{
	Name:     "gosumtype",
	Doc:      "Searches for inexhaustive type switches over sum-types",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

// sumTypeDecl is a declaration of a sum type in a Go source file.
type sumTypeDecl struct {
	// The package path that contains this decl.
	Package *types.Package
	// The type named by this decl.
	TypeName string
	// The file path where this declaration was found.
	Path string
	// The line number where this declaration was found.
	Line int
}

// Location returns a short string describing where this declaration was found.
func (d sumTypeDecl) Location() string {
	return fmt.Sprintf("%s:%d", d.Path, d.Line)
}

func run(pass *analysis.Pass) (interface{}, error) {
	// pass.ResultOf[inspect.Analyzer] will be set if we've added inspect.Analyzer to Requires.
	inspector := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	nodeFilter := []ast.Node{ // filter needed nodes: visit only them
		(*ast.File)(nil),
		(*ast.TypeSwitchStmt)(nil),
	}

	var (
		fileToPkg   = map[*ast.File]*types.Package{}
		switchToPkg = map[*ast.TypeSwitchStmt]*types.Package{}
	)

	inspector.Preorder(nodeFilter, func(node ast.Node) {
		switch v := node.(type) {
		case *ast.File:
			fileToPkg[v] = pass.Pkg

		case *ast.TypeSwitchStmt:
			switchToPkg[v] = pass.Pkg
		}
	})

	decls, err := findSumTypeDecls(pass.Fset, fileToPkg)
	if err != nil {
		return nil, err
	}

	defs, errs := findSumTypeDefs(decls)
	if len(defs) == 0 {
		// todo handle multierr properly
		if len(errs) == 0 {
			return nil, nil
		}
		return nil, errs[0]
	}

	for swtch, pkg := range switchToPkg {
		err := checkSwitch(pass.Fset, pkg, defs, swtch)
		if err != nil {
			return nil, err
		}
	}

	return nil, nil
}

func findSumTypeDecls(fset *token.FileSet, filesToPkg map[*ast.File]*types.Package) ([]sumTypeDecl, error) {
	var decls []sumTypeDecl
	for file, pkg := range filesToPkg {
		pos := fset.Position(file.Pos())
		filename := pos.Filename
		if filepath.Base(filename) == "C" {
			// ignore (fake?) cgo files
			continue
		}

		fileDecls, err := sumTypeDeclSearch(filename)
		if err != nil {
			return nil, err
		}
		for i := range fileDecls {
			fileDecls[i].Package = pkg
		}
		decls = append(decls, fileDecls...)
	}
	return decls, nil
}

// sumTypeDeclSearch searches the given file for sum type declarations of the
// form `go-sumtype:decl ...`.
func sumTypeDeclSearch(path string) ([]sumTypeDecl, error) {
	var decls []sumTypeDecl

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	lineNum := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if !isSumTypeDecl(line) {
			continue
		}
		ty := parseSumTypeDecl(line)
		if len(ty) == 0 {
			continue
		}
		decls = append(decls, sumTypeDecl{
			TypeName: ty,
			Path:     path,
			Line:     lineNum,
		})
	}
	if err := scanner.Err(); err != nil {
		// A scanner can puke if it hits a line that is too long.
		// We assume such files won't contain any future decls and
		// otherwise move on.
		log.Printf("scan error reading '%s': %s", path, err)
	}
	return decls, nil
}

var reParseSumTypeDecl = regexp.MustCompile(`^//go-sumtype:decl\s+(\S+)\s*$`)

// parseSumTypeDecl parses the type name out of a sum type decl.
//
// If no such decl could be found, then this returns an empty string.
func parseSumTypeDecl(line []byte) string {
	caps := reParseSumTypeDecl.FindSubmatch(line)
	if len(caps) < 2 {
		return ""
	}
	return string(caps[1])
}

// isSumTypeDecl returns true if and only if this line in a Go source file
// is a sum type decl.
func isSumTypeDecl(line []byte) bool {
	variant1, variant2 := []byte("//go-sumtype:decl "), []byte("//go-sumtype:decl\t")
	return bytes.HasPrefix(line, variant1) || bytes.HasPrefix(line, variant2)
}
