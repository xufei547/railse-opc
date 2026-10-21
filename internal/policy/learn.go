package policy

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ErrCannotLearn 表示请求的风险等级不能生成 learned 放行规则。只有 read、write_in_project 与 network 可以：
// unknown 与 write_outside 涉及项目外的路径或无法解析的参数，学来的前缀表达不了；
// outward 与 destructive 只能由用户手写规则放行。
var ErrCannotLearn = errors.New("只有只读、项目内改动和联网操作能记住为自动放行规则，其余请手写规则")

// ProposeRule 从一条用户已批准的请求生成 learned 规则草案：action 为 allow，risk 固定为请求当前的风险等级，
// 范围为当前项目。没有项目根时文件与命令规则没有可限定的项目范围，学出来的 global 规则会放行任何项目里的同类请求，
// 所以 shell 与 file_write 报错，mcp 与 network 学成 global。
// 风险等级不是 read、write_in_project 或 network 时返回 ErrCannotLearn；outward 与 destructive 最先判断，
// 命令同时含通配或命令替换时也返回 ErrCannotLearn。
// shell 只接受单段命令，并且不能含无法静态分析的写法、引号外的通配或 $ 展开、指向文件的重定向：
// 引擎的放行检查（segmentAllowable）不会命中这样的命令，学出来的规则是死的。前缀见 learnableShell，
// 选项之后没有操作数的命令（make、git -C sub、make -j4）只在 read 时能学。
// file_write 取所在目录，文件就在项目根下时只取它的精确路径；mcp 取服务加工具；network 取主机名。
// 路径按字面转义通配元字符。草案在返回前经过 Validate。
func ProposeRule(p Payload, ctx Context, requestID string) (Rule, error) {
	risk := Classify(p, ctx)
	if risk >= RiskOutward {
		return Rule{}, ErrCannotLearn
	}
	if ctx.ProjectRoot == "" && (p.Type == PayloadShell || p.Type == PayloadFileWrite) {
		return Rule{}, errors.New("没有项目根目录，不能生成文件或命令规则")
	}
	r := Rule{
		ID:          "learned-" + shortID(requestID),
		Source:      SourceLearned,
		Action:      ActionAllow,
		CreatedFrom: requestID,
		Match:       Match{Payload: p.Type},
	}
	if ctx.ProjectRoot != "" {
		r.Scope = Scope{Project: escapeGlob(ctx.ProjectRoot)}
	}
	cwd := p.Cwd
	if cwd == "" {
		cwd = ctx.ProjectRoot
	}
	hasOperand := true
	switch p.Type {
	case PayloadShell:
		words, re, operand, err := learnableShell(p.Command, cwd, ctx)
		if err != nil {
			return Rule{}, err
		}
		hasOperand = operand
		r.Match.CommandPrefix = strings.Join(words, " ")
		r.Match.CommandRegex = re
	case PayloadFileWrite:
		abs := resolvePath(p.Path, cwd, ctx.HomeDir)
		dir := filepath.Dir(abs)
		if rel, ok := relWithin(dir, filepath.Clean(ctx.ProjectRoot)); ok && rel != "." {
			r.Match.PathGlob = filepath.Join(escapeGlob(dir), "**")
		} else {
			// 项目根下的文件（以及根目录本身）只取精确路径：取 <根>/** 等于放行整个项目的写入
			r.Match.PathGlob = escapeGlob(abs)
		}
	case PayloadMCP:
		r.Match.MCPServer = p.Server
		r.Match.MCPTool = p.Tool
	case PayloadNetwork:
		// 与引擎的 hostMatches 用同一个解析
		host, ok := parseHost(p.URL)
		if !ok || host == "" {
			return Rule{}, errors.New("URL 无效，不能生成规则")
		}
		r.Match.Domain = normalizeHost(host)
	default:
		return Rule{}, fmt.Errorf("未知载荷类型 %q", p.Type)
	}
	// 命令前设置能注入代码的环境变量时 Classify 至少为 unknown，在这里拒绝
	switch risk {
	case RiskRead, RiskWriteInProject, RiskNetwork:
	default:
		return Rule{}, ErrCannotLearn
	}
	if !hasOperand && risk != RiskRead {
		return Rule{}, fmt.Errorf("命令 %q 在选项之后没有操作数，记住它会放行这个命令的一切同级操作，只读命令才能这样记住，请手写规则", r.Match.CommandPrefix)
	}
	r.Match.Risk = &risk
	if err := (RuleSet{Version: RuleSetVersion, Rules: []Rule{r}}).Validate(); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// learnableShell 检查单段 shell 命令能否被前缀规则放行，能则返回剥掉包装后学来的前缀词，
// 以及命令在选项之后有没有操作数（子命令、脚本或参数）。
// 取整条命令的（见 learnsWholeCommand）还返回锚定这一段原文的正则：前缀按词边界匹配，整条命令之后再加
// 操作数仍会命中，正则把它限定为原命令。引擎对 allow 规则按段的原文（seg.text）匹配正则，
// 所以正则取原文，不取剥过包装的词。其余命令的前缀见 learnedWords。
func learnableShell(command, cwd string, ctx Context) (words []string, re string, hasOperand bool, err error) {
	segs := splitSegments(command)
	if len(segs) > 1 {
		return nil, "", false, errors.New("复合命令不能生成规则，请手写")
	}
	if len(segs) == 0 {
		return nil, "", false, errors.New("空命令不能生成规则")
	}
	seg := parseSegment(segs[0])
	if seg.subshell {
		return nil, "", false, errors.New("命令含命令替换、子 shell、heredoc 等无法静态分析的写法，不能生成规则")
	}
	if slices.ContainsFunc(seg.argvFlags, func(f wordFlags) bool { return f.glob || f.expand }) {
		return nil, "", false, errors.New("命令含引号外的通配或 $ 展开，不能生成规则")
	}
	if !harmlessTargets(seg.redirects, seg.redirectFlags) || !harmlessTargets(seg.inputs, seg.inputFlags) {
		return nil, "", false, errors.New("命令含指向文件的重定向，不能生成规则")
	}
	argv, flags := commandWords(seg)
	if len(argv) == 0 {
		return nil, "", false, errors.New("空命令不能生成规则")
	}
	if learnsWholeCommand(filepath.Base(argv[0])) {
		return argv, "^" + regexp.QuoteMeta(seg.text) + "$", firstNonOption(argv) < len(argv), nil
	}
	words, hasOperand = learnedWords(argv, flags, cwd, ctx)
	return words, "", hasOperand, nil
}

// pathOperandCommands 的参数大多是要改动的路径，是命令表里 paths: true 的行之外的兜底列表：
// 否则批准 rm -f build.log 会学成 rm -f，放行项目内任意文件的删除。
var pathOperandCommands = []string{
	"mkdir", "touch", "cp", "mv", "ln", "rm", "rmdir", "chmod", "tee", "patch", "unzip", "tar", "sed", "sort", "uniq",
}

// remoteCommands 联网或在远端执行命令：地址、远端命令与上传内容都在参数里，前两个词限定不住。
var remoteCommands = []string{
	"curl", "wget", "http", "xh", "ssh", "scp", "rsync", "nc", "telnet",
	"ping", "dig", "nslookup", "host", "traceroute",
}

// dispatchCommands 是按子命令分发的云、集群、包管理与部署 CLI：真正的动作常在第三个词（aws ec2 terminate-instances、
// gh auth token、kubectl get secrets），前两个词一样的命令风险等级相同、效果却天差地别。
var dispatchCommands = []string{
	"aws", "gcloud", "az", "gh", "kubectl", "helm", "docker", "brew", "cargo", "terraform", "pulumi",
	"flyctl", "fly", "vercel", "wrangler", "firebase", "heroku", "railway",
}

// wholeCommandNames 是学来的规则取整条命令并用锚定正则限定的命令：命令表里做路径分析（paths: true）的命令、
// pathOperandCommands、remoteCommands 与 dispatchCommands。
var wholeCommandNames = func() map[string]bool {
	m := map[string]bool{}
	for _, row := range commandTable {
		if row.paths {
			m[row.name] = true
		}
	}
	for _, n := range slices.Concat(pathOperandCommands, remoteCommands, dispatchCommands) {
		m[n] = true
	}
	return m
}()

// learnsWholeCommand 判断学来的规则是否要取整条命令：wholeCommandNames 里的命令，以及任何 shell 调用
// （带不带 -c 都一样，命令串或脚本参数就是要执行的内容）。
func learnsWholeCommand(name string) bool {
	return wholeCommandNames[name] || isShell(name)
}

// learnedWords 决定前缀取剥掉包装后的哪几个词，并报告选项之后有没有操作数：
// 有全局选项表的命令（git、npm 等）取到子命令为止，含全局选项及其参数（git -C sub status）；
// 解释器按选项表跳过选项及其参数，取到要运行的脚本为止（python3 -X dev app.py）；
// 其余取前两个词，第二个词是选项时一直取到选项之后的第一个非选项词（ls -la src）。
func learnedWords(argv []string, flags []wordFlags, cwd string, ctx Context) (words []string, hasOperand bool) {
	name := filepath.Base(argv[0])
	if spec, ok := leadingOptions[name]; ok {
		rest, _, _, _ := stripLeading(argv, flags, spec, cwd, ctx)
		return argv[:min(len(argv)-len(rest)+2, len(argv))], len(rest) > 1
	}
	if spec, ok := interpreters[name]; ok {
		if n, operand, ok := interpWords(argv, spec); ok {
			return argv[:n], operand
		}
	}
	n := firstNonOption(argv)
	return argv[:min(n+1, len(argv))], n < len(argv)
}

// firstNonOption 返回 argv[0] 之后第一个不以 - 开头的词的下标，没有时为 len(argv)。
func firstNonOption(argv []string) int {
	n := 1
	for n < len(argv) && strings.HasPrefix(argv[n], "-") {
		n++
	}
	return n
}

// interpWords 返回解释器调用里到要运行的脚本为止的词数，以及有没有脚本：-m 这类终止选项取到它的参数为止
// （python3 -m pytest，模块就是操作数），deno、bun 的 run 之后再跳一遍选项。有不认识的选项时 ok 为 false。
func interpWords(argv []string, spec interpSpec) (n int, hasOperand, ok bool) {
	opts, rest, ok := parseOpts(argv[1:], spec.opts)
	if !ok {
		return 0, false, false
	}
	n = 1 + rest // 第一个操作数在 argv 中的下标
	if len(opts) > 0 && slices.Contains(spec.opts.terminal, opts[len(opts)-1].name) {
		return n, true, true
	}
	if spec.subcmds && n < len(argv) && argv[n] == "run" {
		_, more, ok := parseOpts(argv[n+1:], spec.opts)
		if !ok {
			return 0, false, false
		}
		n += 1 + more
	}
	return min(n+1, len(argv)), n < len(argv), true
}

// escapeGlob 给字面路径里的 doublestar 元字符加反斜杠，让它在规则里只匹配自己。
func escapeGlob(s string) string {
	var b strings.Builder
	for _, c := range s {
		if strings.ContainsRune(`\*?[]{}`, c) {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
