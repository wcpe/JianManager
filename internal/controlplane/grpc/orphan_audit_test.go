package grpc

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// TestTruncateUTF8_RuneBoundary 锁定 truncateUTF8 的截断语义（CP-15 复核）：
// 结果必须是「不超过 max 字节的**最大**合法 UTF-8 rune 前缀」——既不切断多字节字符，
// 也不在还能整 rune 保留时少留（off-by-one 会在此暴露）。
func TestTruncateUTF8_RuneBoundary(t *testing.T) {
	inputs := []string{"", "a", "中", "ab", "a中", "中a", "中文", "中文X", "a中文b", "🙂x", "a🙂", "🙂🙂"}
	for _, s := range inputs {
		for max := 0; max <= len(s)+2; max++ {
			got := truncateUTF8(s, max)
			if max <= 0 || len(s) <= max {
				if got != s {
					t.Errorf("s=%q max=%d: 应原样返回，得到 %q", s, max, got)
				}
				continue
			}
			if len(got) > max {
				t.Errorf("s=%q max=%d: 结果 %q 超过 %d 字节", s, max, got, max)
			}
			if !utf8.ValidString(got) {
				t.Errorf("s=%q max=%d: 结果 %q 非法 UTF-8", s, max, got)
			}
			// 最大化判据：结果应是「不超过 max 字节的最大 rune 前缀」
			best := ""
			for i := 0; i <= len(s); i++ {
				if i <= max && utf8.ValidString(s[:i]) {
					best = s[:i]
				}
			}
			if got != best {
				t.Errorf("s=%q max=%d: 得到 %q，期望最大合法前缀 %q", s, max, got, best)
			}
		}
	}
	// 长串抽查（中英数 emoji 混合），防「只在短串正确」的偶然通过。
	long := strings.Repeat("中文abc🙂", 100)
	for max := 1; max <= 40; max++ {
		got := truncateUTF8(long, max)
		best := ""
		for i := 0; i <= len(long); i++ {
			if i <= max && utf8.ValidString(long[:i]) {
				best = long[:i]
			}
		}
		if got != best {
			t.Errorf("long max=%d: 得到 %d 字节，期望 %d 字节", max, len(got), len(best))
		}
	}
}

// TestOrphanAuditAllowedActions_Contract 锁定审计白名单契约（CP-3）：
// 本表是强校验（不在表内一律拒绝），新增动作必须同时更新本表、spec 与 proto 注释。
// 本用例守护两条不变量：
//  1. 已知的系统级动作（含 ADR-097 的 cgroup.isolate_failed）必须在表内；
//  2. 表内动作名不超过 maxOrphanAuditActionLen（纵深防御依赖该上限）。
func TestOrphanAuditAllowedActions_Contract(t *testing.T) {
	// Worker 侧会发送的系统动作：缺一则该动作的审计被 CP 拒收后静默丢弃（CP-3）。
	mustAllow := []string{
		"orphan.scan_detected",
		"orphan.scan_disposed",
		"orphan.scan_dispose_blocked",
		"orphan.dispose_blocked",
		"orphan.dispose_reaped",
		"orphan.auto_adopted",
		"orphan.adopt_blocked",
		"orphan.adopt_failed",
		"orphan.startup_detected_not_reaped",
		"orphan.foreign_runtime_adopt_blocked",
		"orphan.foreign_runtime_adopted",
		"orphan.foreign_runtime_detected",
		"health.dead_detected",
		"health.selfheal_restart",
		"health.selfheal_exhausted",
		"health.circuit_broken",
		"health.circuit_released",
		// ADR-097「无隔离保护必须成为可观测事实」的唯一 CP 侧留痕通道。
		"cgroup.isolate_failed",
	}
	for _, action := range mustAllow {
		if _, ok := orphanAuditAllowedActions[action]; !ok {
			t.Errorf("动作 %q 不在 CP 审计白名单内，Worker 侧上报会被拒收后静默丢弃", action)
		}
	}
	for action := range orphanAuditAllowedActions {
		if len(action) > maxOrphanAuditActionLen {
			t.Errorf("白名单动作 %q 超过长度上限 %d", action, maxOrphanAuditActionLen)
		}
	}
}

// TestReportOrphanAudit_AllowsCgroupIsolateFailed 端到端守护 CP-3：
// cgroup.isolate_failed 必须能被 ReportOrphanAudit 接受（不在白名单时返回 InvalidArgument）。
func TestReportOrphanAudit_AllowsCgroupIsolateFailed(t *testing.T) {
	dsn := "file:orp_audit_cgroup?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	node := &model.Node{UUID: "n-cgroup", Secret: "s", Name: "n", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)

	h := NewControlPlaneHandler(db, NewClientPool())
	// 未装配审计落库器时确认收到即返回成功——此时 action 已过白名单校验。
	_, err = h.ReportOrphanAudit(context.Background(), &workerpb.ReportOrphanAuditRequest{
		NodeUuid:   "n-cgroup",
		NodeSecret: "s",
		Action:     "cgroup.isolate_failed",
		TargetId:   "/var/lib/jianmanager-daemons/u1",
		Detail:     `{"reason":"非特权用户无从迁移 cgroup"}`,
		Success:    false,
	})
	if err != nil {
		t.Fatalf("cgroup.isolate_failed 应被接受，得到错误: %v", err)
	}
}
