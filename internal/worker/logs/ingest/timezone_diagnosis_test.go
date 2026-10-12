package ingest

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 2026-10-02 真机复验的连带交付：节点级默认时区必须**运行期可自证**。
//
// 现场：`log_ingest.time_zone: local` 配上并重启后，新入库条目仍是 +8h。逐层核对（接线、
// 恢复路径、归一化链路）都是对的——因为 `local` 解析的是 **Worker 进程**的本地时区，
// 而容器（alpine 基础镜像无 TZ/无 /etc/localtime）与部分 systemd 单元（`Environment=TZ=`）里
// 它恰好等于 UTC：配了 local 等于什么都没配，且**完全没有任何日志能看出这一点**。
//
// 本文件的用例：
//  1. TestDefaultTimeZoneHintFlagsLocalResolvingToUTC —— local 解析成 UTC 必须给出可判定的提示；
//  2. TestDefaultTimeZoneHintStaysSilentForExplicitConfig —— 显式 IANA 名/UTC 与真正的非 UTC
//     local 不得告警（避免把正常配置变成噪音）。

func TestDefaultTimeZoneHintFlagsLocalResolvingToUTC(t *testing.T) {
	hint := DefaultTimeZoneHint("local", time.UTC)
	require.NotEmpty(t, hint, "local 解析为 UTC 时必须给出提示：这正是真机复验踩到的形态")
	require.Contains(t, hint, "UTC+00:00")
	require.Contains(t, hint, "Asia/Hong_Kong", "提示必须给出可执行的替代（显式 IANA 名）")
	// 提示里要能看到判定依据（TZ / /etc/localtime），否则运维仍要自己猜为什么是 UTC。
	require.Contains(t, hint, "TZ=")
	require.Contains(t, hint, "/etc/localtime")

	// 别名与大小写形态同样覆盖（ParseTimeZone 接受 node/host/LOCAL）。
	for _, alias := range []string{"LOCAL", "node", "host", " local "} {
		require.NotEmpty(t, DefaultTimeZoneHint(alias, time.UTC), "别名 %q 同样要判定", alias)
	}
}

func TestDefaultTimeZoneHintStaysSilentForExplicitConfig(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)

	// 真正的非 UTC local（裸机 /etc/localtime 或 TZ 指向 +08:00）：不告警。
	require.Empty(t, DefaultTimeZoneHint("local", shanghai),
		"local 解析到非 UTC 时必须静默：那正是运维配 local 的本意")
	// 显式 IANA 名与显式 UTC：与 local 无关，不告警。
	require.Empty(t, DefaultTimeZoneHint("Asia/Hong_Kong", shanghai))
	require.Empty(t, DefaultTimeZoneHint("utc", time.UTC))
	require.Empty(t, DefaultTimeZoneHint("", time.UTC), "未配置（默认 UTC）不是配置错误")
	require.Empty(t, DefaultTimeZoneHint("local", nil))
}

// TestParseTimeZoneAliases 守住取值口径本身（提示逻辑依赖它把 local 走 time.Local）。
func TestParseTimeZoneAliases(t *testing.T) {
	for _, raw := range []string{"local", "LOCAL", "node", "host", " local "} {
		loc, err := ParseTimeZone(raw)
		require.NoError(t, err, "%q 必须可解析", raw)
		require.Equal(t, time.Local, loc, "%q 必须解析为进程本地时区", raw)
	}
	require.Equal(t, time.UTC, mustParseZone(t, ""))
	require.Equal(t, time.UTC, mustParseZone(t, "utc"))
	require.Equal(t, time.UTC, mustParseZone(t, "z"))

	loc := mustParseZone(t, "Asia/Hong_Kong")
	_, offset := time.Now().In(loc).Zone()
	require.Equal(t, 8*3600, offset, "IANA 名必须解析成对应偏移（香港无夏令时）")
	require.False(t, strings.Contains(loc.String(), "UTC"), "解析结果不得退化成 UTC：%s", loc)
}

func mustParseZone(t *testing.T, raw string) *time.Location {
	t.Helper()
	loc, err := ParseTimeZone(raw)
	require.NoError(t, err, "时区 %q 必须可解析", raw)
	return loc
}
