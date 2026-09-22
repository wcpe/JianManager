package crashdiag

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// 崩溃根因枚举（FR-470 spec §2.1）。分类为启发式规则，返回置信度 + 证据原文供人复核。
const (
	CrashRootCauseOOM           = "oom"
	CrashRootCausePortInUse     = "port_in_use"
	CrashRootCauseClassNotFound = "class_not_found"
	CrashRootCauseJVMArgs       = "jvm_args"
	CrashRootCausePermission    = "permission"
	CrashRootCauseSegfault      = "segfault"
	CrashRootCauseCorruptData   = "corrupt_data"
	CrashRootCauseUnknown       = "unknown"
)

// CrashClassification 一次崩溃快照的分类结果（FR-470）。
type CrashClassification struct {
	// SnapshotID 关联快照 ID（读侧回填；纯分类时为 0）。
	SnapshotID uint `json:"snapshotId"`
	// RootCause 根因枚举（多规则命中取最高分）。
	RootCause string `json:"rootCause"`
	// Signature 归一化「同类指纹」：Exception 类名 + 首行去噪，用于同类聚合。
	Signature string `json:"signature"`
	// Evidence 命中规则的原文行（去重、按出现顺序，最多 maxCrashEvidenceLines 行）。
	Evidence []string `json:"evidence"`
	// Confidence 0~1。规则基础分 + 命中行数奖励，封顶 0.98。
	Confidence float64 `json:"confidence"`
	// Labels 全部命中的根因（含 RootCause），供「多标签」展示。
	Labels []string `json:"labels"`
}

// crashRule 一条分类规则：命中任一 pattern 即认为该根因成立。
type crashRule struct {
	cause    string
	base     float64
	patterns []*regexp.Regexp
}

// maxCrashEvidenceLines 证据最多保留的原文行数（前端展开区，避免撑爆卡片）。
const maxCrashEvidenceLines = 8

// crashSignatureMaxLen 指纹列长度上限（对齐 varchar(255)）。
const crashSignatureMaxLen = 255

// crashRules 规则表：顺序即平局优先级（越靠前越优先）。
//
// 「优先级」= 语义确定性：OOM 与段错误有明确文本/信号证据，端口占用与类找不到次之，
// JVM 参数与权限再次，数据损坏最泛。命中同分时取靠前者，避免同一现场在两次运行间摇摆。
var crashRules = []crashRule{
	{cause: CrashRootCauseOOM, base: 0.9, patterns: []*regexp.Regexp{
		regexp.MustCompile(`(?i)java\.lang\.OutOfMemoryError`),
		regexp.MustCompile(`(?i)OutOfMemoryError`),
		regexp.MustCompile(`(?i)Out of memory`),
		regexp.MustCompile(`(?i)Cannot allocate memory`),
		regexp.MustCompile(`(?i)std::bad_alloc`),
		regexp.MustCompile(`(?i)insufficient memory`),
		regexp.MustCompile(`(?i)Killed process \d+`),
		regexp.MustCompile(`(?i)memory allocation of \d+ bytes failed`),
	}},
	{cause: CrashRootCauseSegfault, base: 0.9, patterns: []*regexp.Regexp{
		regexp.MustCompile(`(?i)SIGSEGV`),
		regexp.MustCompile(`(?i)hs_err_pid\d+`),
		regexp.MustCompile(`(?i)segmentation fault`),
		regexp.MustCompile(`(?i)EXCEPTION_ACCESS_VIOLATION`),
		regexp.MustCompile(`(?i)A fatal error has been detected by the Java Runtime Environment`),
		regexp.MustCompile(`(?i)SIGBUS`),
	}},
	{cause: CrashRootCausePortInUse, base: 0.9, patterns: []*regexp.Regexp{
		regexp.MustCompile(`(?i)Address already in use`),
		regexp.MustCompile(`(?i)java\.net\.BindException`),
		regexp.MustCompile(`(?i)Failed to bind to port`),
		regexp.MustCompile(`(?i)bind\(\) to .* failed`),
		regexp.MustCompile(`(?i)listen tcp .*: bind: address already in use`),
	}},
	{cause: CrashRootCauseClassNotFound, base: 0.85, patterns: []*regexp.Regexp{
		regexp.MustCompile(`(?i)NoClassDefFoundError`),
		regexp.MustCompile(`(?i)ClassNotFoundException`),
		regexp.MustCompile(`(?i)Could not find or load main class`),
		regexp.MustCompile(`(?i)UnsatisfiedLinkError`),
		regexp.MustCompile(`(?i)NoSuchMethodError`),
		regexp.MustCompile(`(?i)NoSuchFieldError`),
	}},
	{cause: CrashRootCauseJVMArgs, base: 0.85, patterns: []*regexp.Regexp{
		regexp.MustCompile(`(?i)Unrecognized VM option`),
		regexp.MustCompile(`(?i)Unrecognized option:`),
		regexp.MustCompile(`(?i)Error: Could not create the Java Virtual Machine`),
		regexp.MustCompile(`(?i)Invalid maximum heap size`),
		regexp.MustCompile(`(?i)Could not reserve enough space for \d+ object heap`),
		regexp.MustCompile(`(?i)Error: A fatal exception has occurred\. Program will exit`),
		// m-4①：JDK 升级后旧 class/旧 jar 跑在新 JVM 上最常见的崩溃之一，
		// 归 jvm_args（运行环境不匹配，处置方式同样是调 JVM/换 JDK 而非查数据）。
		regexp.MustCompile(`(?i)UnsupportedClassVersionError`),
		regexp.MustCompile(`(?i)class file version \d+\.\d+`),
	}},
	{cause: CrashRootCausePermission, base: 0.8, patterns: []*regexp.Regexp{
		regexp.MustCompile(`(?i)Permission denied`),
		regexp.MustCompile(`(?i)java\.nio\.AccessDeniedException`),
		regexp.MustCompile(`(?i)Operation not permitted`),
		// m-4②：FileNotFoundException **不**在此列——它的文案通常指「文件不存在」
		// （服务端 jar 被删/路径写错/world 目录缺失），而非权限不足，硬归 permission
		// 会把绝大多数「文件缺失」现场误导到排查目录权限。它改由 corrupt_data 承接
		// （同为「数据/文件层面的问题」），证据文案仍保留原文供人工复核。
	}},
	{cause: CrashRootCauseCorruptData, base: 0.7, patterns: []*regexp.Regexp{
		regexp.MustCompile(`(?i)Failed to load`),
		regexp.MustCompile(`(?i)Corrupted`),
		regexp.MustCompile(`(?i)invalid stream header`),
		regexp.MustCompile(`(?i)zip file is empty`),
		regexp.MustCompile(`(?i)java\.util\.zip\.ZipException`),
		regexp.MustCompile(`(?i)EOFException`),
		regexp.MustCompile(`(?i)is not a valid`),
		// m-4②：文件缺失/读不到（服务端 jar 被删、world 目录缺失、路径写错）。
		// 归在「数据/文件层面的问题」而非权限：同现场绝大多数不是权限问题。
		regexp.MustCompile(`(?i)java\.io\.FileNotFoundException`),
		regexp.MustCompile(`(?i)No such file or directory`),
	}},
}

// signatureHint 用于挑选「同类指纹」候选行的正则（依次尝试）。
var signatureHints = []*regexp.Regexp{
	regexp.MustCompile(`(?i)Caused by:\s*(.+)`),
	regexp.MustCompile(`(?i)([\w.$]+(?:Exception|Error))(?::\s*(.*))?$`),
	regexp.MustCompile(`(?i)panic:\s*(.+)`),
	regexp.MustCompile(`(?i)(FATAL|ERROR)[:\]]\s*(.+)`),
}

// ClassifyCrashSnapshot 对一个崩溃现场做根因归类（FR-470 §2.1）。
//
// 输入：退出码、终止信号名、崩溃前终端尾部输出。纯函数、无副作用、确定性
// （同输入恒同输出），可在入库事务内同步调用，也可对历史快照批量重跑。
//
// 规则顺序与退出码/信号先验结合：文本规则先收集证据并打分，信号/退出码先验仅在
// 「文本未给出明确根因」时兜底（避免把一条恰好含 "Error" 的日志误判成 OOM）。
func ClassifyCrashSnapshot(exitCode int, signal, tailOutput string) CrashClassification {
	lines := splitCrashLines(tailOutput)
	result := CrashClassification{RootCause: CrashRootCauseUnknown, Confidence: 0.0}

	// 1. 文本规则打分。
	scored := make(map[string]float64)
	evidence := make([]string, 0, maxCrashEvidenceLines)
	seenEvidence := make(map[string]struct{})
	matched := make(map[string]map[string]struct{}) // cause -> 命中行集合（去重后计入奖励）
	for _, rule := range crashRules {
		for _, line := range lines {
			if !crashLineMatches(rule, line) {
				continue
			}
			if matched[rule.cause] == nil {
				matched[rule.cause] = make(map[string]struct{})
			}
			matched[rule.cause][line] = struct{}{}
			if _, dup := seenEvidence[line]; !dup && len(evidence) < maxCrashEvidenceLines {
				seenEvidence[line] = struct{}{}
				evidence = append(evidence, line)
			}
		}
		if hits := len(matched[rule.cause]); hits > 0 {
			// 基础分 + 命中的不同行数奖励（每多一行 +0.02，封顶 +0.08）。
			boost := float64(hits-1) * 0.02
			if boost > 0.08 {
				boost = 0.08
			}
			score := rule.base + boost
			if score > 0.98 {
				score = 0.98
			}
			scored[rule.cause] = score
		}
	}

	// 2. 信号 / 退出码先验。
	applyCrashPriors(scored, exitCode, signal, len(evidence) > 0)

	// 3. 取最高分（平局按 crashRules 顺序，即确定性）。
	best := ""
	bestScore := 0.0
	for _, rule := range crashRules {
		if s, ok := scored[rule.cause]; ok && s > bestScore {
			best = rule.cause
			bestScore = s
		}
	}
	if best != "" {
		result.RootCause = best
		result.Confidence = bestScore
	}

	// 4. 多标签：全部命中且分数不低于 0.7 的根因（按分数降序，同分按规则顺序）。
	result.Labels = crashLabels(scored, result.RootCause)

	// 5. 指纹归一。
	result.Signature = crashSignature(lines, result.RootCause)
	result.Evidence = evidence
	return result
}

// crashLineMatches 报告某行是否命中规则的任一 pattern。
func crashLineMatches(rule crashRule, line string) bool {
	for _, re := range rule.patterns {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// applyCrashPriors 用退出码/信号先验补分：仅在文本未给出明确根因时兜底。
//
// killed（SIGKILL，退出码 137=128+9）是 OOM killer 的经典指纹；segmentation fault
// （SIGSEGV）同理。先验分低于文本规则的 0.8+，故文本命中时不会被覆盖；文本沉默时
// 至少给出「更像 OOM 而不是 unknown」的判断，置信度保持中等（0.45~0.6）。
func applyCrashPriors(scored map[string]float64, exitCode int, signal string, hasTextEvidence bool) {
	normalized := strings.ToLower(strings.TrimSpace(signal))
	switch {
	case exitCode == 137 || normalized == "killed":
		if _, ok := scored[CrashRootCauseOOM]; !ok {
			prior := 0.45
			if !hasTextEvidence {
				prior = 0.6
			}
			scored[CrashRootCauseOOM] = prior
		}
	case exitCode == 139 || strings.Contains(normalized, "segmentation") || normalized == "segv":
		if _, ok := scored[CrashRootCauseSegfault]; !ok {
			prior := 0.45
			if !hasTextEvidence {
				prior = 0.6
			}
			scored[CrashRootCauseSegfault] = prior
		}
	}
}

// crashLabels 汇总多标签：分数 ≥0.7 的根因按分数降序（同分按规则顺序），恒含主根因。
func crashLabels(scored map[string]float64, rootCause string) []string {
	type entry struct {
		cause string
		score float64
		order int
	}
	var list []entry
	for i, rule := range crashRules {
		if s, ok := scored[rule.cause]; ok && s >= 0.7 {
			list = append(list, entry{cause: rule.cause, score: s, order: i})
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].order < list[j].order
	})
	labels := make([]string, 0, len(list)+1)
	seen := make(map[string]struct{})
	if rootCause != "" && rootCause != CrashRootCauseUnknown {
		labels = append(labels, rootCause)
		seen[rootCause] = struct{}{}
	}
	for _, e := range list {
		if _, dup := seen[e.cause]; dup {
			continue
		}
		labels = append(labels, e.cause)
		seen[e.cause] = struct{}{}
	}
	return labels
}

// splitCrashLines 按行切分尾部输出，去除行首尾空白与空行。
func splitCrashLines(tail string) []string {
	if tail == "" {
		return nil
	}
	raw := strings.Split(tail, "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		l = strings.TrimSpace(strings.TrimSuffix(l, "\r"))
		if l == "" {
			continue
		}
		out = append(out, l)
	}
	return out
}

// crashSignature 计算归一化「同类指纹」。
//
// 目标：同一种异常在多次崩溃里得到**同一个**指纹（哪怕时间戳/行号/内存地址不同），
// 不同种异常得到不同指纹。做法：挑出最像「异常类名 + 首要信息」的一行，再抹掉易变噪声：
// 时间戳、十六进制地址、纯数字（行号/字节数/端口）、多余空白。
//
// 找不到任何异常特征行时，回退「根因 + 首个非日志前缀行」，仍能形成可比对的指纹；
// 实在无内容时回退根因本身（unknown 崩溃也会归为同一桶）。
func crashSignature(lines []string, rootCause string) string {
	for _, hint := range signatureHints {
		for _, line := range lines {
			m := hint.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			// 优先取第二个捕获组（信息体），退回首行。
			candidate := ""
			for i := len(m) - 1; i >= 1; i-- {
				if strings.TrimSpace(m[i]) != "" {
					candidate = m[i]
					break
				}
			}
			if candidate == "" {
				candidate = m[0]
			}
			if sig := normalizeCrashSignature(candidate); sig != "" {
				return sig
			}
		}
	}
	// 无异常特征：用根因 + 首行兜底。
	for _, line := range lines {
		if sig := normalizeCrashSignature(stripCrashLogPrefix(line)); sig != "" {
			return truncateCrashSignature(rootCause + "|" + sig)
		}
	}
	if rootCause == "" {
		rootCause = CrashRootCauseUnknown
	}
	return rootCause
}

var (
	crashLogPrefixRe = regexp.MustCompile(`^\[[^\]]*\]\s*(\[[^\]]*\]\s*)?(WARN|ERROR|INFO|DEBUG|FATAL|SEVERE)?[:\]]?\s*`)
	crashHexRe       = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	crashDigitRe     = regexp.MustCompile(`\d+`)
	crashSpaceRe     = regexp.MustCompile(`\s+`)
)

// stripCrashLogPrefix 去掉常见日志抬头（如 `[12:34:56] [Server thread/ERROR]:`）。
func stripCrashLogPrefix(line string) string {
	return crashLogPrefixRe.ReplaceAllString(line, "")
}

// normalizeCrashSignature 抹去指纹中的易变噪声并归一化空白。
func normalizeCrashSignature(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = stripCrashLogPrefix(s)
	s = crashHexRe.ReplaceAllString(s, "HEX")
	s = crashDigitRe.ReplaceAllString(s, "N")
	s = crashSpaceRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	return truncateCrashSignature(s)
}

// truncateCrashSignature 截断到指纹列上限。
func truncateCrashSignature(s string) string {
	if len(s) > crashSignatureMaxLen {
		return s[:crashSignatureMaxLen]
	}
	return s
}

// EncodeCrashEvidence 把证据行序列化为 JSON 数组文本（落库用）。空切片返回空串。
func EncodeCrashEvidence(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	raw, err := json.Marshal(lines)
	if err != nil {
		return ""
	}
	return string(raw)
}

// DecodeCrashEvidence 解析证据 JSON 文本为字符串切片；空串/非法 JSON 返回 nil。
func DecodeCrashEvidence(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}
