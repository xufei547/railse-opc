package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// localNames 收集一个函数里所有局部声明的名字：接收者、参数、返回值（含函数字面量的）、:=、函数内的 var/const、
// range 变量、type switch 绑定。不用已弃用的 ast.Object；宁可多收（遮蔽了就当变量），不可漏收。
func localNames(fd *ast.FuncDecl) map[string]bool {
	m := map[string]bool{}
	add := func(id *ast.Ident) {
		if id != nil {
			m[id.Name] = true
		}
	}
	ast.Inspect(fd, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Field:
			for _, id := range x.Names {
				add(id)
			}
		case *ast.ValueSpec:
			for _, id := range x.Names {
				add(id)
			}
		case *ast.AssignStmt:
			if x.Tok == token.DEFINE {
				for _, e := range x.Lhs {
					if id, ok := e.(*ast.Ident); ok {
						add(id)
					}
				}
			}
		case *ast.RangeStmt:
			if x.Tok == token.DEFINE {
				for _, e := range []ast.Expr{x.Key, x.Value} {
					if id, ok := e.(*ast.Ident); ok {
						add(id)
					}
				}
			}
		}
		return true
	})
	return m
}

// scanKeys 扫 root 下所有非测试 .go 文件里的 i18n.T(…) 调用，返回用到的键（键 → 出现位置）与违规清单。
// 参数合规的只有两种：字符串字面量；或标识符，且它指向同一个包（同目录非测试文件）里
// `const 名 = <单个字符串字面量>` 的包级常量（键取那个字面量的值）。其余（变量、拼接、函数调用、
// 指向拼接常量的标识符、参数个数不对、被函数内同名局部声明遮蔽的标识符）一律算违规，因为完整性没法保证。
// 遍历时跳过 root 以下名为 i18n、testdata、node_modules 的子目录。
func scanKeys(root string) (used map[string][]string, problems []string, err error) {
	used = map[string][]string{}
	fset := token.NewFileSet()
	// 先按目录分组解析：同包的常量要跨文件查。
	pkgs := map[string][]*ast.File{}
	var dirs []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (d.Name() == "i18n" || d.Name() == "testdata" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		dir := filepath.Dir(path)
		if _, ok := pkgs[dir]; !ok {
			dirs = append(dirs, dir)
		}
		pkgs[dir] = append(pkgs[dir], f)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		files := pkgs[dir]
		consts := map[string]string{} // 包级 const 名 → 字面量值
		for _, f := range files {
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, spec := range gd.Specs {
					vs := spec.(*ast.ValueSpec)
					if len(vs.Names) != 1 || len(vs.Values) != 1 {
						continue
					}
					lit, ok := vs.Values[0].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					if v, uerr := strconv.Unquote(lit.Value); uerr == nil {
						consts[vs.Names[0].Name] = v
					}
				}
			}
		}
		for _, f := range files {
			for _, decl := range f.Decls {
				var locals map[string]bool // 所在函数里出现过的局部声明名（保守：整个顶层函数一视同仁）
				if fd, ok := decl.(*ast.FuncDecl); ok {
					locals = localNames(fd)
				}
				ast.Inspect(decl, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "T" {
						return true
					}
					if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "i18n" {
						return true
					}
					pos := fset.Position(call.Pos()).String()
					if len(call.Args) != 1 {
						problems = append(problems, pos+": i18n.T 要恰好一个参数")
						return true
					}
					switch arg := call.Args[0].(type) {
					case *ast.BasicLit:
						key, uerr := strconv.Unquote(arg.Value)
						if arg.Kind != token.STRING || uerr != nil {
							problems = append(problems, pos+": i18n.T 的参数必须是字符串字面量")
							return true
						}
						used[key] = append(used[key], pos)
					case *ast.Ident:
						if locals[arg.Name] {
							problems = append(problems, pos+": i18n.T("+arg.Name+") 被函数内的同名局部声明遮蔽，不能当包级常量认（变量请用 i18n.Lookup）")
							return true
						}
						key, ok := consts[arg.Name]
						if !ok {
							problems = append(problems, pos+": i18n.T("+arg.Name+") 不是同包里“单个字符串字面量”的包级常量（变量请用 i18n.Lookup，并手工把句子列进 en.go）")
							return true
						}
						used[key] = append(used[key], pos)
					default:
						problems = append(problems, pos+": i18n.T 的参数必须是字符串字面量或指向字面量的包级常量（变量请用 i18n.Lookup，并手工把句子列进 en.go）")
					}
					return true
				})
			}
		}
	}
	sort.Strings(problems)
	return used, problems, nil
}

// 扫描器本身：字面量与“常量=单个字面量”被接受（含跨文件），变量、拼接常量、函数调用等被报出。
func TestScanKeys(t *testing.T) {
	used, problems, err := scanKeys(filepath.Join("testdata", "scan", "ok"))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("合规样本不该有违规: %v", problems)
	}
	for _, want := range []string{"字面量", "反引号字面量", "用法: x", "一个常量"} {
		if _, ok := used[want]; !ok {
			t.Errorf("没扫到键 %q，实际 %v", want, used)
		}
	}
	if _, ok := used["不是 i18n 的 T"]; ok {
		t.Errorf("other.T 不该算")
	}
	if got := len(used["用法: x"]); got != 2 {
		t.Errorf("常量 usage 在两个文件里各用一次，实际 %d 处", got)
	}

	_, problems, err = scanKeys(filepath.Join("testdata", "scan", "bad"))
	if err != nil {
		t.Fatal(err)
	}
	// 逐条：行号 + 原因关键词（行号对应 testdata/scan/bad/bad.go）。
	want := []struct {
		line int
		why  string
		desc string
	}{
		{12, "不是同包", "包级变量"},
		{13, "不是同包", "拼接常量"},
		{14, "必须是字符串字面量或指向", "拼接表达式"},
		{15, "必须是字符串字面量或指向", "函数调用"},
		{16, "恰好一个参数", "两个参数"},
		{17, "不是同包", "未知标识符"},
		{22, "遮蔽", "局部变量遮蔽包级常量"},
		{26, "遮蔽", "函数参数遮蔽包级常量"},
		{30, "遮蔽", "闭包参数遮蔽包级常量"},
	}
	if len(problems) != len(want) {
		t.Fatalf("违规应有 %d 条，实际 %d：\n%s", len(want), len(problems), strings.Join(problems, "\n"))
	}
	for _, w := range want {
		prefix := "bad.go:" + strconv.Itoa(w.line) + ":"
		found := false
		for _, p := range problems {
			if strings.Contains(p, prefix) {
				found = true
				if !strings.Contains(p, w.why) {
					t.Errorf("%s（第 %d 行）原因应含 %q，实际：%s", w.desc, w.line, w.why, p)
				}
			}
		}
		if !found {
			t.Errorf("%s（第 %d 行）没被报出：\n%s", w.desc, w.line, strings.Join(problems, "\n"))
		}
	}
}

// 真实仓库：cmd/ 与 internal/ 下每个 i18n.T 的键都得在英文表里；en 里没人用的键只打印。
func TestCatalogComplete(t *testing.T) {
	root := filepath.Join("..", "..")
	used := map[string][]string{}
	for _, top := range []string{"cmd", "internal"} {
		u, problems, err := scanKeys(filepath.Join(root, top))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range problems {
			t.Error(p)
		}
		for k, w := range u {
			used[k] = append(used[k], w...)
		}
	}
	if len(used) == 0 {
		t.Fatal("没扫到任何 i18n.T 调用，路径不对？")
	}
	var missing []string
	for key, where := range used {
		if _, ok := en[key]; !ok {
			missing = append(missing, strconv.Quote(key)+"  // "+where[0])
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("英文表缺 %d 条，请补进 internal/i18n/en.go：\n%s", len(missing), strings.Join(missing, "\n"))
	}
	for key := range en {
		if _, ok := used[key]; !ok {
			t.Logf("英文表里没人用的键（Lookup 用的固定句子除外）：%q", key)
		}
	}
}
