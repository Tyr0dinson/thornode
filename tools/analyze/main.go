package main //nolint:govet

import (
	"errors"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/multichecker"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"

	"gitlab.com/thorchain/thornode/v3/constants"
)

// -------------------------------------------------------------------------------------
// MapIteration
// -------------------------------------------------------------------------------------

func MapIteration(pass *analysis.Pass) (interface{}, error) {
	inspect, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return nil, errors.New("analyzer is not type *inspector.Inspector")
	}

	// track lines with the expected ignore comment
	type ignorePos struct {
		file string
		line int
	}
	ignore := map[ignorePos]bool{}

	// one pass to find all comments
	inspect.Preorder([]ast.Node{(*ast.File)(nil)}, func(node ast.Node) {
		n, ok := node.(*ast.File)
		if !ok {
			panic("node was not *ast.File")
		}
		for _, c := range n.Comments {
			if strings.Contains(c.Text(), "analyze-ignore(map-iteration)") {
				p := pass.Fset.Position(c.Pos())
				ignore[ignorePos{p.Filename, p.Line + strings.Count(c.Text(), "\n")}] = true
			}
		}
	})

	inspect.Preorder([]ast.Node{(*ast.RangeStmt)(nil)}, func(node ast.Node) {
		n, ok := node.(*ast.RangeStmt)
		if !ok {
			panic("node was not *ast.RangeStmt")
		}
		// skip if this is not a range over a map
		if !strings.HasPrefix(pass.TypesInfo.TypeOf(n.X).String(), "map") {
			return
		}

		// skip if this is a test file
		p := pass.Fset.Position(n.Pos())
		if strings.HasSuffix(p.Filename, "_test.go") {
			return
		}

		// skip if the previous line contained the ignore comment
		if ignore[ignorePos{p.Filename, p.Line}] {
			return
		}

		pass.Reportf(node.Pos(), "found map iteration")
	})

	return nil, nil
}

// -------------------------------------------------------------------------------------
// Rand
// -------------------------------------------------------------------------------------

func Rand(pass *analysis.Pass) (interface{}, error) {
	inspect, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return nil, errors.New("analyzer is not type *inspector.Inspector")
	}

	allow := func(pos token.Pos) bool {
		p := pass.Fset.Position(pos)
		if strings.HasSuffix(p.Filename, "_test.go") {
			return true
		}
		if strings.HasSuffix(p.Filename, "querier_quotes.go") {
			return true
		}
		if strings.HasSuffix(p.Filename, "test_common.go") {
			return true
		}
		return false
	}

	inspect.Preorder([]ast.Node{(*ast.CallExpr)(nil)}, func(node ast.Node) {
		n, ok := node.(*ast.CallExpr)
		if !ok {
			return
		}
		if id, ok := n.Fun.(*ast.Ident); ok {
			if strings.Contains(id.Name, "Rand") && !allow(n.Pos()) {
				pass.Reportf(n.Pos(), "use of functions with \"Rand\" in name is prohibited")
			}
		}
	})

	return nil, nil
}

// -------------------------------------------------------------------------------------
// FloatComparison
// -------------------------------------------------------------------------------------

func FloatComparison(pass *analysis.Pass) (interface{}, error) {
	inspect, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return nil, errors.New("analyzer is not type *inspector.Inspector")
	}

	// track lines with the expected ignore comment
	type ignorePos struct {
		file string
		line int
	}
	ignore := map[ignorePos]bool{}

	// one pass to find all comments
	inspect.Preorder([]ast.Node{(*ast.File)(nil)}, func(node ast.Node) {
		n, ok := node.(*ast.File)
		if !ok {
			panic("node was not *ast.File")
		}
		for _, c := range n.Comments {
			if strings.Contains(c.Text(), "analyze-ignore(float-comparison)") {
				p := pass.Fset.Position(c.Pos())
				ignore[ignorePos{p.Filename, p.Line + strings.Count(c.Text(), "\n")}] = true
			}
		}
	})

	inspect.Preorder([]ast.Node{(*ast.BinaryExpr)(nil)}, func(node ast.Node) {
		n, ok := node.(*ast.BinaryExpr)
		if !ok {
			panic("node was not *ast.BinaryExpr")
		}

		// only check comparison operators
		switch n.Op {
		case token.EQL, token.NEQ, token.LSS, token.GTR, token.LEQ, token.GEQ:
			// continue with check
		default:
			return
		}

		// check if both operands are floats
		xType := pass.TypesInfo.TypeOf(n.X)
		yType := pass.TypesInfo.TypeOf(n.Y)

		if xType == nil || yType == nil {
			return
		}

		xBasic, xOk := xType.Underlying().(*types.Basic)
		yBasic, yOk := yType.Underlying().(*types.Basic)

		if !xOk || !yOk {
			return
		}

		// check if both are float types
		xIsFloat := xBasic.Info()&types.IsFloat != 0
		yIsFloat := yBasic.Info()&types.IsFloat != 0

		if !xIsFloat || !yIsFloat {
			return
		}

		// skip if this is a test file
		p := pass.Fset.Position(n.Pos())
		if strings.HasSuffix(p.Filename, "_test.go") {
			return
		}

		// skip if the previous line contained the ignore comment
		if ignore[ignorePos{p.Filename, p.Line}] {
			return
		}

		pass.Reportf(node.Pos(), "found float comparison")
	})

	return nil, nil
}

// -------------------------------------------------------------------------------------
// UnscaledConfig
// -------------------------------------------------------------------------------------

// UnscaledConfig flags reads of block-denominated settings (constants tagged as a
// duration or per-block rate in constants/units.go) that bypass GetConfigInt64:
// a GetInt64Value(constants.X) or GetMimir(ctx, constants.X.String() / "X") whose
// enclosing function never passes constants.X to ScaleConfigInt64. Such a read
// would keep its reference-block value when BLOCKTIMEMS changes the block time.
func UnscaledConfig(pass *analysis.Pass) (interface{}, error) {
	inspect, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return nil, errors.New("analyzer is not type *inspector.Inspector")
	}

	// track lines with the expected ignore comment
	type ignorePos struct {
		file string
		line int
	}
	ignore := map[ignorePos]bool{}
	inspect.Preorder([]ast.Node{(*ast.File)(nil)}, func(node ast.Node) {
		n, ok := node.(*ast.File)
		if !ok {
			panic("node was not *ast.File")
		}
		for _, c := range n.Comments {
			if strings.Contains(c.Text(), "analyze-ignore(unscaled-config)") {
				p := pass.Fset.Position(c.Pos())
				ignore[ignorePos{p.Filename, p.Line + strings.Count(c.Text(), "\n")}] = true
			}
		}
	})

	// constantName returns the name of a constants.X expression, or of X in
	// constants.X.String()
	constantName := func(e ast.Expr) string {
		if call, ok := e.(*ast.CallExpr); ok && len(call.Args) == 0 {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "String" {
				e = sel.X
			}
		}
		var id *ast.Ident
		switch x := e.(type) {
		case *ast.SelectorExpr:
			id = x.Sel
		case *ast.Ident:
			id = x
		default:
			return ""
		}
		c, ok := pass.TypesInfo.Uses[id].(*types.Const)
		if !ok || c.Pkg() == nil || c.Pkg().Name() != "constants" {
			return ""
		}
		return c.Name()
	}
	// keyName returns the mimir or constant key an argument names, if static
	keyName := func(e ast.Expr) string {
		if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if v := constant.StringVal(constant.MakeFromLiteral(lit.Value, lit.Kind, 0)); v != "" {
				return v
			}
		}
		return constantName(e)
	}

	inspect.Preorder([]ast.Node{(*ast.FuncDecl)(nil)}, func(node ast.Node) {
		fn, ok := node.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return
		}
		p := pass.Fset.Position(fn.Pos())
		if strings.HasSuffix(p.Filename, "_test.go") {
			return
		}

		type read struct {
			pos token.Pos
			key string
		}
		var reads []read
		scaled := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch {
			case sel.Sel.Name == "ScaleConfigInt64" && len(call.Args) == 3:
				if k := constantName(call.Args[1]); k != "" {
					scaled[strings.ToUpper(k)] = true
				}
			case sel.Sel.Name == "GetInt64Value" && len(call.Args) == 1:
				if k := constantName(call.Args[0]); k != "" {
					reads = append(reads, read{call.Pos(), k})
				}
			case sel.Sel.Name == "GetMimir" && len(call.Args) == 2:
				if k := keyName(call.Args[1]); k != "" {
					reads = append(reads, read{call.Pos(), k})
				}
			}
			return true
		})

		for _, r := range reads {
			unit := constants.MimirKeyUnit(r.key)
			if !unit.Scales() || scaled[strings.ToUpper(r.key)] {
				continue
			}
			rp := pass.Fset.Position(r.pos)
			if ignore[ignorePos{rp.Filename, rp.Line}] {
				continue
			}
			pass.Reportf(r.pos, "unscaled read of %s (%s): use GetConfigInt64 or ScaleConfigInt64", r.key, unit)
		}
	})

	return nil, nil
}

// -------------------------------------------------------------------------------------
// Main
// -------------------------------------------------------------------------------------

func main() {
	multichecker.Main(
		&analysis.Analyzer{
			Name:     "map_iteration",
			Doc:      "fails on uncommented map iterations",
			Requires: []*analysis.Analyzer{inspect.Analyzer},
			Run:      MapIteration,
		},
		&analysis.Analyzer{
			Name:     "rand",
			Doc:      "fails on use of functions with \"Rand\" in name",
			Requires: []*analysis.Analyzer{inspect.Analyzer},
			Run:      Rand,
		},
		&analysis.Analyzer{
			Name:     "float_comparison",
			Doc:      "fails on float comparisons",
			Requires: []*analysis.Analyzer{inspect.Analyzer},
			Run:      FloatComparison,
		},
		&analysis.Analyzer{
			Name:     "unscaled_config",
			Doc:      "fails on reads of block-denominated settings that bypass BLOCKTIMEMS scaling",
			Requires: []*analysis.Analyzer{inspect.Analyzer},
			Run:      UnscaledConfig,
		},
	)
}
