package ingest

// 本文件守住两件事（2026-10-21）：
//
// ① 缺陷 B：**缺省**归档发现必须覆盖「与活文件同前缀的无扩展名数字轮转」。
//    BungeeCord 的轮转产物是 proxy.log.1 / proxy.log.2（纯文本、无扩展名），导入侧早已支持
//    该形态（acquire.DetectArchiveFormat 按文件头分流 + 账本 rotated_text），但缺省 glob 只写
//    *.gz ⇒ 该形态在缺省配置下永远不可达。用例同时锁住「不得放宽成 *.log」这条边界：
//    同目录的其它活文件、轮转文件的锁文件、备份名一律不得进入候选。
//
// ② 缺陷 A 的**安全前提**：增量尾部快路径之所以可以不做 event_id 级去重，是因为
//    「部分写入失败后的重投」必定是**整窗重复**（PublicationPending 在写入之前就被持久化，
//    下一轮据此走 replace 而非 tail）。把这条前提写成断言，是为了防止有人把 pending 从
//    planDeliveryTail 的判据里"优化"掉——那样重投真的会落到 tail 快路径上，而已写入的行
//    会被原样再插一份（inst:318 形态：13 万行只有 6.4 万唯一 event_id）。
//    为什么不是"在 tail 路径上加去重"：校验面（verifyProjectionChunk）钉在**本次代次名**上，
//    而重投必然换到新代次名（代次名不得复用是 2026-10-01 生产事故后的红线）；于是被去重
//    跳过的那部分行在新代次下查不到 ⇒ 校验恒「not fully visible」⇒ 永不 publish、水位钉死。
//    即：跨代次去重与现有校验面语义冲突，属设计级改动，不是局部补丁。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// insertGateVL 在既有内存 VL 前面加一道「插入请求闸」：可从第 N 个插入请求起返回 500，
// 用来精确造出「分批插入时前几批成功、后续批失败」（VL 超时/部分接受）的现场。
//
// 为什么需要它而不是复用 reconcileVLFixture.failInsert：后者是**全有或全无**的开关
// （所有插入都失败），造不出「部分已落库」这个正是缺陷 A 关心的中间态。
type insertGateVL struct {
	inner *reconcileVLFixture
	mu    sync.Mutex
	count int
	// failAfter > 0 时，自第 failAfter+1 个插入请求起一律 500。
	failAfter int
}

func (g *insertGateVL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/insert/jsonline" {
		g.mu.Lock()
		g.count++
		fail := g.failAfter > 0 && g.count > g.failAfter
		g.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}
	g.inner.ServeHTTP(w, r)
}

// arm 让「第 failAfter 个之后的插入请求」失败（计数从头开始）。
func (g *insertGateVL) arm(failAfter int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.count = 0
	g.failAfter = failAfter
}

// disarm 恢复正常插入。
func (g *insertGateVL) disarm() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.count = 0
	g.failAfter = 0
}

// touchFile 建一个普通文件（内容非空；0 字节在归档导入里等价于空归档，另有用例覆盖）。
func touchFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("[12:00:00] [Server thread/INFO]: rotated line\n"), 0o644))
}

// baseNames 把路径集合压成排序后的 basename 切片，便于逐字断言「发现了哪些」。
func baseNames(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, filepath.Base(path))
	}
	sort.Strings(out)
	return out
}

// TestDiscoverSourceArchivesDefaultFindsUnnumberedRotations 是缺陷 B 的主回归。
//
// 现场形态（BungeeCord / java.util.logging，见 rules.go 的 bungee 规则注释）：
// 活跃文件 `proxy.log.0`，轮转产物 `proxy.log.1`、`proxy.log.2`，另有 0 字节锁文件
// `proxy.log.0.lck` 与 `proxy.log.1.lck`，以及同目录**别的**日志 `other.log` / `other.log.1`。
//
// 断言：候选 = {proxy.log.1, proxy.log.2} + 既有 *.gz（gzip 行为不得退化）。
//
// 转红方式（实测）：把 discoverRotatedLogArchives 的调用删掉（回到只 glob *.gz）⇒ 本用例红；
// 把「纯数字后缀」判据放宽成 `strings.HasPrefix(name, stem+".")`（等价于 *.log.*）⇒
// .lck 与 other.log.1 进入候选 ⇒ 本用例红（这条边界正是「不要宽泛匹配」的要求）。
func TestDiscoverSourceArchivesDefaultFindsUnnumberedRotations(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "proxy.log.0")
	for _, name := range []string{
		"proxy.log.0",     // 活文件自身（必须排除：它由 FileTailer 采集）
		"proxy.log.1",     // 轮转产物（纯文本、无扩展名）⇒ 必须发现
		"proxy.log.2",     // 同上
		"proxy.log.0.lck", // 活跃文件的 JUL 锁文件（0 字节，非归档）
		"proxy.log.1.lck", // 轮转文件的锁文件（0 字节，非归档）
		"proxy.log.bak",   // 运维备份名（非数字后缀，不得误纳）
		"other.log",       // 同目录别的活文件：绝不能被当成本源归档
		"other.log.1",     // 别的族的轮转：同样不得纳入
		"archived.gz",     // 既有 *.gz 契约（不得退化）
	} {
		path := filepath.Join(dir, name)
		touchFile(t, path)
		if name == "proxy.log.0.lck" || name == "proxy.log.1.lck" {
			// 锁文件是 0 字节（现场实测）；内容按 0 字节造，避免用例自身偏离现场形态。
			require.NoError(t, os.WriteFile(path, nil, 0o644))
		}
	}

	source := SourceConfig{LogSourceID: "node:bungee", SourceGeneration: "g1", Path: live}
	got, err := discoverSourceArchives(source)
	require.NoError(t, err)
	require.Equal(t, []string{"archived.gz", "proxy.log.1", "proxy.log.2"}, baseNames(got),
		"缺省发现必须包含无扩展名轮转与既有 gz，且排除活文件、锁文件、备份名与别的日志族")
}

// TestDiscoverSourceArchivesDefaultHandlesLog4jLiveName —— 同一判据对 log4j 形态也成立。
//
// log4j2 的活跃文件是 `latest.log`（无数字后缀），轮转为 `latest.log.1`；而 java.util.logging
// 的**当前**文件 `proxy.log.0` 在同名前缀下属于「活跃文件」而非轮转（见 discoverRotatedLogArchives
// 的注释）⇒ 必须排除，否则与实时 tail 双计。
func TestDiscoverSourceArchivesDefaultHandlesLog4jLiveName(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "latest.log")
	for _, name := range []string{"latest.log", "latest.log.1", "latest.log.2", "latest.log.0", "other.log.1"} {
		touchFile(t, filepath.Join(dir, name))
	}
	source := SourceConfig{LogSourceID: "node:paper", SourceGeneration: "g1", Path: live}
	got, err := discoverSourceArchives(source)
	require.NoError(t, err)
	require.Equal(t, []string{"latest.log.1", "latest.log.2"}, baseNames(got),
		"活文件自身与「活文件名无数字后缀时的 .0（=JUL 当前文件）」都不得进入候选")
}

// TestDiscoverSourceArchivesExplicitGlobIsVerbatim —— 显式 glob 不得被"善意放宽"。
//
// 运维/规则表写下的 archive_glob 是**完整判据**：写了 *.gz 就只发现 *.gz。
// 这也是本修复的安全边界——放宽只作用于缺省分支，任何显式配置（含规则表下发的
// proxy.log.[1-9]* 与 instances.go 路径覆盖分支的 <dir>/*.gz）逐字不变。
func TestDiscoverSourceArchivesExplicitGlobIsVerbatim(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"proxy.log.0", "proxy.log.1", "proxy.log.2", "archived.gz"} {
		touchFile(t, filepath.Join(dir, name))
	}
	source := SourceConfig{
		LogSourceID: "node:bungee", SourceGeneration: "g1",
		Path:        filepath.Join(dir, "proxy.log.0"),
		ArchiveGlob: filepath.Join(dir, "*.gz"),
	}
	got, err := discoverSourceArchives(source)
	require.NoError(t, err)
	require.Equal(t, []string{"archived.gz"}, baseNames(got),
		"显式 glob 逐字生效：轮转不得因缺省分支的放宽而被纳入")
}

// TestDiscoverSourceArchivesMissingDirIsNotAnError —— 目录暂不存在（实例未启动）不算失败。
//
// 为什么必须如此：归档发现报错会登记 ARCHIVE_DISCOVERY_FAILED 缺口，而该原因不该由
// 「实例目录还没建出来」这类常态触发。
func TestDiscoverSourceArchivesMissingDirIsNotAnError(t *testing.T) {
	source := SourceConfig{
		LogSourceID: "node:absent", SourceGeneration: "g1",
		Path: filepath.Join(t.TempDir(), "not-created-yet", "proxy.log.0"),
	}
	got, err := discoverSourceArchives(source)
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestPollSourceImportsUnnumberedRotationByDefault 是缺陷 B 的端到端回归：
// 缺省配置（ArchiveGlob 为空）下，与活文件同前缀的无扩展名轮转必须真的被导入账本。
//
// 断言的是**账本登记**（而不是"候选列表里有它"）：只有形态分流 + 导入 + 记账全链路走通，
// `proxy.log.1` 才会以 rotated_text 形态出现在分段里；把这步删掉（回到只 glob *.gz）即红。
func TestPollSourceImportsUnnumberedRotationByDefault(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	dir := filepath.Join(root, "bungee")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	live := filepath.Join(dir, "proxy.log.0")
	require.NoError(t, os.WriteFile(live, []byte("[12:00:00] [Server thread/INFO]: live line\n"), 0o644))
	rotated := filepath.Join(dir, "proxy.log.1")
	require.NoError(t, os.WriteFile(rotated,
		[]byte("[11:59:00] [Server thread/INFO]: rotated one\n[11:59:01] [Server thread/INFO]: rotated two\n"), 0o644))

	src := SourceConfig{
		LogSourceID:      "node:bungee-default",
		SourceGeneration: "g1",
		Path:             live,
		Mode:             pipeline.ModeFilePrimary,
		StorageNamespace: "ns:bungee-default",
		UTCDay:           runtimeTestUTCDay(),
		// 刻意不设 ArchiveGlob：本用例测的就是**缺省**分支。
	}
	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{src}})
	require.NoError(t, err)

	m.pollOnce()

	key := src.LogSourceID + "/" + src.SourceGeneration
	p, ok := m.pipeFor(key)
	require.True(t, ok)
	entry := p.Ledger().Get(p.Key())
	require.NotNil(t, entry)

	kindByPath := map[string]ledger.SegmentKind{}
	importedByPath := map[string]bool{}
	for _, seg := range entry.Segments {
		kindByPath[filepath.Base(seg.Path)] = seg.Kind
		importedByPath[filepath.Base(seg.Path)] = seg.Imported
	}
	require.Equal(t, ledger.SegmentRotatedText, kindByPath["proxy.log.1"],
		"缺省发现必须把无扩展名轮转交给纯文本形态导入（删掉默认放宽即红）: %v", kindByPath)
	require.True(t, importedByPath["proxy.log.1"], "轮转归档必须被标记为已导入，否则每轮都会重发现")

	// 活文件自身绝不能被当归档导入（否则同一份字节被 tail 与归档各计一次）。
	require.NotEqual(t, ledger.SegmentRotatedText, kindByPath["proxy.log.0"],
		"活文件自身不得作为归档被导入: %v", kindByPath)
}

// TestDeliveryRetryAfterPartialInsertIsNotTailFastPath 锁住缺陷 A 的**安全前提**。
//
// 形态（现场 §缺陷 A）：tail 批次经 insertInBatchesCtxYield 分批插入，前几批成功、后续批失败。
// 事实：writeProjectionPlan 在**写入之前**就把 `PublicationPending=true` 与本次代次名持久化
// （persistHot），于是下一轮的 planDeliveryTail 因 pending 直接返 false，重投走 replace 整窗
// 重写 ⇒ tail 快路径不会重跑，那批已写入的行不会被"原样再插一份"。
//
// 断言分两半，缺一不可：
//   - 失败后 state 确实 pending=true（前提成立，不是空谈）；
//   - 同一批事件、同一水位下，pending=true ⇒ Fast=false；清掉 pending ⇒ Fast=true（对照）。
//     对照是必要的：否则「Fast=false」可能只是因为水位判据本来就不满足，用例会假绿。
//
// 转红方式（实测）：把 planDeliveryTail 首个判据里的 `saved.PublicationPending` 删掉
// ⇒ 半个用例立刻转红（重投会重新落回 tail 快路径）。另见文件头对「为什么不改成在 tail
// 路径上去重」的论证。
func TestDeliveryRetryAfterPartialInsertIsNotTailFastPath(t *testing.T) {
	inner := &reconcileVLFixture{}
	gate := &insertGateVL{inner: inner}
	server := httptest.NewServer(gate)
	t.Cleanup(server.Close)
	client, clientErr := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, clientErr)

	root := t.TempDir()
	dir := filepath.Join(root, "s00")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "latest.log")
	// 600 行 > insertBatchMaxEvents(500) ⇒ 一次投递会拆成 2 个插入请求，
	// 才能造出「前几批成功、后续批失败」的部分写入。
	require.NoError(t, os.WriteFile(path, []byte(pollTestLines(0, 600)), 0o600))

	src := SourceConfig{
		LogSourceID:      "node:tail-partial",
		SourceGeneration: "g1",
		Path:             path,
		Mode:             pipeline.ModeFilePrimary,
		StorageNamespace: "ns:tail-partial",
		UTCDay:           runtimeTestUTCDay(),
	}
	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{src}})
	require.NoError(t, err)

	// 第 1 轮：正常投递（尾路径），建立段与水位。
	m.pollOnce()
	key := src.LogSourceID + "/" + src.SourceGeneration
	require.Positive(t, inner.rowCount(), "前置：首轮必须已写入投影")

	// 第 2 轮：追加一批，并让插入从第 2 个请求起失败 ⇒ 部分写入 + 投递报错。
	appendPollTestLines(t, path, pollTestLines(600, 600))
	gate.arm(1)
	m.pollOnce()
	gate.disarm()

	saved := savedSource(t, m, key)
	require.True(t, saved.PublicationPending,
		"部分写入失败后必须留下 pending=true（这是重投不走尾路径的唯一依据）")
	require.NotEmpty(t, saved.ProjectionGeneration)

	// 构造「整体在水位之后」的一批：若只看水位，它本应走尾路径。
	freshStart := saved.EventsStoredThrough
	if p, ok := m.pipeFor(key); ok {
		if positions, ok := p.Positions(); ok && positions.Delivery > freshStart {
			freshStart = positions.Delivery
		}
	}
	freshStart++
	fresh := []logtypes.Event{logtypes.BuildEvent(
		logtypes.SourceIdentity{
			LogSourceID: src.LogSourceID, SourceGeneration: src.SourceGeneration, ParserVersion: "v1",
		},
		logtypes.RecordRange{Start: freshStart, End: freshStart + 1},
		"2026-10-21T12:30:00Z", "2026-10-21T12:30:01Z", "INFO", "stdout", "fresh tail candidate",
	)}

	require.False(t, m.planDeliveryTail(key, saved, fresh, false).Fast,
		"pending=true 时必须拒绝尾快路径（删掉 planDeliveryTail 里的 PublicationPending 判据即转红）")

	confirmed := saved
	confirmed.PublicationPending = false
	require.True(t, m.planDeliveryTail(key, confirmed, fresh, false).Fast,
		"对照：同一批事件在 pending 清除后必须走尾快路径，否则上面的断言没有区分力")
}
