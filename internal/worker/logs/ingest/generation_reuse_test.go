package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 回归：物理代次名在 VL 中已被占用时必须进位，绝不复用（2026-10-01 生产事故）。
//
// 现场：回滚脚本把归档的旧 JSON 改回后，代次计数器从 87 重算 → 重放写 projection-88；而 VL 里
// 已有当天 03:22 那一轮**同名整窗重发**留下的行（VL 只追加不删除，「取代」只改查询侧白名单，
// 物理行保留到 retention 到期）→ 校验查询把旧行读成本次写入 → `unexpected or duplicate
// projection event_id` → 校验永不通过 → 运行时创建失败、采集停摆；三次重试都写同一个名字，
// 越写越脏，最终只能靠 VL 空库重建恢复。
//
// 本用例把那份「上一轮生命周期的同名行」按原样注入假 VL（同代次、同源、同时间窗、同事件体），
// 然后断言第二次启动的重放自动进位到未占用的新名字并成功。
// 改回旧的 `nextProjectionGeneration`（不做占用探测）即转红：重放写同名代次，校验撞上注入行。
func TestRecoveryAvoidsProjectionGenerationAlreadyInVL(t *testing.T) {
	client, fixture := newProjectionVL(t)

	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte(
		"[12:00:01] [Server thread/INFO]: hello\n[12:00:02] [Server thread/INFO]: next\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "node:1", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay(),
	}
	opts := Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source}}
	key := source.LogSourceID + "/" + source.SourceGeneration

	first, err := newTestManager(t, opts)
	require.NoError(t, err)
	first.pollOnce()
	require.NoError(t, first.Stop())
	require.NotEmpty(t, fixture.writes, "首启应产生投影写入")

	// 现场注入：把「当前代次」的那一行原样复制一份、只改代次名为「下一次重放本会选用的名字」
	// ——这正是「上一轮生命周期」在 VL 中留下的同名物理行。
	current := savedSource(t, first, key).ProjectionGeneration
	require.NotEmpty(t, current)
	template := ""
	for index := len(fixture.writes) - 1; index >= 0; index-- {
		if strings.Contains(string(fixture.writes[index]), `"projection_generation":"`+current+`"`) {
			template = string(fixture.writes[index])
			break
		}
	}
	require.NotEmpty(t, template, "应能找到当前代次的投影行")
	stale := strings.Replace(template,
		`"projection_generation":"`+current+`"`,
		`"projection_generation":`+strconv.Quote(nextProjectionGeneration(current)), 1)
	require.NotEqual(t, template, stale, "注入行必须改掉代次名")
	fixture.appendWrites([]byte(stale))

	second, err := newTestManager(t, opts)
	require.NoError(t, err, "重放不得复用 VL 中已存在的物理代次名（旧行会被读成本次写入）")
	require.NoError(t, second.Stop())

	writes := len(fixture.writes)
	replayGeneration := projectionGenerationOf(t, fixture.writes[writes-1])
	require.Greater(t, replayGeneration, maxProjectionGenerationOf(t, fixture.writes[:writes-1]),
		"重放必须写到比所有历史代次（含注入的陈旧同名行）都新的名字")
	require.Equal(t, fmt.Sprintf("projection-%d", replayGeneration),
		savedSource(t, second, key).ProjectionGeneration,
		"进位后的代次名必须落库（供后续启动继续向上走）")
}

// appendWrites 直接向假 VL 追加一行（模拟 VL 里既有的物理行）。
func (f *projectionVLFixture) appendWrites(payload []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, payload)
}

// projectionGenerationOf 取写入负载里的物理代次号（测试断言用）。
func projectionGenerationOf(t *testing.T, payload []byte) int {
	t.Helper()
	const prefix = `"projection_generation":"projection-`
	index := strings.Index(string(payload), prefix)
	require.GreaterOrEqual(t, index, 0, "写入负载缺少投影代次：%s", string(payload))
	rest := string(payload)[index+len(prefix):]
	value, err := strconv.Atoi(rest[:strings.Index(rest, `"`)])
	require.NoError(t, err)
	return value
}

// maxProjectionGenerationOf 取一组写入负载里最大的物理代次号。
func maxProjectionGenerationOf(t *testing.T, payloads [][]byte) int {
	t.Helper()
	maximum := 0
	for _, payload := range payloads {
		if generation := projectionGenerationOf(t, payload); generation > maximum {
			maximum = generation
		}
	}
	return maximum
}
