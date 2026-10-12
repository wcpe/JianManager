package ingest

import (
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStartupRecoveryFailureLevelSplitsDeferral（现场铁证驱动的红证 ✓✓，用户 2026-10-05）：
// **延后不打 ERROR**（设计内让路 ✓）、**真失败照旧 ERROR**（不一刀切 ✓）。
//
// 现场依据：新二进制部署后 `inst:142/143` 仍持续打 ERROR（08:22:49 ✓）——发射点正是
// [runtime.go:1729](internal/worker/logs/ingest/runtime.go#L1729) 那条**无条件** `slog.Error`，
// 而它携带的 `err` 里有 `%w` 包装的 `ErrVerifyDeferred` ✓（我此前"三搜"把它误判成"≠投递/校验面" ✗）。
//
// 转红方式（实测，**有效变异** ✓：先验编译）：去掉 `errors.Is(err, ErrVerifyDeferred)` 分支
// （恒 ERROR ✗）⇒ 本用例在「延期 ⇒ WARN」处红 ✓。
func TestStartupRecoveryFailureLevelSplitsDeferral(t *testing.T) {
	// 延期（含 %w 包装 ✓，与生产同形 ✓）⇒ WARN，且文案点明"设计内让路" ✓。
	lvl, msg := startupRecoveryFailureLevel(fmt.Errorf("ingest: projection inst:142/g1 %w (round budget exhausted)", ErrVerifyDeferred))
	require.Equal(t, slog.LevelWarn, lvl, "延期必须降级为 WARN（现场 ERROR 的根因 ✗），文案=%q", msg)
	require.NotEqual(t, slog.LevelError, lvl)

	// 真失败 ⇒ 照旧 ERROR ✓（一刀切降级会把真问题淹没 ✗）。
	lvl2, msg2 := startupRecoveryFailureLevel(errors.New("boom: 索引打开失败"))
	require.Equal(t, slog.LevelError, lvl2, "真失败必须保持 ERROR（不许一刀切 ✗），文案=%q", msg2)
}
