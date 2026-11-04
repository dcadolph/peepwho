package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// mapsumPrefix marks a comment that vouches for one float accumulation inside
// a map range. The rest of the comment is the reason it cannot depend on order.
const mapsumPrefix = "// mapsum:"

// listedPackage is the subset of go list output the scan needs.
type listedPackage struct {
	// ImportPath is the package path.
	ImportPath string
	// Dir is the package directory.
	Dir string
	// GoFiles are the non-test Go files.
	GoFiles []string
	// Export is the compiled export data file.
	Export string
	// Error is set when go list could not load the package.
	Error *struct{ Err string }
	// Module identifies the owning module.
	Module *struct{ Main bool }
}

// mapsumHit is one float accumulation inside a range over a map.
type mapsumHit struct {
	// Stmt is the accumulating assignment.
	Stmt *ast.AssignStmt
	// Covered is true when a rule proves every enclosing map range cannot
	// change the result by its order.
	Covered bool
}

// TestMapOrderFloatSums fails on a float accumulated inside a range over a
// map unless a rule or a mapsum comment shows the order cannot change it.
// Go randomizes map order per process and float addition is not associative,
// so such a sum differs in its last bits between runs, and byte-for-byte
// reproducible output is a product claim.
func TestMapOrderFloatSums(t *testing.T) {
	t.Parallel()
	fset, pkgs := loadModule(t)
	if len(pkgs) == 0 {
		t.Fatal("go list found no packages in the main module")
	}
	total := 0
	for _, p := range pkgs {
		hits := scanMapSums(p.info, p.files)
		total += len(hits)
		for _, msg := range checkAnnotations(fset, p.files, hits) {
			t.Error(msg)
		}
	}
	if total == 0 {
		t.Fatal("scan reported no float accumulation in any map range: the loader is broken")
	}
}

// checkedPackage is one type-checked main-module package.
type checkedPackage struct {
	// files are the parsed non-test files.
	files []*ast.File
	// info holds the type-checker's results for files.
	info *types.Info
}

// loadModule lists, parses, and type-checks every main-module package.
func loadModule(t *testing.T) (*token.FileSet, []checkedPackage) {
	t.Helper()
	out, err := exec.Command("go", "list", "-e", "-export", "-deps", "-json", "./...").Output()
	if err != nil {
		t.Fatalf("go list failed: %v", err)
	}
	exports := make(map[string]string)
	var mine []listedPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listedPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("go list output decode failed: %v", err)
		}
		if p.Error != nil {
			t.Fatalf("go list failed on %s: %s", p.ImportPath, p.Error.Err)
		}
		exports[p.ImportPath] = p.Export
		if p.Module != nil && p.Module.Main {
			mine = append(mine, p)
		}
	}
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok || file == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(file)
	})
	var pkgs []checkedPackage
	for _, p := range mine {
		var files []*ast.File
		for _, name := range p.GoFiles {
			f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			files = append(files, f)
		}
		info, err := typeCheck(fset, p.ImportPath, files, imp)
		if err != nil {
			t.Fatalf("type check of %s failed: %v", p.ImportPath, err)
		}
		pkgs = append(pkgs, checkedPackage{files: files, info: info})
	}
	return fset, pkgs
}

// typeCheck runs the type checker over files and returns the first error, since
// the tree compiles and any error means the loader is wrong.
func typeCheck(fset *token.FileSet, path string, files []*ast.File, imp types.Importer) (*types.Info, error) {
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}
	conf := types.Config{Importer: imp}
	if _, err := conf.Check(path, fset, files, info); err != nil {
		return nil, err
	}
	return info, nil
}

// checkAnnotations returns one message per uncovered hit without a mapsum
// comment, per mapsum comment on a covered hit, per mapsum comment above no
// hit, and per mapsum comment with no reason.
func checkAnnotations(fset *token.FileSet, files []*ast.File, hits []mapsumHit) []string {
	type key struct {
		file string
		line int
	}
	notes := make(map[key]string)
	for _, f := range files {
		for _, g := range f.Comments {
			for i, c := range g.List {
				if !strings.HasPrefix(c.Text, mapsumPrefix) {
					continue
				}
				reason := strings.TrimSpace(strings.TrimPrefix(c.Text, mapsumPrefix))
				for _, rest := range g.List[i+1:] {
					reason += " " + strings.TrimSpace(strings.TrimPrefix(rest.Text, "//"))
				}
				end := fset.Position(g.End())
				notes[key{end.Filename, end.Line + 1}] = strings.TrimSpace(reason)
			}
		}
	}
	var msgs []string
	seen := make(map[key]bool)
	for _, h := range hits {
		pos := fset.Position(h.Stmt.Pos())
		k := key{pos.Filename, pos.Line}
		seen[k] = true
		reason, noted := notes[k]
		switch {
		case h.Covered && noted:
			msgs = append(msgs, fmt.Sprintf("%s: a rule already covers this, drop the mapsum comment", pos))
		case !h.Covered && !noted:
			msgs = append(msgs, fmt.Sprintf("%s: float accumulated in map order; walk the keys "+
				"sorted, or say why order cannot matter in a %q comment above it", pos, mapsumPrefix))
		case noted && reason == "":
			msgs = append(msgs, fmt.Sprintf("%s: mapsum comment gives no reason", pos))
		}
	}
	for k := range notes {
		if !seen[k] {
			msgs = append(msgs, fmt.Sprintf("%s:%d: mapsum comment sits above no map-order float "+
				"accumulation, remove it", k.file, k.line))
		}
	}
	slices.Sort(msgs)
	return msgs
}

// scanMapSums returns every float accumulation inside a range over a map in
// files, one entry per statement, covered only when every map range that
// reports it is covered by a rule.
func scanMapSums(info *types.Info, files []*ast.File) []mapsumHit {
	covered := make(map[*ast.AssignStmt]bool)
	var order []*ast.AssignStmt
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			rs, ok := n.(*ast.RangeStmt)
			if !ok {
				return true
			}
			kind := mapRangeKind(info, rs.X)
			if kind == notMapRange {
				return true
			}
			ast.Inspect(rs.Body, func(m ast.Node) bool {
				as, ok := m.(*ast.AssignStmt)
				if !ok || !isFloatAccumulation(info, rs, as) {
					return true
				}
				ok = coveredByRule(info, rs, kind, as)
				if prev, seen := covered[as]; seen {
					covered[as] = prev && ok
				} else {
					covered[as] = ok
					order = append(order, as)
				}
				return true
			})
			return true
		})
	}
	hits := make([]mapsumHit, 0, len(order))
	for _, as := range order {
		hits = append(hits, mapsumHit{Stmt: as, Covered: covered[as]})
	}
	return hits
}

// rangeKind says what a range statement walks.
type rangeKind int

// rangeKind values: not a map, a map or maps.Keys or maps.All (unique keys),
// or maps.Values (values may repeat).
const (
	notMapRange rangeKind = iota
	uniqueKeyRange
	valueRange
)

// mapRangeKind classifies the operand of a range statement.
func mapRangeKind(info *types.Info, x ast.Expr) rangeKind {
	if t := info.TypeOf(x); t != nil {
		if _, ok := t.Underlying().(*types.Map); ok {
			return uniqueKeyRange
		}
	}
	call, ok := ast.Unparen(x).(*ast.CallExpr)
	if !ok {
		return notMapRange
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return notMapRange
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return notMapRange
	}
	pkg, ok := info.Uses[id].(*types.PkgName)
	if !ok || pkg.Imported().Path() != "maps" {
		return notMapRange
	}
	switch sel.Sel.Name {
	case "Keys", "All":
		return uniqueKeyRange
	case "Values":
		return valueRange
	}
	return notMapRange
}

// isFloatAccumulation reports whether as updates a float from its own prior
// value, unless it is a plain variable declared inside the body of rs. A field
// or element is reported even when its root is declared in the body, since the
// root may point at state shared between iterations.
func isFloatAccumulation(info *types.Info, rs *ast.RangeStmt, as *ast.AssignStmt) bool {
	if len(as.Lhs) != 1 || len(as.Rhs) != 1 {
		return false
	}
	t := info.TypeOf(as.Lhs[0])
	if t == nil {
		return false
	}
	if b, ok := t.Underlying().(*types.Basic); !ok || b.Info()&types.IsFloat == 0 {
		return false
	}
	switch as.Tok {
	case token.ADD_ASSIGN, token.SUB_ASSIGN, token.MUL_ASSIGN, token.QUO_ASSIGN:
	case token.ASSIGN:
		if !mentions(as.Rhs[0], as.Lhs[0]) {
			return false
		}
	default:
		return false
	}
	if id, ok := as.Lhs[0].(*ast.Ident); ok {
		if obj := info.Uses[id]; obj != nil && within(obj.Pos(), rs.Body) {
			return false
		}
	}
	return true
}

// coveredByRule reports whether one of three rules proves the order of rs
// cannot change what as computes. Per-key: the left side is X[k] for the
// range's own unique key k, so each entry is touched once per walk. Extremum:
// x = max(x, ...) or min with the builtin, which is exact in any order.
// Fresh variable: the left side is the range's own key or value variable,
// which starts over each iteration. The first two also require every other input to be
// the range key or value or a variable the body never sets, since state
// carried between iterations brings the order back in.
func coveredByRule(info *types.Info, rs *ast.RangeStmt, kind rangeKind, as *ast.AssignStmt) bool {
	keyObj, valObj := rangeVar(info, rs.Key), rangeVar(info, rs.Value)
	lhs := ast.Unparen(as.Lhs[0])
	if id, ok := lhs.(*ast.Ident); ok {
		if obj := info.Uses[id]; obj != nil && (obj == keyObj || obj == valObj) {
			return true
		}
	}
	clean := func(e ast.Expr) bool { return readsOnlyStable(info, rs, e, keyObj, valObj) }
	if ix, ok := lhs.(*ast.IndexExpr); ok && kind == uniqueKeyRange && as.Tok != token.ASSIGN {
		id, ok := ast.Unparen(ix.Index).(*ast.Ident)
		if ok && keyObj != nil && info.Uses[id] == keyObj && clean(as.Rhs[0]) {
			return true
		}
	}
	call, ok := ast.Unparen(as.Rhs[0]).(*ast.CallExpr)
	if !ok || as.Tok != token.ASSIGN {
		return false
	}
	fn, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok {
		return false
	}
	if b, ok := info.Uses[fn].(*types.Builtin); !ok || (b.Name() != "max" && b.Name() != "min") {
		return false
	}
	self := false
	for _, arg := range call.Args {
		if types.ExprString(arg) == types.ExprString(as.Lhs[0]) {
			self = true
			continue
		}
		if !clean(arg) {
			return false
		}
	}
	return self
}

// rangeVar returns the object a range key or value expression names, or nil.
func rangeVar(info *types.Info, e ast.Expr) types.Object {
	id, ok := e.(*ast.Ident)
	if !ok || id.Name == "_" {
		return nil
	}
	if obj := info.Defs[id]; obj != nil {
		return obj
	}
	return info.Uses[id]
}

// readsOnlyStable reports whether every root identifier e reads is the range
// key or value, or an object the body of rs neither declares nor assigns.
func readsOnlyStable(info *types.Info, rs *ast.RangeStmt, e ast.Expr, keyObj, valObj types.Object) bool {
	set := assignedIn(info, rs.Body)
	ok := true
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		if !ok {
			return false
		}
		switch n := n.(type) {
		case *ast.SelectorExpr:
			ast.Inspect(n.X, visit)
			return false
		case *ast.Ident:
			obj := info.Uses[n]
			if obj == nil || obj == keyObj || obj == valObj {
				return true
			}
			if set[obj] || within(obj.Pos(), rs.Body) {
				ok = false
			}
		}
		return true
	}
	ast.Inspect(e, visit)
	return ok
}

// assignedIn returns the root objects of every assignment and increment in body.
func assignedIn(info *types.Info, body *ast.BlockStmt) map[types.Object]bool {
	set := make(map[types.Object]bool)
	add := func(e ast.Expr) {
		if id := rootIdent(e); id != nil {
			if obj := info.Uses[id]; obj != nil {
				set[obj] = true
			}
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for _, l := range n.Lhs {
				add(l)
			}
		case *ast.IncDecStmt:
			add(n.X)
		}
		return true
	})
	return set
}

// rootIdent strips index, selector, star, and paren expressions down to the
// identifier being written, or returns nil.
func rootIdent(e ast.Expr) *ast.Ident {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x
		case *ast.IndexExpr:
			e = x.X
		case *ast.SelectorExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		default:
			return nil
		}
	}
}

// mentions reports whether expr contains an expression spelled like lhs.
func mentions(expr, lhs ast.Expr) bool {
	want := types.ExprString(lhs)
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if e, ok := n.(ast.Expr); ok && types.ExprString(e) == want {
			found = true
		}
		return !found
	})
	return found
}

// within reports whether pos falls inside node.
func within(pos token.Pos, node ast.Node) bool {
	return pos >= node.Pos() && pos <= node.End()
}

// TestScanMapSums checks each rule against small sources, including the
// shapes that look safe and are not.
func TestScanMapSums(t *testing.T) {
	t.Parallel()
	tests := []struct {
		WantCovered []bool
		Body        string
	}{{ // Test 0: Plain sum over a map is reported and uncovered.
		Body:        `var s float64; for _, w := range m { s += w }; _ = s`,
		WantCovered: []bool{false},
	}, { // Test 1: Per-key update from the range value is covered.
		Body:        `x := map[string]float64{}; for k, w := range m { x[k] += w }`,
		WantCovered: []bool{true},
	}, { // Test 2: Per-key update reading carried state is not covered.
		Body: `var s float64; x := map[string]float64{}
			for k, w := range m { s += w; x[k] += s }`,
		WantCovered: []bool{false, false},
	}, { // Test 3: Per-key update over maps.Values is not covered, values repeat.
		Body:        `x := map[float64]float64{}; for v := range maps.Values(m) { x[v] += 1.5 }`,
		WantCovered: []bool{false},
	}, { // Test 4: Builtin max of the range value is covered.
		Body:        `var b float64; for _, w := range m { b = max(b, w) }; _ = b`,
		WantCovered: []bool{true},
	}, { // Test 5: Builtin max of carried state is not covered.
		Body:        `var b, p float64; for _, w := range m { b = max(b, w-p); p = w }; _ = b`,
		WantCovered: []bool{false},
	}, { // Test 6: Range value scaled in its own iteration is covered.
		Body:        `for _, w := range m { w *= 2; _ = w }`,
		WantCovered: []bool{true},
	}, { // Test 7: Outer slice value multiplied in a nested map range is not covered.
		Body:        `for _, sc := range []float64{1} { for _, f := range m { sc *= f }; _ = sc }`,
		WantCovered: []bool{false},
	}, { // Test 8: A sum over maps.Keys is reported.
		Body:        `var s float64; for k := range maps.Keys(m) { s += m[k] }; _ = s`,
		WantCovered: []bool{false},
	}, { // Test 9: A sum over sorted keys is a slice walk and not reported.
		Body:        `var s float64; for _, k := range slices.Sorted(maps.Keys(m)) { s += m[k] }; _ = s`,
		WantCovered: nil,
	}, { // Test 10: An accumulator declared in the body is not reported.
		Body:        `for _, w := range m { var s float64; s += w; _ = s }`,
		WantCovered: nil,
	}, { // Test 11: Division is reported.
		Body:        `s := 1.0; for _, w := range m { s /= w }; _ = s`,
		WantCovered: []bool{false},
	}, { // Test 12: A field through a pointer declared in the body is reported.
		Body:        `acc := &struct{ s float64 }{}; for _, w := range m { p := acc; p.s += w }`,
		WantCovered: []bool{false},
	}, { // Test 13: A user function named max is not the builtin and is not covered.
		Body: `max := func(a, b float64) float64 { return a + b }
			var b float64; for _, w := range m { b = max(b, w) }; _ = b`,
		WantCovered: []bool{false},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			src := "package p\n\nimport (\n\t\"maps\"\n\t\"slices\"\n)\n\n" +
				"func use(m map[string]float64) { _ = slices.Sorted(maps.Keys(m)) }\n\n" +
				"func f(m map[string]float64) {\n" + test.Body + "\n}\n"
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "p.go", src, 0)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			info, err := typeCheck(fset, "p", []*ast.File{f}, importer.Default())
			if err != nil {
				t.Fatalf("type check failed: %v", err)
			}
			var got []bool
			for _, h := range scanMapSums(info, []*ast.File{f}) {
				got = append(got, h.Covered)
			}
			if diff := cmp.Diff(test.WantCovered, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
