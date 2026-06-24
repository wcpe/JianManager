// Package search 实现 Worker 本地持久全文索引（FR-074，见 ADR-017）。
//
// 每个实例一份倒排索引，落进节点数据根的 var/index/<instance-uuid>/（ADR-010）。
// 索引是 Worker 私有派生资产，绝不进 Control Plane 数据库（架构不变量）：
// CP 仅经 gRPC SearchFiles 拿查询结果，不持有索引本身。
//
// 设计取舍（见 ADR-017）：在树内零依赖最小倒排索引——token → 文件集合，
// 配合「文件 → 指纹(size+mtime)」表做增量；查询时倒排取候选文件，再在候选内
// 精确扫描提取行号与片段。不引入 bleve（依赖树过重）或 SQLite FTS5（纯 Go 驱动需 cgo）。
package search

import (
	"bufio"
	"encoding/gob"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// defaultMaxResults 是查询命中上限的默认值（调用方传 <=0 时生效）。
	defaultMaxResults = 200
	// maxIndexedFileBytes 是单文件内容索引体积上限：超过则只索引文件名、不索引内容。
	maxIndexedFileBytes = 2 * 1024 * 1024
	// maxIndexedFiles 是单实例索引文件数上限，防止超大目录把索引撑爆。
	maxIndexedFiles = 50000
	// binarySniffBytes 是二进制探测读取的首部字节数。
	binarySniffBytes = 8000
	// maxSnippetBytes 是返回的命中行片段最大字节数（过长行截断）。
	maxSnippetBytes = 400
	// indexFileName 是落盘索引文件名（gob 编码）。
	indexFileName = "index.gob"
	// indexFormatVersion 是落盘格式版本；不匹配时丢弃旧索引全量重建。
	indexFormatVersion = 1
)

// fileFingerprint 是一个文件的指纹，用于增量比对（size+mtime 任一变化即重索引）。
type fileFingerprint struct {
	Size    int64
	ModTime int64 // Unix 纳秒
	// Indexed 为 false 表示该文件因体积/二进制只登记了文件名、未索引内容。
	Indexed bool
}

// persisted 是落盘的索引快照（gob）。
type persisted struct {
	Version int
	// Postings 倒排：token → 该 token 出现过的相对路径集合（用 map 当 set）。
	Postings map[string]map[string]struct{}
	// Files 文件指纹表：相对路径 → 指纹。也是「已知文件全集」（含未索引内容者）。
	Files map[string]fileFingerprint
}

// buildStartHook 是后台首建 goroutine 启动时（Update 之前）的测试钩子，生产恒为 nil。
// 测试用它把一次构建「卡」在途，确定性地验证未就绪查询返回 indexing=true（沿用 BUG-012
// preflight 的可替换 var 模式）。
var buildStartHook func()

// SetBuildStartHookForTest 设置后台首建启动钩子并返回旧值，仅供测试注入「构建在途」用（生产恒为 nil）。
func SetBuildStartHookForTest(fn func()) func() {
	prev := buildStartHook
	buildStartHook = fn
	return prev
}

// Index 是单实例的全文索引。并发安全（自带锁，构建/增量/查询串行化到该实例）。
//
// 首建后台化（FR-113，见 ADR-024）：ready/building 为进程内就绪态（不落盘，Worker 重启归零）；
// builtCh 在首建完成（成功或失败）时关闭，供有界等待者唤醒。building 经 CAS 保证单飞构建。
type Index struct {
	mu       sync.Mutex
	dir      string // 该实例索引目录 <indexRoot>/<instance-uuid>
	ignore   *matcher
	postings map[string]map[string]struct{}
	files    map[string]fileFingerprint
	loaded   bool

	ready    atomic.Bool   // 是否已完成至少一次构建（可信查询）
	building atomic.Bool    // 是否有后台构建在途（CAS 单飞）
	builtCh  chan struct{} // 首建完成时关闭
}

// Hit 是一条搜索命中。content 模式含行号(1 起)与该行片段；filename 模式 Line=0、Snippet 空。
type Hit struct {
	Path    string
	Line    int
	Snippet string
}

// Result 是一次搜索的结果。
type Result struct {
	Hits      []Hit
	Truncated bool // 命中数达到上限被截断
}

// NewIndex 创建（或绑定）某实例的索引对象。
// indexRoot 是 var/index 根；instanceUUID 决定子目录；extraIgnore 追加到默认忽略规则。
// 不立即加载落盘数据，首次 Update/Search 时惰性加载。
func NewIndex(indexRoot, instanceUUID string, extraIgnore []string) *Index {
	return &Index{
		dir:      filepath.Join(indexRoot, instanceUUID),
		ignore:   newMatcher(extraIgnore),
		postings: make(map[string]map[string]struct{}),
		files:    make(map[string]fileFingerprint),
		builtCh:  make(chan struct{}),
	}
}

// Ready 报告索引是否已完成至少一次构建（可信查询）。进程内状态，Worker 重启归零（FR-113，ADR-024）。
func (ix *Index) Ready() bool { return ix.ready.Load() }

// EnsureBuilding 若索引未就绪且无构建在途，则启动一次后台全量构建（CAS 单飞，非阻塞）。
// 构建复用落盘索引做增量追平（非从零）；完成（成功或失败）都置 ready=true 并关闭 builtCh，
// 避免失败时每次查询重新构建导致「索引中」死循环（自愈交由后续同步增量 Update，见 ADR-024 §3）。
func (ix *Index) EnsureBuilding(workDir string) {
	if ix.ready.Load() {
		return
	}
	if !ix.building.CompareAndSwap(false, true) {
		return // 已有构建在途。
	}
	go func() {
		// defer LIFO：先置就绪、再关闭信号，确保等待者被唤醒时 Ready() 已为真。
		defer close(ix.builtCh)
		defer ix.ready.Store(true)
		if buildStartHook != nil {
			buildStartHook()
		}
		_, _ = ix.Update(workDir) // 顶层错误（如 workDir 被删）吞掉记账，置就绪自愈。
	}()
}

// WaitReady 至多等待 d 让首建完成；返回是否已就绪。
// 小目录构建在预算内完成 → 返回 true（调用方本次即可同步查询，不退化）；
// 大目录预算内未完成 → 返回 false（调用方返回 indexing=true）。见 ADR-024 §2。
func (ix *Index) WaitReady(d time.Duration) bool {
	if ix.ready.Load() {
		return true
	}
	select {
	case <-ix.builtCh:
		return true
	case <-time.After(d):
		return ix.ready.Load()
	}
}

// load 惰性加载落盘索引（若存在且格式版本匹配）。已加载则直接返回。调用方须持锁。
func (ix *Index) load() {
	if ix.loaded {
		return
	}
	ix.loaded = true
	f, err := os.Open(filepath.Join(ix.dir, indexFileName))
	if err != nil {
		return // 无落盘索引（首建）或不可读：保持空，交给 Update 全量构建。
	}
	defer f.Close()
	var p persisted
	if err := gob.NewDecoder(f).Decode(&p); err != nil || p.Version != indexFormatVersion {
		return // 损坏或版本不符：丢弃，全量重建。
	}
	if p.Postings != nil {
		ix.postings = p.Postings
	}
	if p.Files != nil {
		ix.files = p.Files
	}
}

// save 落盘当前索引。调用方须持锁。
func (ix *Index) save() error {
	if err := os.MkdirAll(ix.dir, 0o755); err != nil {
		return fmt.Errorf("创建索引目录失败: %w", err)
	}
	tmp := filepath.Join(ix.dir, indexFileName+".tmp")
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("创建索引文件失败: %w", err)
	}
	enc := gob.NewEncoder(f)
	err = enc.Encode(persisted{Version: indexFormatVersion, Postings: ix.postings, Files: ix.files})
	cerr := f.Close()
	if err != nil {
		return fmt.Errorf("写索引失败: %w", err)
	}
	if cerr != nil {
		return fmt.Errorf("关闭索引文件失败: %w", cerr)
	}
	// 原子替换：先写 .tmp 再 rename，避免半截索引。
	if err := os.Rename(tmp, filepath.Join(ix.dir, indexFileName)); err != nil {
		return fmt.Errorf("替换索引文件失败: %w", err)
	}
	return nil
}

// Update 扫描工作目录并增量更新索引（FR-074，见 ADR-017）。
//
// 遍历当前文件，按指纹(size+mtime)与索引内记录比对：
//   - 新增/变化文件：重新索引（读内容切词，更新倒排）。
//   - 删除文件：从倒排与指纹表移除。
//   - 未变文件：跳过。
//
// 增量经目录扫描 + 指纹比对驱动，不依赖文件系统事件（跨平台一致、Worker 重启自愈）。
// 更新后落盘。返回扫描到的文件数。
func (ix *Index) Update(workDir string) (int, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.load()

	seen := make(map[string]struct{})
	changed := false
	count := 0

	walkErr := filepath.WalkDir(workDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// 单个条目读取出错（权限/竞态删除）不致命，跳过。
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(workDir, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if ix.ignore.ignored(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if count >= maxIndexedFiles {
			return nil // 超过文件数上限：停止登记新文件（已登记的仍可搜）。
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		seen[rel] = struct{}{}
		count++

		fp := fileFingerprint{Size: info.Size(), ModTime: info.ModTime().UnixNano()}
		if old, ok := ix.files[rel]; ok && old.Size == fp.Size && old.ModTime == fp.ModTime {
			return nil // 未变，跳过。
		}
		// 新增或变化：先移除旧倒排贡献（若有），再重索引。
		ix.removePostings(rel)
		indexed := ix.indexFile(p, rel, info.Size())
		fp.Indexed = indexed
		ix.files[rel] = fp
		changed = true
		return nil
	})
	if walkErr != nil {
		return count, fmt.Errorf("扫描工作目录失败: %w", walkErr)
	}

	// 删除已不存在的文件。
	for rel := range ix.files {
		if _, ok := seen[rel]; !ok {
			ix.removePostings(rel)
			delete(ix.files, rel)
			changed = true
		}
	}

	if changed {
		if err := ix.save(); err != nil {
			return count, err
		}
	}
	return count, nil
}

// indexFile 读取并索引单个文件的内容。返回是否真的索引了内容
// （false 表示因体积/二进制只登记文件名）。调用方须持锁。
func (ix *Index) indexFile(absPath, rel string, size int64) bool {
	if size > maxIndexedFileBytes {
		return false // 超大文件只登记文件名（可被 filename 搜索命中），不索引内容。
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return false
	}
	sniff := data
	if len(sniff) > binarySniffBytes {
		sniff = sniff[:binarySniffBytes]
	}
	if looksBinary(sniff) {
		return false
	}
	for tok := range tokenize(string(data)) {
		set := ix.postings[tok]
		if set == nil {
			set = make(map[string]struct{})
			ix.postings[tok] = set
		}
		set[rel] = struct{}{}
	}
	return true
}

// removePostings 从倒排中移除某文件的全部贡献。调用方须持锁。
// 倒排不存位置，故按 token 遍历删除该文件；空 token 桶一并清理。
func (ix *Index) removePostings(rel string) {
	for tok, set := range ix.postings {
		if _, ok := set[rel]; ok {
			delete(set, rel)
			if len(set) == 0 {
				delete(ix.postings, tok)
			}
		}
	}
}

// candidateFiles 用倒排求「包含查询全部 token」的候选文件集合（AND 语义）。
// 查询无可用 token（如纯标点）时返回 nil, false，调用方回退到对全部已索引文件扫描。
// 调用方须持锁。
func (ix *Index) candidateFiles(tokens []string) (map[string]struct{}, bool) {
	if len(tokens) == 0 {
		return nil, false
	}
	// 从最小的桶开始求交集。
	var smallest map[string]struct{}
	for _, tok := range tokens {
		set := ix.postings[tok]
		if len(set) == 0 {
			return map[string]struct{}{}, true // 任一 token 无命中 → 整体无候选。
		}
		if smallest == nil || len(set) < len(smallest) {
			smallest = set
		}
	}
	cand := make(map[string]struct{}, len(smallest))
	for rel := range smallest {
		all := true
		for _, tok := range tokens {
			if _, ok := ix.postings[tok][rel]; !ok {
				all = false
				break
			}
		}
		if all {
			cand[rel] = struct{}{}
		}
	}
	return cand, true
}

// SearchContent 全文搜索：返回包含 query 的文件中匹配行的行号与片段（FR-074）。
// 匹配按子串（大小写不敏感）。倒排缩小候选后在候选文件内精确扫描定位行。
// workDir 用于读取候选文件内容；maxResults<=0 时取默认上限。
func (ix *Index) SearchContent(workDir, query string, maxResults int) (Result, error) {
	if maxResults <= 0 {
		maxResults = defaultMaxResults
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return Result{}, nil
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.load()

	toks := queryTokens(q)
	var files []string
	if cand, ok := ix.candidateFiles(toks); ok {
		for rel := range cand {
			files = append(files, rel)
		}
	} else {
		// 查询无 token（纯符号子串）：回退到全部已索引内容的文件。
		for rel, fp := range ix.files {
			if fp.Indexed {
				files = append(files, rel)
			}
		}
	}
	sort.Strings(files) // 稳定输出顺序。

	needle := strings.ToLower(q)
	var res Result
	for _, rel := range files {
		hits, err := scanFileLines(filepath.Join(workDir, filepath.FromSlash(rel)), rel, needle, maxResults-len(res.Hits))
		if err != nil {
			continue
		}
		res.Hits = append(res.Hits, hits...)
		if len(res.Hits) >= maxResults {
			res.Truncated = true
			res.Hits = res.Hits[:maxResults]
			break
		}
	}
	return res, nil
}

// scanFileLines 在单个文件内按子串(已小写 needle)逐行扫描，返回命中行（行号 1 起 + 截断片段）。
// limit<=0 时不再产生命中（用于全局上限收口）。
func scanFileLines(absPath, rel, needle string, limit int) ([]Hit, error) {
	if limit <= 0 {
		return nil, nil
	}
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var hits []Hit
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if strings.Contains(strings.ToLower(line), needle) {
			hits = append(hits, Hit{Path: rel, Line: lineNo, Snippet: snippet(line)})
			if len(hits) >= limit {
				break
			}
		}
	}
	return hits, sc.Err()
}

// snippet 把命中行裁成不超过 maxSnippetBytes 的片段（去首尾空白、超长截断加省略号）。
func snippet(line string) string {
	s := strings.TrimSpace(line)
	if len(s) <= maxSnippetBytes {
		return s
	}
	// 按 rune 边界安全截断。
	b := []byte(s)
	cut := maxSnippetBytes
	for cut > 0 && !utf8RuneStart(b[cut]) {
		cut--
	}
	return string(b[:cut]) + "…"
}

// utf8RuneStart 判定字节是否是 UTF-8 字符的起始字节（非 0b10xxxxxx 续接字节）。
func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// SearchFilename 文件名快速打开（quick-open）：返回路径/basename 含 query(子串,大小写不敏感)
// 的文件，按路径排序。Line 恒为 0、Snippet 空。基于指纹表里的「已知文件全集」，
// 含因体积/二进制未索引内容的文件（文件名仍可被命中）。
func (ix *Index) SearchFilename(query string, maxResults int) Result {
	if maxResults <= 0 {
		maxResults = defaultMaxResults
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return Result{}
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.load()

	var matched []string
	for rel := range ix.files {
		lower := strings.ToLower(rel)
		base := lower
		if i := strings.LastIndex(lower, "/"); i >= 0 {
			base = lower[i+1:]
		}
		// basename 命中优先于仅路径命中，但都接受。
		if strings.Contains(base, q) || strings.Contains(lower, q) {
			matched = append(matched, rel)
		}
	}
	// basename 命中的排前面，其余按路径序，整体可预测。
	sort.Slice(matched, func(i, j int) bool {
		bi := baseContains(matched[i], q)
		bj := baseContains(matched[j], q)
		if bi != bj {
			return bi // true 排前
		}
		return matched[i] < matched[j]
	})

	var res Result
	for _, rel := range matched {
		if len(res.Hits) >= maxResults {
			res.Truncated = true
			break
		}
		res.Hits = append(res.Hits, Hit{Path: rel})
	}
	return res
}

// baseContains 判定 rel 的 basename(小写) 是否含子串 q(已小写)。
func baseContains(rel, q string) bool {
	lower := strings.ToLower(rel)
	if i := strings.LastIndex(lower, "/"); i >= 0 {
		lower = lower[i+1:]
	}
	return strings.Contains(lower, q)
}

// Remove 删除该实例的整个索引目录（实例销毁时清理派生资产）。
func (ix *Index) Remove() error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.postings = make(map[string]map[string]struct{})
	ix.files = make(map[string]fileFingerprint)
	ix.loaded = true
	if err := os.RemoveAll(ix.dir); err != nil {
		return fmt.Errorf("删除索引目录失败: %w", err)
	}
	return nil
}

// fileCount 返回当前已登记文件数（测试用）。
func (ix *Index) fileCount() int {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.load()
	return len(ix.files)
}
