package acquire

// 段 3 红绿（w18 漂移防护）：**重启后**的外部保头重写必须被续读前指纹校验拦住——
// 既有 tailSHA256 检测依赖进程内尾窗指纹，重启后为空 ⇒ 对这一态结构性看不见 ✗。
//
// 期望行为（三态之②）：
//   ① 不得按旧偏移回读历史内容（重启后首轮事件为空 ✓）；
//   ② 必须留下**可见**缺口（FILE_REWRITTEN，供后面对账补齐 ✓，绝不静默 ✗）；
//   ③ 从新锚续读：此后追加的行照常读到 ✓。
//
// 变异（人工验证过）：把 `!t.confirmedFingerprintChecked && t.cursor > 0` 改成 `false`
// ⇒ 无缺口、静默跳过 ⇒ 本用例红；原样装回 ⇒ 绿。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func TestResumeFingerprintMismatchReanchorsWithoutReRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	key := testKey("src-resume", "g1")

	lines := make([]string, 0, 30)
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("[00:00:%02d] resume line-%02d %s", i, i, strings.Repeat("pad ", 40)))
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	led := ledger.New()
	wal := NewWAL(led, key)
	first := NewFileTailer(led, key, path, wal, NewLineHook())
	events, err := first.Poll()
	require.NoError(t, err)
	require.Len(t, events, 30)
	require.NoError(t, wal.Append(events...))
	require.NoError(t, wal.Commit())
	_ = led.RecordDelivery(key, 0, first.LogicalPos(), logtypes.DeliveryRequestDone)
	_, err = first.Poll()
	require.NoError(t, err)
	at, fingerprint, ok := led.PositionsFingerprint(key)
	require.True(t, ok, "前置：确认水位后应已写入指纹（段 2）")
	require.NotZero(t, at)
	require.NotEmpty(t, fingerprint)

	// 外部保头重写：**只改已确认水位前 512B 窗口的内容**，文件长度不变、首 512B（身份前缀）不动
	// ⇒ 尺寸/身份/轮转判据全部看不见，只有指纹看得见 ✗。
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	require.NoError(t, err)
	from := int64(at) - 512
	require.Greater(t, from, int64(512), "窗口应落在身份前缀之后，确保是'保头'重写")
	_, err = f.WriteAt([]byte(strings.Repeat("X", 96)), from)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// "重启"：同一账本、同一文件，新建 tailer（游标与水位来自账本/段，不来自进程内存）。
	restarted := NewFileTailer(led, key, path, wal, NewLineHook())
	resumed, err := restarted.Poll()
	require.NoError(t, err)
	require.Empty(t, resumed, "重启后不得按旧偏移回读历史内容 ✗")
	require.Greater(t, led.UnresolvedGapCount(key), 0, "必须留下可见缺口，交给既有对账补齐 ✓（绝不静默 ✗）")

	// 从新锚续读：此后追加的行照常读到 ✓（锚在 EOF，不丢后续数据）。
	af, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = af.WriteString("[00:00:31] resume line-31 after reanchor\n")
	require.NoError(t, err)
	require.NoError(t, af.Close())
	after, err := restarted.Poll()
	require.NoError(t, err)
	require.Len(t, after, 1, "重锚后续读不得丢行 ✓")
}
