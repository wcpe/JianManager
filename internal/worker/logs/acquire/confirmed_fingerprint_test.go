package acquire

// 段 2 定向（w18 漂移防护）：已确认水位（投递线）处的内容指纹必须**成对**持久化，且随水位前进重采样——
// 旧指纹绝不允许挂在新位置上（否则段 3 的"一致"判定会放过一次真实的漂移 ✗）。
//
// 采样点挂在「已确认水位」而不是「当前游标」：游标总在 EOF（未确认区），对恢复续读没有判别力 ✗。

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

func TestConfirmedFingerprintTracksDeliveryWatermark(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	key := testKey("src-fp", "g1")

	lines := make([]string, 0, 30)
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("[00:00:%02d] fingerprint line-%02d %s", i, i, strings.Repeat("pad ", 40)))
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	led := ledger.New()
	wal := NewWAL(led, key)
	tailer := NewFileTailer(led, key, path, wal, NewLineHook())

	events, err := tailer.Poll()
	require.NoError(t, err)
	require.Len(t, events, 30)

	// 尚无投递确认（投递线为 0）⇒ 无凭证：段 3 视作不一致，宁可走对账不回读 ✓。
	_, _, confirmed := led.PositionsFingerprint(key)
	require.False(t, confirmed, "未确认水位时不得写指纹")

	// 确认投递到本批末尾 ⇒ 下一次 Poll 必须在该水位采样并成对写入。
	require.NoError(t, wal.Append(events...))
	require.NoError(t, wal.Commit())
	_ = led.RecordDelivery(key, 0, tailer.LogicalPos(), logtypes.DeliveryRequestDone)
	positions, okPositions := led.Positions(key)
	require.True(t, okPositions)
	require.NotZero(t, positions.Delivery, "本用例需要非零投递水位")
	_, err = tailer.Poll()
	require.NoError(t, err)
	at, fingerprint, ok := led.PositionsFingerprint(key)
	require.True(t, ok, "确认水位后必须写入指纹")
	require.Equal(t, positions.Delivery, at, "指纹必须挂在当前已确认（投递）水位上")
	require.NotEmpty(t, fingerprint)

	// 幂等：水位未前进 ⇒ 不重采样（指纹原样保留）。
	_, err = tailer.Poll()
	require.NoError(t, err)
	atAgain, fingerprintAgain, ok := led.PositionsFingerprint(key)
	require.True(t, ok)
	require.Equal(t, at, atAgain)
	require.Equal(t, fingerprint, fingerprintAgain)

	// 追加一行并确认（水位前进）⇒ 指纹必须在**新位置**重采样，旧指纹不得残留。
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("[00:00:31] fingerprint line-31 appended after watermark\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	more, err := tailer.Poll()
	require.NoError(t, err)
	require.Len(t, more, 1)
	require.NoError(t, wal.Append(more...))
	require.NoError(t, wal.Commit())
	end2 := tailer.LogicalPos()
	require.Greater(t, end2, uint64(at), "追加后逻辑位置必须前进")
	_ = led.RecordDelivery(key, uint64(at), end2, logtypes.DeliveryRequestDone)
	positions2, okPositions2 := led.Positions(key)
	require.True(t, okPositions2)
	require.Greater(t, positions2.Delivery, uint64(at), "已确认水位必须前进")
	_, err = tailer.Poll()
	require.NoError(t, err)
	at2, fingerprint2, ok := led.PositionsFingerprint(key)
	require.True(t, ok)
	require.Equal(t, positions2.Delivery, at2, "水位前进后指纹必须挂在新位置（旧指纹不得残留）✗")
	require.NotEqual(t, fingerprint, fingerprint2, "内容窗口随位置前移，指纹必然变化（旧指纹失效重采样）✗")
}
