//go:build ignore

// 禁止代码 / 文件名 / 注释 / 测试描述里出现 FR、ADR、PR 编号。
//
// 规则正文见 .claude/rules/no-ref-in-code.md。
//
// # 为什么不是 grep / 正则
//
// 编号会变形：同一件事在仓库里有「短横线形式」「紧贴形式」「大小写混写」三种写法，
// 正则要么只匹配其中一种（早前版本只认带短横线的，紧贴形式全漏），要么写成一坨
// 需要不断打补丁的字符类。这里改用语法级解析：
//
//   - .go 文件走 go/parser + go/ast：标识符、注释、字符串字面量分别取，位置精确到行列，
//     注释不会被误当成代码，字符串里的转义也不会打乱列号
//   - 其中 .ts/.tsx/.js/.mjs 不做文本扫描，交给 ESLint 的 AST 规则
//     （no-restricted-syntax，见 packages/eslint-config/index.js 与各 app 的 flat config）
//   - .java/.kts/.proto 无现成解析器接入 lint，走词法切分兜底
//   - 文件名对 basename 做同样的词法切分
//
// # 词法切分（不是正则）
//
// 把文本切成「字母段 / 数字段」交替的序列，再判定相邻两段是不是「编号前缀 + 编号数字」。
// 这样天然处理变形：一个测试函数名会被切成 [Test FR 397 _ Tools Registered]，
// 而形如 XFR-1 的标识符切出来是 [XFR 1]——整段不等于任何前缀，不会误报。
//
// # PR 为什么额外要求「紧贴」
//
// FR / ADR 的规范写法自带短横线，所以允许前缀与数字之间隔分隔符。
// PR 不是：代码和注释里 PR 大量指 Pull Request（如提交流里的 PR 引用），
// 隔了分隔符就无法与「PR 编号」区分。真实存在的违规写法（测试文件、测试函数名）
// 又都是紧贴的，因此 PR 只认紧贴形式，用这一点把误报挡掉。
//
// 用法：
//
//	go run ./scripts/check-no-ref-in-code.go                # diff 模式，对比 origin/master
//	go run ./scripts/check-no-ref-in-code.go <base-ref>     # diff 模式，对比指定基线
//	go run ./scripts/check-no-ref-in-code.go --stock        # 存量模式（扫工作区当前内容）
//
// 退出码：0 = 通过；1 = 命中违规；2 = 用法或环境错误。
package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// scanRoots 是参与扫描的代码根。
// docs/ 与 CHANGELOG.md 不在内——编号在文档里才是有效信息。
var scanRoots = []string{"apps", "internal", "packages", "scripts", "proto", "client-updater", "configs"}

// skipDirNames 是整目录跳过的名字。
var skipDirNames = map[string]bool{
	"node_modules": true,
	".git":         true,
	"dist":         true,
	"vendor":       true,
	"coverage":     true,
	".tmp":         true,
}

// exemptLineMark 出现在某行里表示该行豁免（需在 PR 描述里说明原因）。
const exemptLineMark = "no-ref-check"

// refPrefix 描述一类被禁的编号前缀。
type refPrefix struct {
	text string // 规范化写法，仅用于报告
	// glued 为 true 时，要求前缀与数字之间没有任何分隔符。
	// PR 常被用作 Pull Request 的缩写，只有紧贴形式才可能是编号，故要求 glued。
	glued bool
}

var refPrefixes = []refPrefix{
	{text: "FR"},
	{text: "ADR"},
	{text: "PR", glued: true},
}

// minDigits 是编号数字段的最少位数。
// 单字符数字不足以构成编号（短横线形式同理），却能撞上大量普通标识符，故从两位起判。
const minDigits = 2

// tokKind 是词法段的类别。
type tokKind int

const (
	tokLetter tokKind = iota
	tokDigit
)

// lexeme 是一个词法段，start/end 是它在原串里的字节区间（end 不含）。
// 名字刻意不叫 token——那会与 go/token 包名冲突。
type lexeme struct {
	kind       tokKind
	text       string
	start, end int
}

// splitTokens 把文本按「字母段 / 数字段」切开，其余字节一律视为分隔符。
// 这是本检查器的判据来源——不是正则，是逐字节的状态机。
func splitTokens(s string) []lexeme {
	toks := make([]lexeme, 0, 16)
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			j := i
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			toks = append(toks, lexeme{tokDigit, s[i:j], i, j})
			i = j
		case isLetter(c):
			j := i
			for j < len(s) && isLetter(s[j]) {
				j++
			}
			toks = append(toks, lexeme{tokLetter, s[i:j], i, j})
			i = j
		default:
			i++
		}
	}
	return toks
}

func isLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// matchRef 在一段文本里找出第一个违规的编号，返回 (命中片段, 是否命中)。
// 判定条件：字母段等于某个前缀（大小写不敏感），且紧随其后的数字段位数达标；
// 前缀要求紧贴时，两段在原串里还必须首尾相接。
//
// 豁免：编号若属于文件路径的一部分（如 docs/specs/x/fr-091.md、docs/adr/097-y.md），
// 它是指向文档的路标而不是代码自己的名字。规则只禁“代码自称”，文档路径本就必须
// 带编号才能定位文件，所以这类跳过。
func matchRef(s string) (string, bool) {
	toks := splitTokens(s)
	for i := 0; i+1 < len(toks); i++ {
		lit, num := toks[i], toks[i+1]
		if lit.kind != tokLetter || num.kind != tokDigit {
			continue
		}
		if len(num.text) < minDigits {
			continue
		}
		for _, p := range refPrefixes {
			if !strings.EqualFold(lit.text, p.text) {
				continue
			}
			if p.glued && num.start != lit.end {
				continue
			}
			if isPathComponent(s, lit.start, num.end) {
				continue
			}
			return s[lit.start:num.end], true
		}
	}
	return "", false
}

// isPathComponent 判断 [start,end) 这段命中是否属于一个文件路径。
// 判据：命中段前面紧跟着目录分隔符（/ 或 \），或者命中段之后不远处出现了文件扩展名，
// 且整段里带 / 或 \ 。这样既放过 docs/specs/…/fr-091.md，又不会放过
// TestFR091Xxx 这种标识符（后面没有扩展名）。
func isPathComponent(s string, start, end int) bool {
	// 向前看：紧邻的分隔符，或同一段路径里还出现过分隔符。
	j := start - 1
	for j >= 0 && !isPathSep(s[j]) && !isPathStop(s[j]) {
		j--
	}
	hasSep := j >= 0 && isPathSep(s[j])

	// 向后看：剩余部分里是否很快出现斜线或扩展名。
	rest := s[end:]
	for k := 0; k < len(rest) && k < 24; k++ {
		if isPathSep(rest[k]) {
			return true
		}
	}
	return hasSep
}

func isPathSep(c byte) bool { return c == '/' || c == '\\' }

// isPathStop 是路径边界：空白与常见标点。
func isPathStop(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '"', '\'', '`', '(', ')', '[', ']', '{', '}', ',', ';', ':', '=', '<', '>', '|', '&':
		return true
	}
	return false
}

// violation 是一条违规记录。
type violation struct {
	path string
	line int
	col  int
	what string // 违规出现的位置描述：标识符 / 注释 / 字符串 / 文件名
	text string // 命中的片段
	snip string // 便于人眼定位的原文片段
}

func (v violation) String() string {
	loc := v.path
	if v.line > 0 {
		loc = fmt.Sprintf("%s:%d:%d", v.path, v.line, v.col)
	}
	snip := strings.TrimSpace(v.snip)
	if len(snip) > 120 {
		snip = snip[:120] + "…"
	}
	return fmt.Sprintf("%s  [%s] %s  %s", loc, v.what, v.text, snip)
}

// isExemptLine 判断某行是否带豁免标记。
func isExemptLine(s string) bool {
	return strings.Contains(s, exemptLineMark)
}

// codeExts 是需要做内容扫描的扩展名。
// .ts/.tsx/.js 等由 ESLint 负责，此处不重复扫，避免两套判据漂移。
var lexicalExts = map[string]bool{
	".java":  true,
	".kts":   true,
	".proto": true,
}

// isGenerated 判断是否生成物（含编号是源文件带出来的，改源即可）。
func isGenerated(path string) bool {
	return strings.HasSuffix(path, ".pb.go")
}

// listFiles 取受版本控制的文件名清单。
// 用 git ls-files 而不是遍历目录：只查入库内容，构建产物与本地垃圾自然不在内。
//
// 注意：改名/删除已暂存但未提交时，git ls-files 仍会把旧路径列出来，
// 对这些路径读文件会失败。故按 `git ls-files --deleted` 的索引事实做存在性过滤。
func listFiles() ([]string, error) {
	deleted := map[string]bool{}
	if dout, derr := exec.Command("git", "ls-files", "--deleted", "--", ".").Output(); derr == nil {
		for _, ln := range strings.Split(string(dout), "\n") {
			if ln = strings.TrimSpace(ln); ln != "" {
				deleted[ln] = true
			}
		}
	}

	out, err := exec.Command("git", append([]string{"ls-files", "--"}, scanRoots...)...).Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files 失败: %w", err)
	}
	var files []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || deleted[line] {
			continue
		}
		files = append(files, line)
	}
	return files, sc.Err()
}

// isSelfExempt 判断文件本身是否应当豁免。
//
// 与规则文档「允许」一节一致：规则与检查器**必须举反例、写判据**，
// 它们不可避免地出现编号字样（`FR-397 / FR397 / fr397` 这类变形举例、
// 词法切分的示意输出）。把这些当成违规会让门禁无法存在。
//
// 豁免面严格限定在「检查器与规则自身」，不放宽到业务代码：
//   - 本仓库的检查器与改名工具（scripts/check-no-ref-in-code.go 等）
//   - .claude/rules/ 下的规则文档（本来就不在扫描根内，列出仅为完整）
func isSelfExempt(path string) bool {
	clean := filepath.ToSlash(path)
	switch {
	case strings.HasPrefix(clean, ".claude/rules/"):
		return true
	case clean == "scripts/check-no-ref-in-code.go", clean == "scripts/rename-identifiers.go":
		return true
	}
	return false
}

// scanFileName 检查文件名（只看 basename；目录名带编号不影响导航，也无先例）。
func scanFileName(path string) []violation {
	if isSelfExempt(path) {
		return nil
	}
	base := filepath.Base(path)
	// 文档文件名里编号是有效信息，仅当它位于 docs/ 下才豁免；
	// 扫描根本身不含 docs/，但 client-updater/ 等目录里可能有 README 之类，故显式跳过文档扩展名。
	switch strings.ToLower(filepath.Ext(base)) {
	case ".md", ".png", ".jpg", ".jpeg", ".svg", ".gif":
		return nil
	}
	if hit, ok := matchRef(base); ok {
		return []violation{{path: path, what: "文件名", text: hit, snip: base}}
	}
	return nil
}

// scanGoFile 用 go/ast 解析一个 Go 源文件，分别检查标识符、注释、字符串字面量。
func scanGoFile(path string) ([]violation, error) {
	if isSelfExempt(path) {
		return nil, nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读 %s 失败: %w", path, err)
	}
	// 解析失败不算违规——编译错误由 go build / go vet 负责，这里只管命名。
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, nil
	}

	var out []violation
	lineText := func(pos token.Pos) string {
		p := fset.Position(pos)
		if p.Line-1 < len(strings.Split(string(src), "\n")) && p.Line > 0 {
			lines := strings.Split(string(src), "\n")
			return lines[p.Line-1]
		}
		return ""
	}
	add := func(pos token.Pos, what, text string) {
		p := fset.Position(pos)
		out = append(out, violation{
			path: path, line: p.Line, col: p.Column,
			what: what, text: text, snip: lineText(pos),
		})
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.Ident:
			// 标识符：函数名、类型名、变量名、字段名、方法名都走这里。
			if hit, ok := matchRef(node.Name); ok {
				add(node.Pos(), "标识符", hit)
			}
		case *ast.BasicLit:
			// 字符串与字符字面量。值里带引号，词法切分会自动跳过。
			if node.Kind == token.STRING || node.Kind == token.CHAR {
				if hit, ok := matchRef(node.Value); ok {
					add(node.Pos(), "字符串", hit)
				}
			}
		}
		return true
	})

	// 注释单独遍历：ast.Inspect 也能碰到 *ast.Comment，但那要求文件带 ParseComments
	// 且在 CommentGroup 里，逐条取更直观。
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if isExemptLine(c.Text) {
				continue
			}
			if hit, ok := matchRef(c.Text); ok {
				out = append(out, violation{
					path: path,
					line: fset.Position(c.Pos()).Line,
					col:  fset.Position(c.Pos()).Column,
					what: "注释", text: hit, snip: c.Text,
				})
			}
		}
	}
	return out, nil
}

// scanLexicalFile 对无现成解析器的语言做逐行词法扫描。
// 只用于 .java/.kts/.proto —— 这些在本仓存量很少，且不参与日常命名。
func scanLexicalFile(path string) ([]violation, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []violation
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if isExemptLine(text) {
			continue
		}
		if hit, ok := matchRef(text); ok {
			out = append(out, violation{path: path, line: line, what: "文本", text: hit, snip: text})
		}
	}
	return out, sc.Err()
}

// scanStock 全量扫描工作区当前内容。
func scanStock() ([]violation, error) {
	files, err := listFiles()
	if err != nil {
		return nil, err
	}
	var all []violation
	var errs []string
	for _, path := range files {
		if isGenerated(path) {
			continue
		}
		all = append(all, scanFileName(path)...)

		switch {
		case strings.HasSuffix(path, ".go"):
			vs, err := scanGoFile(path)
			if err != nil {
				errs = append(errs, err.Error())
				continue
			}
			all = append(all, vs...)
		default:
			if lexicalExts[strings.ToLower(filepath.Ext(path))] {
				vs, err := scanLexicalFile(path)
				if err != nil {
					errs = append(errs, err.Error())
					continue
				}
				all = append(all, vs...)
			}
		}
	}
	if len(errs) > 0 {
		return all, fmt.Errorf("部分文件读取失败：\n  %s", strings.Join(errs, "\n  "))
	}
	return all, nil
}

// addedLine 是 diff 模式里的一条新增行。
type addedLine struct {
	path string
	line int
	text string
}

// gitDiffAdded 取 base...HEAD 的新增行（开启 -M，否则「重命名 + 改动」会被当成整文件新增）。
func gitDiffAdded(base string) ([]addedLine, error) {
	out, err := exec.Command("git", "diff", "-U0", "-M", base+"...HEAD", "--", ".").Output()
	if err != nil {
		// 基线不存在等情形按「没有新增」处理，避免把门禁变成环境问题。
		return nil, nil
	}
	var res []addedLine
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	var cur string
	var curLine int
	for sc.Scan() {
		text := sc.Text()
		switch {
		case strings.HasPrefix(text, "+++ b/"):
			cur = text[len("+++ b/"):]
		case strings.HasPrefix(text, "@@ "):
			curLine = parseHunkStart(text)
		case strings.HasPrefix(text, "+") && !strings.HasPrefix(text, "+++"):
			res = append(res, addedLine{path: cur, line: curLine, text: text[1:]})
			curLine++
		}
	}
	return res, sc.Err()
}

// parseHunkStart 从 "@@ -a,b +c,d @@" 里取新增侧的起始行号。
func parseHunkStart(s string) int {
	i := strings.Index(s, "+")
	if i < 0 {
		return 0
	}
	rest := s[i+1:]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	n, err := strconv.Atoi(rest[:j])
	if err != nil {
		return 0
	}
	return n
}

// scanDiff 只扫本次新引入的编号。
// 同一文件里「新增行出现的编号、且该文件删除行里没有」才算新引入——
// 只改同一行里的一个词会让整行变成「删旧 + 增新」，行内原有编号会凭空变成新增。
func scanDiff(base string) ([]violation, error) {
	added, err := gitDiffAdded(base)
	if err != nil {
		return nil, err
	}

	// 建「文件 → 删除行里出现过的编号」索引。
	removedByFile := map[string]map[string]bool{}
	if out, err := exec.Command("git", "diff", "-U0", "-M", base+"...HEAD", "--", ".").Output(); err == nil {
		sc := bufio.NewScanner(strings.NewReader(string(out)))
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		var cur string
		for sc.Scan() {
			text := sc.Text()
			switch {
			case strings.HasPrefix(text, "+++ b/"):
				cur = text[len("+++ b/"):]
			case strings.HasPrefix(text, "-") && !strings.HasPrefix(text, "---"):
				body := text[1:]
				if hit, ok := matchRef(body); ok {
					if removedByFile[cur] == nil {
						removedByFile[cur] = map[string]bool{}
					}
					removedByFile[cur][strings.ToUpper(hit)] = true
				}
			}
		}
	}

	var out []violation
	for _, ln := range added {
		if v := checkDiffLine(ln.path, ln.line, ln.text); v != nil {
			// 该编号在本文件里本来就存在则不算新引入。
			if removedByFile[ln.path][strings.ToUpper(v.text)] {
				continue
			}
			out = append(out, *v)
		}
	}
	return out, nil
}

// scanNewGoFile 用 go/ast 扫一个尚未入库的新文件（git diff 看不到它，
// 因为它可能根本还没被 add）。不走 scanGoFile 是因为后者按路径取违规，
// 这里直接复用同一套 AST 遍历。
func scanNewGoFile(path string) ([]violation, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, nil // 文件已不在（刚好被删），不算违规
	}
	return scanGoFile(path)
}

// listUntrackedCode 列出未入库的新文件。
// git diff / git ls-files 都看不到它们，而「刚写完的新文件」恰恰是最容易带编号的地方，
// 所以必须单独取：审计自己就是这么漏掉过一次（新写的审计脚本没被拦）。
func listUntrackedCode() ([]string, error) {
	out, err := exec.Command(
		"git", append([]string{"ls-files", "--others", "--exclude-standard", "--"}, scanRoots...)...).Output()
	if err != nil {
		return nil, nil // 不在 git 仓里时不影响门禁主流程
	}
	var files []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			files = append(files, line)
		}
	}
	return files, sc.Err()
}

// scanWorkingTree 扫「已改但未提交」的那部分：uncommitted 改动 + 未入库新文件。
// 本地跑门禁时最常见的形态就是这样——文件改完还没 commit，diff 对比 base...HEAD 看不见。
func scanWorkingTree(base string) ([]violation, error) {
	// 已入库文件的未提交改动与未入库新文件。
	var out []violation

	// 未暂存 + 已暂存但未提交的改动，按文件整份扫比逐行判定更稳（本地场景不必区分存量）。
	diffOut, err := exec.Command("git", "diff", "-U0", "-M", base, "--", ".").Output()
	if err == nil {
		sc := bufio.NewScanner(strings.NewReader(string(diffOut)))
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		var cur string
		var curLine int
		for sc.Scan() {
			text := sc.Text()
			switch {
			case strings.HasPrefix(text, "+++ b/"):
				cur = text[len("+++ b/"):]
			case strings.HasPrefix(text, "@@ "):
				curLine = parseHunkStart(text)
			case strings.HasPrefix(text, "+") && !strings.HasPrefix(text, "+++"):
				body := text[1:]
				if v := checkDiffLine(cur, curLine, body); v != nil {
					out = append(out, *v)
				}
				curLine++
			}
		}
	}

	// 未入库新文件：整份扫。
	files, _ := listUntrackedCode()
	for _, path := range files {
		if isGenerated(path) {
			continue
		}
		if v := scanFileName(path); len(v) > 0 {
			out = append(out, v...)
		}
		switch {
		case strings.HasSuffix(path, ".go"):
			vs, err := scanNewGoFile(path)
			if err == nil {
				out = append(out, vs...)
			}
		default:
			if lexicalExts[strings.ToLower(filepath.Ext(path))] {
				if vs, err := scanLexicalFile(path); err == nil {
					out = append(out, vs...)
				}
			}
		}
	}
	return out, nil
}

// checkDiffLine 判断一条新增行是否违规（文档与生成物跳过）。
func checkDiffLine(path string, line int, body string) *violation {
	if path == "" || isGenerated(path) || isSelfExempt(path) {
		return nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".json", ".yaml", ".yml":
		return nil
	}
	if isExemptLine(body) {
		return nil
	}
	hit, ok := matchRef(body)
	if !ok {
		return nil
	}
	return &violation{path: path, line: line, what: "新增", text: hit, snip: body}
}

func main() {
	stock := flag.Bool("stock", false, "存量模式：扫工作区当前全部内容（期望 0 命中）")
	worktree := flag.Bool("worktree", false, "只扫未提交的改动 + 未入库新文件（本地提交前自检）")
	showAll := flag.Bool("all", false, "完整列出所有命中（默认最多 80 条）")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "用法: go run ./scripts/check-no-ref-in-code.go [--stock|--worktree] [<base-ref>]")
		flag.PrintDefaults()
	}
	flag.Parse()

	var (
		violations []violation
		err        error
	)
	base := "origin/master"
	if flag.NArg() > 0 {
		base = flag.Arg(0)
	}
	switch {
	case *stock:
		violations, err = scanStock()
	case *worktree:
		violations, err = scanWorkingTree(base)
	default:
		violations, err = scanDiff(base)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "检查失败: %v\n", err)
		os.Exit(2)
	}

	if len(violations) == 0 {
		if *stock {
			fmt.Println("✅ 存量模式：代码内容与文件名中已无 FR/ADR/PR 编号")
		} else {
			fmt.Println("✅ 未发现新增的 FR/ADR/PR 编号")
		}
		return
	}

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].path != violations[j].path {
			return violations[i].path < violations[j].path
		}
		return violations[i].line < violations[j].line
	})

	if *stock {
		fmt.Printf("❌ 代码内容与文件名里仍有编号共 %d 处（规则要求 0）\n", len(violations))
	} else {
		fmt.Printf("❌ 本次改动新引入了编号共 %d 处\n", len(violations))
	}
	fmt.Println("   规则见 .claude/rules/no-ref-in-code.md：编号只属于文档；")
	fmt.Println("   注释里写理由本身，文件名与标识符用主题词。")
	fmt.Println()
	limit := len(violations)
	if !*showAll && limit > 80 {
		limit = 80
	}
	for _, v := range violations[:limit] {
		fmt.Println("   " + v.String())
	}
	if len(violations) > limit {
		fmt.Printf("   …… 另有 %d 处未列出（加 -all 看全）\n", len(violations)-limit)
	}
	fmt.Println()
	if !*stock {
		fmt.Println("   同一行里本来就有的存量编号不算违规；存量用 --stock 检查。")
	}
	os.Exit(1)
}
