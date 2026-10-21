package policy

import (
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// optSpec 描述一个命令在操作数之前的选项，按 getopt 的习惯解析：单字母选项可以组成选项簇（-lc），
// 带参数的单字母选项在簇里后面还有字符时那些字符就是参数，否则吃下一个词；
// 带参数的长选项吃下一个词，也接受 --name=value。表里没有的选项一律视为不认识。
type optSpec struct {
	flags      string   // 不带参数的单字母选项
	withArg    string   // 带参数的单字母选项
	long       []string // 不带参数的长选项（也可以是 -ws 这类单横线的多字母选项），可写成 --name=value
	longArg    []string // 带参数的长选项
	longPrefix []string // 以这些前缀开头的长选项都不带参数，如 deno 的 --allow-
	plus       bool     // 也接受 + 开头的选项簇（shell 的 +o、+x）
	terminal   []string // 解析到这些选项就停止，后面都归它（python -m）
}

// parsedOpt 是解析出的一个选项，选项簇拆成单个选项（-lc 拆成 -l 与 -c）。
type parsedOpt struct {
	name   string
	arg    string
	hasArg bool
	word   int // 参数所在的词在 args 中的下标，用来取它的标志
}

// parseOpts 解析 args 开头的选项，返回解析出的选项、第一个操作数的下标（没有时为 len(args)）
// 与是否全部认识。-- 结束选项；单独的 - 和不以 -（或 +）开头的词是操作数。
// 遇到不认识的选项或缺参数时 ok 为 false。
func parseOpts(args []string, spec optSpec) (opts []parsedOpt, rest int, ok bool) {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			return opts, i + 1, true
		}
		if len(a) < 2 || !(a[0] == '-' || spec.plus && a[0] == '+') {
			return opts, i, true
		}
		word := i
		i++
		name, val, hasVal := strings.Cut(a, "=")
		switch {
		case slices.Contains(spec.long, name), strings.HasPrefix(a, "--") && hasAnyPrefix(name, spec.longPrefix):
			opts = append(opts, parsedOpt{name: name, arg: val, hasArg: hasVal, word: word})
		case slices.Contains(spec.longArg, name):
			if !hasVal {
				if i >= len(args) {
					return opts, i, false
				}
				val, word = args[i], i
				i++
			}
			opts = append(opts, parsedOpt{name: name, arg: val, hasArg: true, word: word})
		case strings.HasPrefix(a, "--"):
			return opts, i, false
		default:
			for j := 1; j < len(a); j++ {
				c := a[j]
				n := string([]byte{a[0], c})
				switch {
				case strings.IndexByte(spec.withArg, c) >= 0:
					switch {
					case j+1 < len(a):
						opts = append(opts, parsedOpt{name: n, arg: a[j+1:], hasArg: true, word: word})
					case i < len(args):
						opts = append(opts, parsedOpt{name: n, arg: args[i], hasArg: true, word: i})
						i++
					default:
						return opts, i, false
					}
					j = len(a)
				case strings.IndexByte(spec.flags, c) >= 0:
					opts = append(opts, parsedOpt{name: n, word: word})
				default:
					return opts, i, false
				}
			}
		}
		if slices.Contains(spec.terminal, opts[len(opts)-1].name) {
			return opts, i, true
		}
	}
	return opts, i, true
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// ---- shell ----

var posixShellOptions = optSpec{
	flags:   "abefhkmnptuvxBCEHPTilrsDcIV",
	withArg: "oO",
	long: []string{"--login", "--noprofile", "--norc", "--posix", "--restricted", "--verbose", "--version",
		"--help", "--noediting", "--debugger", "--dump-po-strings", "--dump-strings", "--pretty-print"},
	longArg: []string{"--rcfile", "--init-file"},
	plus:    true,
}

var shellOptions = map[string]optSpec{
	"sh":   posixShellOptions,
	"bash": posixShellOptions,
	"dash": posixShellOptions,
	"zsh": {
		flags:   "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghiklmnprstuvwxy",
		withArg: "o",
		long: []string{"--login", "--interactive", "--no-rcs", "--no-globalrcs", "--version", "--help",
			"--verbose", "--xtrace", "--errexit", "--nounset", "--privileged"},
		longArg: []string{"--emulate"},
		plus:    true,
	},
	"fish": {
		flags:   "ilnNPvh",
		withArg: "cCpdof",
		long: []string{"--interactive", "--login", "--no-execute", "--no-config", "--private", "--version",
			"--help", "--print-rusage-self", "--print-debug-categories"},
		longArg: []string{"--command", "--init-command", "--profile", "--profile-startup", "--debug",
			"--debug-output", "--features"},
	},
}

func isShell(name string) bool {
	_, ok := shellOptions[name]
	return ok
}

// shellRisk 给 sh、bash 这类 shell 的调用定级：
// -c（含 -lc、-xc 这类选项簇）之后第一个操作数是命令串，递归分析；fish 的 -c、--command 直接带命令串，
// -C、--init-command 的命令串也分析；带 -s、只有 -c 没有命令串，或没有操作数时从 stdin 读命令，为 unknown；
// 否则第一个操作数是脚本，按 runFileRisk 定级。有不认识的选项时为 unknown。
func shellRisk(name string, argv []string, flags []wordFlags, cwd string, ctx Context) Risk {
	call, ok := parseShellCall(name, argv)
	if !ok {
		return RiskUnknown
	}
	risk := RiskRead
	for _, c := range call.init {
		risk = maxRisk(risk, classifyShell(c, cwd, ctx))
	}
	switch {
	case call.hasCommand:
		return maxRisk(risk, classifyShell(call.command, cwd, ctx))
	case call.cmdFlag, call.stdin, call.rest >= len(argv):
		return RiskUnknown
	default:
		return maxRisk(risk, runFileRisk(argv[call.rest], flagAt(flags, call.rest), cwd, ctx))
	}
}

// shellCall 是解析后的 shell 调用。
type shellCall struct {
	init       []string // fish 的 -C、--init-command 命令串，先于主命令执行
	command    string   // -c 的命令串
	hasCommand bool
	cmdFlag    bool // 有 -c 但没有命令串
	stdin      bool // -s：从 stdin 读命令
	rest       int  // 第一个操作数在 argv 中的下标
}

// inline 返回调用里要执行的全部命令串。
func (c shellCall) inline() []string {
	out := append([]string(nil), c.init...)
	if c.hasCommand {
		out = append(out, c.command)
	}
	return out
}

// parseShellCall 按 shellOptions 解析 shell 的命令行：-c（含 -lc、-xc 这类选项簇）之后第一个操作数是命令串，
// fish 的 -c、--command 直接带命令串。有不认识的选项时 ok 为 false。
func parseShellCall(name string, argv []string) (shellCall, bool) {
	opts, rest, ok := parseOpts(argv[1:], shellOptions[name])
	if !ok {
		return shellCall{}, false
	}
	call := shellCall{rest: rest + 1} // 换成 argv 的下标
	for _, o := range opts {
		switch o.name {
		case "-c", "--command":
			if o.hasArg {
				call.command, call.hasCommand = o.arg, true
			} else {
				call.cmdFlag = true
			}
		case "-C", "--init-command":
			if o.hasArg {
				call.init = append(call.init, o.arg)
			}
		case "-s":
			call.stdin = true
		}
	}
	if call.cmdFlag && call.rest < len(argv) {
		call.command, call.hasCommand = argv[call.rest], true
	}
	return call, true
}

// ---- 解释器 ----

// interpSpec 描述一个解释器的命令行。
type interpSpec struct {
	opts     optSpec
	inline   []string // 内联或预加载代码的选项，出现即 unknown
	module   string   // 参数是要运行的模块的选项（python -m）
	fileOpts []string // 参数是要运行的文件的选项（php -f）
	subcmds  bool     // deno、bun：run、eval、exec、repl、x 是子命令，不是文件
}

var pythonSpec = interpSpec{
	opts: optSpec{
		flags:    "bBdEhiIOPqsSuvVx?",
		withArg:  "cWXm",
		long:     []string{"--help", "--version", "--help-env", "--help-xoptions", "--help-all"},
		longArg:  []string{"--check-hash-based-pycs"},
		terminal: []string{"-c", "-m"},
	},
	inline: []string{"-c"},
	module: "-m",
}

var interpreters = map[string]interpSpec{
	"python":  pythonSpec,
	"python3": pythonSpec,
	"node": {
		opts: optSpec{
			flags:   "chiv",
			withArg: "eprC",
			long: []string{"--check", "--interactive", "--version", "--help", "--inspect", "--inspect-brk",
				"--inspect-wait", "--enable-source-maps", "--no-warnings", "--no-deprecation", "--trace-warnings",
				"--trace-deprecation", "--trace-uncaught", "--throw-deprecation", "--pending-deprecation",
				"--abort-on-uncaught-exception", "--preserve-symlinks", "--expose-gc", "--test", "--watch",
				"--watch-path", "--experimental-vm-modules", "--experimental-strip-types",
				"--experimental-transform-types", "--max-old-space-size", "--stack-size", "--input-type", "--title",
				"--unhandled-rejections"},
			longArg: []string{"--eval", "--print", "--require", "--import", "--loader", "--experimental-loader",
				"--conditions"},
		},
		inline: []string{"-e", "-p", "-r", "--eval", "--print", "--require", "--import", "--loader",
			"--experimental-loader"},
	},
	"ruby": {
		opts: optSpec{
			flags:   "acdhlnpsSvwy",
			withArg: "IrCe",
			long:    []string{"--version", "--help", "--verbose", "--copyright", "--yjit", "--jit"},
		},
		inline: []string{"-e", "-r", "-S"}, // -S 从 PATH 找脚本
	},
	"perl": {
		opts: optSpec{
			flags:   "acnpstTuUvwWXlhS",
			withArg: "IMmeE",
			long:    []string{"--version", "--help"},
		},
		inline: []string{"-e", "-E", "-M", "-m"},
	},
	"php": {
		opts: optSpec{
			flags:   "ahilmnqsvwHCe",
			withArg: "drf",
			long: []string{"--version", "--help", "--info", "--modules", "--no-php-ini", "--interactive",
				"--syntax-check", "--hide-args", "--no-header", "--no-chdir"},
			longArg: []string{"--define", "--file", "--run"},
		},
		inline:   []string{"-r", "--run"},
		fileOpts: []string{"-f", "--file"},
	},
	"deno": {
		opts: optSpec{
			flags:   "AqhVr",
			withArg: "cL",
			long: []string{"--watch", "--no-check", "--check", "--reload", "--quiet", "--cached-only",
				"--no-prompt", "--no-config", "--no-lock", "--no-remote", "--no-npm", "--node-modules-dir",
				"--inspect", "--inspect-brk", "--inspect-wait", "--version", "--help", "--seed", "--v8-flags",
				"--location"},
			longArg:    []string{"--config", "--import-map", "--lock", "--cert", "--ext", "--log-level"},
			longPrefix: []string{"--allow-", "--deny-", "--unstable"},
		},
		subcmds: true,
	},
	"bun": {
		opts: optSpec{
			flags:   "hvib",
			withArg: "eprc",
			long: []string{"--watch", "--hot", "--bun", "--smol", "--silent", "--if-present", "--no-install",
				"--inspect", "--inspect-brk", "--inspect-wait", "--version", "--help", "--revision",
				"--no-clear-screen"},
			longArg: []string{"--eval", "--print", "--preload", "--config", "--filter"},
		},
		inline:  []string{"-e", "-p", "-r", "--eval", "--print", "--preload"},
		subcmds: true,
	},
}

// unknownSubcmds 是 deno、bun 里不运行文件、也无法静态判断的子命令。
var unknownSubcmds = []string{"eval", "exec", "repl", "x"}

// moduleRisks 是 python -m 已知模块的定级，表外的模块无法判断。
var moduleRisks = map[string]Risk{
	"pytest": RiskRead, "unittest": RiskRead, "build": RiskWriteInProject,
	"pip": RiskNetwork, "pipx": RiskNetwork, "http.server": RiskNetwork, "twine": RiskOutward,
}

// interpRisk 给解释器的调用定级：有不认识的选项、内联或预加载代码、没有文件时为 unknown；
// python -m 按模块定级；deno、bun 先认子命令，run 之后再解析一遍选项，运行 URL 为 network；
// 运行文件时按 runFileRisk 定级。
func interpRisk(name string, argv []string, flags []wordFlags, cwd string, ctx Context) Risk {
	spec, ok := interpreters[name]
	if !ok {
		return RiskUnknown
	}
	opts, rest, ok := parseOpts(argv[1:], spec.opts)
	if !ok {
		return RiskUnknown
	}
	rest++ // 换成 argv 的下标
	for _, o := range opts {
		switch {
		case slices.Contains(spec.inline, o.name):
			return RiskUnknown
		case o.name == spec.module:
			if r, ok := moduleRisks[o.arg]; ok {
				return r
			}
			return RiskUnknown
		case slices.Contains(spec.fileOpts, o.name):
			return runFileRisk(o.arg, flagAt(flags, o.word+1), cwd, ctx)
		}
	}
	if rest >= len(argv) {
		return RiskUnknown
	}
	if spec.subcmds {
		switch op := argv[rest]; {
		case slices.Contains(unknownSubcmds, op):
			return RiskUnknown
		case op == "run":
			runOpts, n, ok := parseOpts(argv[rest+1:], spec.opts)
			if !ok || slices.ContainsFunc(runOpts, func(o parsedOpt) bool { return slices.Contains(spec.inline, o.name) }) {
				return RiskUnknown
			}
			rest += 1 + n
			if rest >= len(argv) {
				return RiskUnknown
			}
		}
		if f := argv[rest]; strings.Contains(f, "://") || strings.HasPrefix(f, "jsr:") || strings.HasPrefix(f, "npm:") {
			return RiskNetwork
		}
	}
	return runFileRisk(argv[rest], flagAt(flags, rest), cwd, ctx)
}

// runFileRisk 给 shell 或解释器要运行的文件定级：- 与 /dev/、/proc/ 下的文件（stdin、描述符、tcp、udp）
// 无法判断；其余取项目内改动与文件路径定级的较大值。
func runFileRisk(file string, f wordFlags, cwd string, ctx Context) Risk {
	if file == "-" {
		return RiskUnknown
	}
	if !wordUnresolvable(file, f, ctx) {
		abs := resolvePath(file, cwd, ctx.HomeDir)
		if abs == "/dev" || strings.HasPrefix(abs, "/dev/") || strings.HasPrefix(abs, "/proc/") {
			return RiskUnknown
		}
	}
	return maxRisk(RiskWriteInProject, pathsRiskWords([]string{file}, []wordFlags{f}, cwd, ctx))
}

// ---- 子命令之前的全局选项 ----

// leadSpec 描述命令在子命令之前的全局选项。
type leadSpec struct {
	opts      optSpec
	dirs      []string // 参数是工作目录（或仓库、工作区目录）的选项
	taint     []string // 出现即至少 unknown 的选项：注入配置、改变执行程序的位置
	stop      []string // 出现在最前面时不剥，留给命令表匹配（--version）
	toolchain bool     // cargo +nightly：最前面 + 开头的词是工具链名
}

var jsStop = []string{"--version", "-v", "--help", "-h"}

var leadingOptions = map[string]leadSpec{
	"git": {
		opts: optSpec{
			flags:   "pP",
			withArg: "Cc",
			long:    []string{"--paginate", "--no-pager", "--bare", "--no-replace-objects", "--exec-path"},
			longArg: []string{"--git-dir", "--work-tree", "--namespace", "--config-env"},
		},
		dirs:  []string{"-C", "--git-dir", "--work-tree"},
		taint: []string{"-c", "--config-env", "--exec-path"},
		stop:  []string{"--version", "-v", "--help", "-h"},
	},
	"npm": {
		opts: optSpec{withArg: "w", long: []string{"-ws", "--workspaces"}, longArg: []string{"--workspace", "--prefix"}},
		dirs: []string{"--prefix"},
		stop: jsStop,
	},
	"pnpm": {
		opts: optSpec{flags: "r", withArg: "C", longArg: []string{"--filter", "--dir"}},
		dirs: []string{"-C", "--dir"},
		stop: jsStop,
	},
	"yarn": {
		opts: optSpec{longArg: []string{"--cwd"}},
		dirs: []string{"--cwd"},
		stop: jsStop,
	},
	"cargo": {toolchain: true, stop: []string{"--version", "-V", "--help", "-h"}},
	"docker": {
		opts: optSpec{withArg: "H", longArg: []string{"--host", "--context"}},
		stop: []string{"--version", "-v", "--help"},
	},
	"kubectl": {opts: optSpec{withArg: "n", longArg: []string{"--namespace", "--context", "--kubeconfig"}}},
	"helm":    {opts: optSpec{withArg: "n", longArg: []string{"--namespace", "--context", "--kubeconfig"}}},
}

// stripLeading 去掉子命令之前的全局选项，让 sub 规则匹配到真正的子命令。返回剥过的 argv 与标志、
// 选项带来的下限，以及目录参数的定级。遇到不认识的选项时原样返回，下限为 unknown。
func stripLeading(argv []string, flags []wordFlags, spec leadSpec, cwd string, ctx Context) ([]string, []wordFlags, Risk, Risk) {
	i := 1
	if spec.toolchain && i < len(argv) && strings.HasPrefix(argv[i], "+") {
		i++
	}
	floor, dirRisk := RiskRead, RiskRead
	if i < len(argv) && !slices.Contains(spec.stop, argv[i]) {
		opts, rest, ok := parseOpts(argv[i:], spec.opts)
		if !ok {
			return argv, flags, RiskUnknown, RiskRead
		}
		for _, o := range opts {
			if slices.Contains(spec.taint, o.name) {
				floor = RiskUnknown
			}
			if slices.Contains(spec.dirs, o.name) {
				f := flagAt(flags, i+o.word)
				dirRisk = maxRisk(dirRisk, pathsRiskWords([]string{o.arg}, []wordFlags{f}, cwd, ctx))
			}
		}
		i += rest
	}
	outArgv := append([]string{argv[0]}, argv[i:]...)
	outFlags := append([]wordFlags{flagAt(flags, 0)}, flags[min(i, len(flags)):]...)
	return outArgv, outFlags, floor, dirRisk
}

// ---- 输出到文件的选项 ----

// outputOpt 是把输出写进文件的长选项：full 是全名，min 是接受的最短缩写（getopt 风格），不接受缩写时与 full 相同。
type outputOpt struct{ full, min string }

// outputSpec 描述命令把结果写进文件的选项。fetch 为 true 的是下载工具：写到项目内不抬级，
// 写到项目外或无法解析的路径按 outward，敏感路径按 destructive；否则按普通写入的路径定级。
type outputSpec struct {
	short      string      // 参数是写入的文件或目录的短选项字符
	long       []outputOpt // 同上的长选项
	dirLong    []outputOpt // 输出目录（curl --output-dir）：目录本身定级，其余相对的输出文件也按落在它下面定级
	taint      string      // 从文件读入更多选项的短选项字符（curl -K）：输出位置无法分析
	taintLong  []outputOpt // 同上的长选项
	shortArg   string      // 其余带参数的短选项字符：选项簇里遇到就停，参数不再当选项看
	remoteName string      // 按远端文件名写进当前目录的短选项字符
	remoteLong []string    // 同上的长选项
	fetch      bool
}

var outputOptions = map[string]outputSpec{
	"curl": {
		short:      "oDc",
		long:       []outputOpt{{"--output", "--output"}, {"--dump-header", "--dump-header"}, {"--cookie-jar", "--cookie-jar"}},
		dirLong:    []outputOpt{{"--output-dir", "--output-dir"}},
		taint:      "K",
		taintLong:  []outputOpt{{"--config", "--config"}},
		shortArg:   "AbCdeEFHmPQrtTuUwxXyYz",
		remoteName: "O", remoteLong: []string{"--remote-name"},
		fetch: true,
	},
	// wget 接受 getopt 风格的长选项缩写，min 取不与别的选项混淆的最短前缀
	"wget": {
		short: "OoaP",
		long: []outputOpt{{"--output-document", "--output-d"}, {"--output-file", "--output-f"},
			{"--append-output", "--ap"}, {"--directory-prefix", "--di"}},
		shortArg: "eiBtTwQlARDIXU",
		fetch:    true,
	},
	"git": {long: []outputOpt{{"--output", "--output"}}},
}

// matchesLong 判断 --name（不含 =value）是否为 opts 里某个长选项或其可接受的缩写。
func matchesLong(opts []outputOpt, name string) bool {
	for _, o := range opts {
		if len(name) >= len(o.min) && strings.HasPrefix(o.full, name) {
			return true
		}
	}
	return false
}

// outputTargets 找出命令写入的文件及其标志：输出选项的值；按远端文件名下载时取各 URL 路径的最后一段，
// 它落在当前目录；有输出目录时，相对的输出文件再按落在目录下各算一次。taint 类选项记一个空目标，按无法解析处理。
// flags 与 argv 一一对应。
func outputTargets(argv []string, flags []wordFlags, spec outputSpec) ([]string, []wordFlags) {
	var targets, dirs []string
	var tflags, dflags []wordFlags
	add := func(t string, f wordFlags) {
		targets = append(targets, t)
		tflags = append(tflags, f)
	}
	remote := false
	for i := 1; i < len(argv); i++ {
		a, f := argv[i], flagAt(flags, i)
		switch {
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a, "=")
			if slices.Contains(spec.remoteLong, name) {
				remote = true
				continue
			}
			file, dir, taint := matchesLong(spec.long, name), matchesLong(spec.dirLong, name), matchesLong(spec.taintLong, name)
			if !file && !dir && !taint {
				continue
			}
			if !hasVal {
				if i+1 >= len(argv) {
					continue
				}
				i++
				val, f = argv[i], flagAt(flags, i)
			}
			switch {
			case taint:
				add("", wordFlags{})
			case dir:
				dirs = append(dirs, val)
				dflags = append(dflags, f)
			default:
				add(val, f)
			}
		case len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				c := a[j]
				switch {
				case strings.IndexByte(spec.remoteName, c) >= 0:
					remote = true
					continue
				case strings.IndexByte(spec.taint, c) >= 0:
					add("", wordFlags{})
					if j+1 == len(a) {
						i++ // 配置文件是下一个词
					}
				case strings.IndexByte(spec.short, c) >= 0:
					if j+1 < len(a) {
						add(a[j+1:], f)
					} else if i+1 < len(argv) {
						add(argv[i+1], flagAt(flags, i+1))
						i++
					}
				case strings.IndexByte(spec.shortArg, c) >= 0:
					if j+1 == len(a) {
						i++ // 参数是下一个词
					}
				default:
					continue
				}
				break
			}
		}
	}
	if remote {
		for _, a := range argv[1:] {
			if !strings.Contains(a, "://") {
				continue
			}
			u, err := url.Parse(a)
			if err != nil {
				add("", wordFlags{}) // 解析不了的 URL：文件名未知，按无法解析处理
				continue
			}
			if base := path.Base(u.Path); base != "/" && base != "." {
				add(base, wordFlags{})
			}
		}
	}
	// 输出目录本身定级；相对的输出文件按落在目录下再算一次，取较大值
	n := len(targets)
	for k, d := range dirs {
		add(d, dflags[k])
		for t := 0; t < n; t++ {
			if p := targets[t]; p != "" && p != "-" && !filepath.IsAbs(p) && !strings.HasPrefix(p, "~") {
				f, g := dflags[k], tflags[t]
				add(filepath.Join(d, p), wordFlags{glob: f.glob || g.glob, expand: f.expand || g.expand, tilde: f.tilde})
			}
		}
	}
	return targets, tflags
}

// outputRisk 给命令写入的文件定级，见 outputSpec。没有输出文件时返回 RiskRead。
func outputRisk(argv []string, flags []wordFlags, spec outputSpec, cwd string, ctx Context) Risk {
	targets, tflags := outputTargets(argv, flags, spec)
	risk := RiskRead
	for i, t := range targets {
		if t == "-" {
			continue // 标准输出
		}
		r := RiskUnknown
		if t != "" && !wordUnresolvable(t, tflags[i], ctx) {
			r = resolvedPathRisk(t, cwd, ctx)
		}
		if spec.fetch {
			switch r {
			case RiskDestructive:
			case RiskWriteOutside, RiskUnknown:
				r = RiskOutward
			default:
				r = RiskRead // 项目内与无害伪设备：保持下载本身的等级
			}
		}
		risk = maxRisk(risk, r)
	}
	return risk
}

// ---- 按参数抬到 unknown 的兜底 ----

// unknownIf 是命中命令表之后再看的兜底：判断为真时结果至少为 unknown，只抬不降。
var unknownIf = map[string]func(args []string) bool{"sed": sedProgramUnsafe}

// sedUnsafePatterns 是 sed 程序里会写文件或执行命令的写法：w、W 命令与 s 命令的 w、e 标志。
var sedUnsafePatterns = []string{"w ", "W ", "/w", "/e"}

// sedUnsafeRegexps 补上子串兜不住的写法：GNU 的 e 命令（可带行号或 $ 地址），
// 以及 s 命令里与其他标志连写的 e、w 标志（s/a/b/ge）。
var sedUnsafeRegexps = []*regexp.Regexp{
	regexp.MustCompile(`(^|[;{}\n])\s*(\d+|\$)?\s*e(\s|;|}|$)`),
	regexp.MustCompile(`/[gpiImM0-9]+[ew](\s|;|}|$)`),
}

// sedProgramUnsafe 判断 sed 的程序会不会写文件或执行命令。程序取每个 -e、--expression 的值，
// 没有 -e 时取第一个操作数。-f、--file 从文件读程序，不认识的选项也无法判断，都返回 true。
// 单独的 -i、-I 后面跟空串或 . 开头的词时，那个词是 BSD sed 的备份后缀。
func sedProgramUnsafe(args []string) bool {
	var progs []string
	operand, hasOperand, hasExpr := "", false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			if !hasOperand && i+1 < len(args) {
				operand, hasOperand = args[i+1], true
			}
			i = len(args)
		case a == "-" || !strings.HasPrefix(a, "-"):
			if !hasOperand {
				operand, hasOperand = a, true
			}
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a, "=")
			switch name {
			case "--expression":
				if !hasVal {
					if i++; i >= len(args) {
						return true
					}
					val = args[i]
				}
				progs, hasExpr = append(progs, val), true
			case "--in-place", "--quiet", "--silent", "--regexp-extended", "--posix", "--debug", "--sandbox",
				"--follow-symlinks", "--separate", "--unbuffered", "--null-data", "--zero-terminated",
				"--line-length", "--help", "--version":
			default:
				return true
			}
		default:
			for j := 1; j < len(a); j++ {
				switch c := a[j]; {
				case c == 'e':
					val := a[j+1:]
					if val == "" {
						if i++; i >= len(args) {
							return true
						}
						val = args[i]
					}
					progs, hasExpr = append(progs, val), true
					j = len(a)
				case c == 'i' || c == 'I':
					if j+1 == len(a) && i+1 < len(args) && (args[i+1] == "" || strings.HasPrefix(args[i+1], ".")) {
						i++
					}
					j = len(a)
				case strings.IndexByte("nErsuza", c) < 0:
					return true // 包括 -f
				}
			}
		}
	}
	if !hasExpr && hasOperand {
		progs = append(progs, operand)
	}
	for _, p := range progs {
		for _, pat := range sedUnsafePatterns {
			if strings.Contains(p, pat) {
				return true
			}
		}
		for _, re := range sedUnsafeRegexps {
			if re.MatchString(p) {
				return true
			}
		}
	}
	return false
}

// ---- 配置与环境注入 ----

// injectEnv 是能让命令执行任意代码或改变其行为的环境变量。
var injectEnv = []string{
	"GIT_EXTERNAL_DIFF", "GIT_SSH_COMMAND", "GIT_SSH", "GIT_PAGER", "GIT_EDITOR", "GIT_CONFIG_PARAMETERS",
	"GIT_DIR", "PAGER", "EDITOR", "VISUAL", "NODE_OPTIONS", "LD_PRELOAD", "LD_LIBRARY_PATH",
	"DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH", "PYTHONSTARTUP", "PYTHONPATH", "PERL5OPT", "RUBYOPT",
	"BASH_ENV", "ENV", "PROMPT_COMMAND", "CDPATH", "PATH", "IFS",
	// 同类
	"GIT_ASKPASS", "SSH_ASKPASS", "GIT_PROXY_COMMAND", "GIT_EXEC_PATH", "GIT_WORK_TREE", "PYTHONHOME",
	"PERL5LIB", "RUBYLIB", "NODE_PATH", "ZDOTDIR", "SHELLOPTS", "BASHOPTS", "PS4",
	// 改家目录与配置目录，等于换一套配置文件
	"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "GIT_SEQUENCE_EDITOR", "GIT_TEMPLATE_DIR",
	// 构建工具与远端端点
	"GOFLAGS", "CC", "CXX", "MAKEFLAGS", "DOCKER_HOST", "KUBECONFIG",
}

// injectEnvPrefixes 是同类变量的前缀：GIT_CONFIG_COUNT、GIT_CONFIG_KEY_n、GIT_CONFIG_GLOBAL 等同于 git -c 或换配置文件，
// GIT_TRACE* 把跟踪输出写到任意路径，BASH_FUNC_ 导出函数，npm_config_ 改 npm 配置。
var injectEnvPrefixes = []string{"GIT_CONFIG", "GIT_TRACE", "BASH_FUNC_", "npm_config_", "NPM_CONFIG_"}

func injectsEnv(name string) bool {
	return slices.Contains(injectEnv, name) || hasAnyPrefix(name, injectEnvPrefixes)
}

// assignsInjectedEnv 判断命令前的 VAR=value 赋值里是否有 injectEnv 中的变量。
func assignsInjectedEnv(words []string) bool {
	for _, w := range words {
		if k, _, ok := strings.Cut(w, "="); ok && injectsEnv(k) {
			return true
		}
	}
	return false
}

// exportsInjected 判断 export 的参数里是否设置了 injectEnv 中的变量。
func exportsInjected(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if k, _, _ := strings.Cut(a, "="); injectsEnv(k) {
			return true
		}
	}
	return false
}

// gitConfigInjectKeys 是能让 git 执行任意命令或改写远端地址的配置键前缀，比较时不区分大小写。
var gitConfigInjectKeys = []string{
	"alias.", "core.hookspath", "core.fsmonitor", "core.pager", "core.sshcommand", "core.editor",
	"diff.external", "credential.helper", "filter.", "merge.", "url.",
	// 同类
	"include.", "includeif.", "core.askpass", "core.gitproxy", "gpg.", "sequence.editor", "pager.",
	"credential.", "diff.",
}

// gitConfigInjectSuffixes 是同类配置键的后缀：remote.<name>.uploadpack 等会执行命令。
var gitConfigInjectSuffixes = []string{".uploadpack", ".receivepack"}

// gitConfigInjects 判断 git config 的参数里是否有 gitConfigInjectKeys 中的键。
func gitConfigInjects(args []string) bool {
	for _, a := range args {
		k := strings.ToLower(a)
		if hasAnyPrefix(k, gitConfigInjectKeys) {
			return true
		}
		for _, suf := range gitConfigInjectSuffixes {
			if strings.HasSuffix(k, suf) {
				return true
			}
		}
	}
	return false
}
