package stateindex

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestIndexMaterializationCompactionReserveAndLegacyClassThreeOpens(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	limits := MaterializationLimits{WindowCopies: 2, HourlyCopies: 2, HourlyBytes: 100, CompactionReserveBytes: 25}
	req := MaterializationRequest{SourceKey: "reserved/g1", UTCDay: "2026-10-22", WindowKey: "content", Generation: "rebuild-1", EstimatedBytes: 75, Now: now}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if row, allowed, _, err := s.ReserveMaterialization(ctx, req, limits); err != nil || !allowed || row.Class != "rebuild" {
		t.Fatalf("默认类别预留失败：%+v %v %v", row, allowed, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// 真实旧台账无类别列，重开必须补列并把既有计费归为后台重建。
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec("ALTER TABLE materialization_attempt DROP COLUMN class"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 3; round++ {
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		req.Generation, req.EstimatedBytes, req.Class = "rebuild-2", 1, "rebuild"
		if _, allowed, _, err := s.ReserveMaterialization(ctx, req, limits); err != nil || allowed {
			t.Fatalf("第%d次重开重建侵占必要压缩保留：%v %v", round+1, allowed, err)
		}
		if round == 0 {
			req.Generation, req.EstimatedBytes, req.Class = "compaction-1", 25, "live_compaction"
			if row, allowed, _, err := s.ReserveMaterialization(ctx, req, limits); err != nil || !allowed || row.Class != "live_compaction" {
				t.Fatalf("必要压缩未得到保留额：%+v %v %v", row, allowed, err)
			}
		}
		req.Generation, req.EstimatedBytes, req.Class = "compaction-2", 1, "live_compaction"
		if _, allowed, _, err := s.ReserveMaterialization(ctx, req, limits); err != nil || allowed {
			t.Fatalf("压缩绕过源日总字节硬顶：%v %v", allowed, err)
		}
		rows, err := s.ListMaterializations(ctx, req.SourceKey, req.UTCDay, req.WindowKey, 0, 10)
		if err != nil || len(rows) != 2 || rows[0].Class != "rebuild" || rows[1].Class != "live_compaction" {
			t.Fatalf("第%d次重开类别/历史计费丢失：%+v %v", round+1, rows, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, reserve := range []int64{-1, 101, math.MaxInt64} {
		bad := limits
		bad.CompactionReserveBytes = reserve
		if _, allowed, _, err := s.ReserveMaterialization(ctx, req, bad); err == nil || allowed {
			t.Fatalf("非法保留额未拒绝：%d", reserve)
		}
	}
	req.Class = "伪造类别"
	if _, allowed, _, err := s.ReserveMaterialization(ctx, req, limits); err == nil || allowed {
		t.Fatal("不在白名单的物化类别获得预算")
	}
	req.Class, req.SourceKey, req.EstimatedBytes = "live_compaction", "overflow/g1", math.MaxInt64
	large := MaterializationLimits{WindowCopies: 2, HourlyCopies: 2, HourlyBytes: math.MaxInt64, CompactionReserveBytes: math.MaxInt64 / 4}
	if _, allowed, _, err := s.ReserveMaterialization(ctx, req, large); err != nil || !allowed {
		t.Fatalf("合法最大整数预算被错误拒绝：%v %v", allowed, err)
	}
	req.Generation, req.EstimatedBytes = "overflow-next", 1
	if _, allowed, _, err := s.ReserveMaterialization(ctx, req, large); err != nil || allowed {
		t.Fatalf("整数溢出绕过总字节硬顶：%v %v", allowed, err)
	}
}

func TestIndexRecoveryAuditIndependentIdempotentThreeOpens(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	const source = "audit-source/g1"
	for round := 0; round < 3; round++ {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if round == 0 {
			for start := 0; start < 1000; start += 16 {
				var rows []RecoveryAuditRow
				for i := start; i < min(start+16, 1000); i++ {
					rows = append(rows, RecoveryAuditRow{SourceKey: source, RefKey: fmt.Sprintf("ref-%04d", i), Fingerprint: fmt.Sprintf("fp-%d", i), Payload: []byte(fmt.Sprintf(`{"ref":%d}`, i))})
				}
				if _, err := s.AppendRecoveryAudit(ctx, rows); err != nil {
					t.Fatal(err)
				}
			}
		}
		var cursor string
		count := 0
		for {
			rows, err := s.ListRecoveryAudit(ctx, source, cursor, 37)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) == 0 {
				break
			}
			count += len(rows)
			cursor = rows[len(rows)-1].RefKey
		}
		if count != 1000 {
			t.Fatalf("第%d次重开审计不全：%d", round+1, count)
		}
		row, found, err := s.LookupRecoveryAudit(ctx, source, "ref-0042")
		if err != nil || !found {
			t.Fatalf("审计查找：%v %v", found, err)
		}
		receipts, err := s.AppendRecoveryAudit(ctx, []RecoveryAuditRow{row})
		if err != nil || len(receipts) != 1 || receipts[0].ArchiveID != row.ArchiveID {
			t.Fatalf("幂等回执改变：%+v %v", receipts, err)
		}
		row.Payload = []byte("篡改正文")
		if _, err := s.AppendRecoveryAudit(ctx, []RecoveryAuditRow{row}); err == nil {
			t.Fatal("身份冲突不得覆盖旧审计")
		}
		state, err := s.Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Aux) != 0 || len(state.Sources) != 0 || len(s.mirror) != tableCount {
			t.Fatalf("历史不得进入活动状态或镜像：%+v", state)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIndexRecoveryAuditCanceledUnitReturnsNoReceipt(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	s.testCrashHook = cancel
	rows := []RecoveryAuditRow{{SourceKey: "audit/g1", RefKey: "ref", Fingerprint: "fp", Payload: []byte("完整审计")}}
	receipts, err := s.AppendRecoveryAudit(ctx, rows)
	s.testCrashHook = nil
	if err == nil || len(receipts) != 0 {
		t.Fatalf("取消事务不能返回持久回执：%+v %v", receipts, err)
	}
	if _, found, err := s.LookupRecoveryAudit(context.Background(), "audit/g1", "ref"); err != nil || found {
		t.Fatalf("未提交审计残留：%v %v", found, err)
	}
	if _, err := s.AppendRecoveryAudit(context.Background(), rows); err != nil {
		t.Fatalf("取消后连接未回滚：%v", err)
	}
}

func TestIndexMaterializationReservationSurvivesThreeOpens(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	limits := MaterializationLimits{WindowCopies: 2, HourlyCopies: 3, HourlyBytes: 1000}
	request := MaterializationRequest{SourceKey: "budget/g1", UTCDay: "2026-10-22", WindowKey: "stable-a", Generation: "candidate-a", EstimatedBytes: 250, Now: now}
	var id int64
	for round := 0; round < 3; round++ {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if round == 0 {
			reservation, allowed, _, err := s.ReserveMaterialization(ctx, request, limits)
			if err != nil || !allowed {
				t.Fatalf("写前预留失败：%+v %v %v", reservation, allowed, err)
			}
			id = reservation.ID
			if err := s.UpdateMaterialization(ctx, id, MaterializationUpdate{State: "unknown", Generation: "candidate-a", Detail: "发送超时，实际结果未知"}); err != nil {
				t.Fatal(err)
			}
			request.WindowKey, request.Generation, request.EstimatedBytes = "stable-b", "candidate-b", 500
			if _, allowed, _, err := s.ReserveMaterialization(ctx, request, limits); err != nil || !allowed {
				t.Fatalf("第二窗口预留：%v %v", allowed, err)
			}
			request.WindowKey, request.Generation, request.EstimatedBytes = "stable-c", "candidate-c", 300
			if _, allowed, retry, err := s.ReserveMaterialization(ctx, request, limits); err != nil || allowed || !retry.Equal(now.Add(time.Hour)) {
				t.Fatalf("新内容窗口绕过小时字节门：%v %v %v", allowed, retry, err)
			}
			if err := s.UpdateMaterialization(ctx, id, MaterializationUpdate{State: "written", WrittenBytes: 400}); err != nil {
				t.Fatal(err)
			}
			if err := s.UpdateMaterialization(ctx, id, MaterializationUpdate{State: "published"}); err == nil {
				t.Fatal("未验证不得记录成功发布")
			}
			if err := s.UpdateMaterialization(ctx, id, MaterializationUpdate{State: "verified", Verified: true}); err != nil {
				t.Fatal(err)
			}
			if err := s.UpdateMaterialization(ctx, id, MaterializationUpdate{State: "published", Published: true}); err != nil {
				t.Fatal(err)
			}
			request.EstimatedBytes = 100
			if _, allowed, _, err := s.ReserveMaterialization(ctx, request, limits); err != nil || !allowed {
				t.Fatalf("剩余字节预算未生效：%v %v", allowed, err)
			}
		}
		rows, err := s.ListMaterializations(ctx, "budget/g1", "2026-10-22", "stable-a", 0, 5)
		if err != nil || len(rows) != 1 || rows[0].ID != id || rows[0].State != "published" || rows[0].WrittenBytes != 400 || !rows[0].Verified || !rows[0].Published {
			t.Fatalf("第%d次重开丢失候选/写入/发布证据：%+v %v", round+1, rows, err)
		}
		request.WindowKey, request.Generation, request.EstimatedBytes = "new-content", fmt.Sprintf("new-%d", round), 1
		if _, allowed, _, err := s.ReserveMaterialization(ctx, request, limits); err != nil || allowed {
			t.Fatalf("重启或换窗口绕过小时次数门：%v %v", allowed, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	request.Now = now.Add(time.Hour)
	request.WindowKey, request.Generation = "stable-a", "candidate-a2"
	if _, allowed, _, err := s.ReserveMaterialization(ctx, request, limits); err != nil || !allowed {
		t.Fatalf("小时窗口释放后无法使用剩余副本：%v %v", allowed, err)
	}
	request.Generation = "candidate-a3"
	if _, allowed, retry, err := s.ReserveMaterialization(ctx, request, limits); err != nil || allowed || !retry.IsZero() {
		t.Fatalf("同内容累计副本门被重启/跨小时重置：%v %v %v", allowed, retry, err)
	}
	request.Generation = "candidate-a2"
	if _, allowed, _, err := s.ReserveMaterialization(ctx, request, limits); err != nil || allowed {
		t.Fatalf("同候选重复获得物理写许可：%v %v", allowed, err)
	}
}
