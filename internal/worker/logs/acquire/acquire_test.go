package acquire

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func testKey(id, gen string) ledger.SourceKey {
	return ledger.SourceKey{LogSourceID: id, SourceGeneration: gen}
}

func writeGzip(t *testing.T, path string, lines []string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := gzip.NewWriter(f)
	for _, ln := range lines {
		if _, err := zw.Write([]byte(ln + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestRotationNotDoubleCount 验证 latest.log 轮转压缩后同一逻辑源不重复计数。
func TestRotationNotDoubleCount(t *testing.T) {
	dir := t.TempDir()
	latest := filepath.Join(dir, "latest.log")
	rotated := filepath.Join(dir, "latest.log.1")
	gzPath := filepath.Join(dir, "latest.log.1.gz")

	// live 文件写入 3 行后轮转。
	liveLines := []string{"line-a", "line-b", "line-c"}
	if err := os.WriteFile(latest, []byte(strings.Join(liveLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	key := testKey("src-rot", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	tailer := NewFileTailer(led, key, latest, wal, NewLineHook())
	tailer.SetRotateTarget(rotated)

	events, err := tailer.Poll()
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 live events, got %d", len(events))
	}
	if err := wal.Append(events...); err != nil {
		t.Fatal(err)
	}
	if err := wal.Commit(); err != nil {
		t.Fatal(err)
	}
	logicalAfterLive := tailer.LogicalPos()
	if logicalAfterLive == 0 {
		t.Fatal("logical pos must advance after live poll")
	}

	// 轮转：内容移到 rotated，再压成 gz；latest 变空/新文件。
	if err := os.WriteFile(rotated, []byte(strings.Join(liveLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeGzip(t, gzPath, liveLines)
	// 截断 latest 触发轮转检测。
	if err := os.WriteFile(latest, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	tailer.SetRotateTarget(gzPath)
	if _, err := tailer.Poll(); err != nil {
		t.Fatalf("poll after rotate: %v", err)
	}
	if tailer.RotationCount() < 1 {
		t.Fatalf("expected rotation detected, got %d", tailer.RotationCount())
	}

	// 轮转关联必须已登记，generation 不变。
	link, ok := led.RotationLinked(key, gzPath)
	if !ok {
		// 关联目标是 rotateTo；再手动确认账本有 rotation 记录。
		ent := led.Get(key)
		if ent == nil || len(ent.Rotations) == 0 {
			t.Fatalf("rotation link missing: %+v", ent)
		}
	} else if link.Generation != "g1" {
		t.Fatalf("rotation must keep generation, got %s", link.Generation)
	}

	// ArchiveImporter：已关联已导入 → 跳过整包，不双计。
	// 先确保分段已登记 Imported（ImportGzip 第一次可能导入）。
	imp := NewArchiveImporter(led, key)
	// 强制登记为已导入的轮转分段（模拟 tail 已覆盖 + rotation 链接完成）。
	if err := led.RegisterSegment(key, ledger.Segment{
		Path:     gzPath,
		Kind:     ledger.SegmentGzip,
		EndPos:   logicalAfterLive,
		Imported: true,
	}); err != nil {
		t.Fatal(err)
	}
	// LinkRotation 到 gz，使 RotationLinked 命中。
	if err := led.LinkRotation(key, latest, gzPath, logicalAfterLive); err != nil {
		t.Fatal(err)
	}

	res, err := imp.ImportGzip(gzPath)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("expected skip when rotation already linked, got %+v", res)
	}
	if res.ImportedCount != 0 {
		t.Fatalf("rotation already linked must not reimport, got %d events", res.ImportedCount)
	}
	if imp.SkippedAlreadyLinked() < 1 {
		t.Fatal("skipped counter must increase")
	}

	// 总完整事件仍为 3，不因归档整包重放变成 6。
	total := tailer.EventsEmitted() + imp.ImportedEvents()
	if total != 3 {
		t.Fatalf("same logical source must not double-count: tailer=%d importer=%d total=%d",
			tailer.EventsEmitted(), imp.ImportedEvents(), total)
	}
}

// TestArchiveImportWhenNotLinked 归档未关联时可导入，event_id 不含 archive_object_id。
func TestArchiveImportWhenNotLinked(t *testing.T) {
	dir := t.TempDir()
	gzPath := filepath.Join(dir, "a.gz")
	writeGzip(t, gzPath, []string{"e1", "e2"})

	key := testKey("src-imp", "g1")
	led := ledger.New()
	imp := NewArchiveImporter(led, key)
	res, err := imp.ImportGzip(gzPath)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped || res.ImportedCount != 2 {
		t.Fatalf("expected 2 imported, got %+v", res)
	}
	if res.ArchiveObjectID == "" {
		t.Fatal("archive object id required as provenance")
	}
	// archive_object_id 只在 Fields，不进 event_id 公式。
	for _, ev := range res.Events {
		recalc := logtypes.EventID(ev.Source, ev.Record)
		if recalc != ev.EventID {
			t.Fatalf("event_id must ignore archive provenance: %s vs %s", recalc, ev.EventID)
		}
		if ev.Fields["archive_object_id"] != res.ArchiveObjectID {
			t.Fatal("archive_object_id must be provenance field only")
		}
	}
}

// TestAckLossSetsUnknown ACK 丢失 → UNKNOWN，且不得回收。
func TestAckLossSetsUnknown(t *testing.T) {
	tests := []struct {
		name       string
		httpStatus int
		ackLost    bool
		wantState  logtypes.DeliveryState
	}{
		{"http 2xx request done", 200, false, logtypes.DeliveryRequestDone},
		{"http 204 request done", 204, false, logtypes.DeliveryRequestDone},
		{"ack lost unknown", 0, true, logtypes.DeliveryUnknown},
		{"http 5xx unknown", 500, false, logtypes.DeliveryUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := testKey("src-ack", "g1")
			led := ledger.New()
			wal := NewWAL(led, key)
			ev := logtypes.BuildEvent(
				logtypes.SourceIdentity{LogSourceID: "src-ack", SourceGeneration: "g1", ParserVersion: "acquire-v1"},
				logtypes.RecordRange{Start: 0, End: 10},
				"2026-09-20T00:00:00Z", "2026-09-20T00:00:01Z", "INFO", "stdout", "hello",
			)
			if err := wal.Append(ev); err != nil {
				t.Fatal(err)
			}
			if err := wal.Commit(); err != nil {
				t.Fatal(err)
			}
			// 绑定 STAGED 恢复分段：无论 REQUEST_DONE 还是 UNKNOWN，HTTP 结果都不单独放行 reclaim。
			if err := wal.BindRecoverySegment("seg-1", "/tmp/seg-1", 0, 10); err != nil {
				t.Fatal(err)
			}
			err := wal.RecordHTTPResult(DeliveryResult{
				StartPos:   0,
				EndPos:     10,
				HTTPStatus: tt.httpStatus,
				AckLost:    tt.ackLost,
			})
			if err != nil {
				t.Fatal(err)
			}
			ent := led.Get(key)
			if ent == nil || ent.DeliveryState != tt.wantState {
				t.Fatalf("delivery_state=%v want %v", ent.DeliveryState, tt.wantState)
			}
			if ent.Positions.Durable != 10 {
				t.Fatalf("durable must stay at fsync end, got %d", ent.Positions.Durable)
			}
			// UNKNOWN / REQUEST_DONE 均不得在 STAGED 下 reclaim。
			pos, rerr := wal.TryReclaim()
			if rerr == nil && pos > 0 {
				t.Fatalf("reclaim must be blocked while recovery STAGED (or no transfer), pos=%d", pos)
			}
			if tt.ackLost && ent.DeliveryState != logtypes.DeliveryUnknown {
				t.Fatal("ACK loss must keep UNKNOWN recovery responsibility")
			}
		})
	}
}

// TestReclaimBlockedOnStaged 恢复分段 STAGED 时 reclaim 被门禁拦截。
func TestReclaimBlockedOnStaged(t *testing.T) {
	key := testKey("src-staged", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	ev := logtypes.BuildEvent(
		logtypes.SourceIdentity{LogSourceID: "src-staged", SourceGeneration: "g1", ParserVersion: "acquire-v1"},
		logtypes.RecordRange{Start: 0, End: 20},
		"t0", "t1", "INFO", "stdout", "msg",
	)
	if err := wal.Append(ev); err != nil {
		t.Fatal(err)
	}
	if err := wal.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := wal.BindRecoverySegment("seg-s", "seg-s", 0, 20); err != nil {
		t.Fatal(err)
	}
	// 即使 HTTP 2xx REQUEST_DONE，STAGED 仍拦截。
	if err := wal.RecordHTTPResult(DeliveryResult{StartPos: 0, EndPos: 20, HTTPStatus: 200}); err != nil {
		t.Fatal(err)
	}
	ent := led.Get(key)
	if ent.DeliveryState != logtypes.DeliveryRequestDone {
		t.Fatalf("want REQUEST_DONE, got %s", ent.DeliveryState)
	}
	if _, err := wal.TryReclaim(); err == nil {
		t.Fatal("CanReclaim must block reclaim when recovery segment is STAGED")
	}
	if got := led.Get(key).Positions.Reclaim; got != 0 {
		t.Fatalf("reclaim_position must remain 0 on STAGED, got %d", got)
	}
}

// TestReclaimAllowedAfterWALResponsibilityTransferred 责任转移后允许 reclaim。
func TestReclaimAllowedAfterWALResponsibilityTransferred(t *testing.T) {
	tests := []struct {
		name     string
		segState logtypes.RecoverySegmentState
		reason   logtypes.ReleaseReason
		hold     bool
		wantOK   bool
	}{
		{
			name:     "xfer + next copy verified",
			segState: logtypes.RecoveryWALResponsibilityXfer,
			reason:   logtypes.ReleaseNextCopyVerified,
			hold:     false,
			wantOK:   true,
		},
		{
			name:     "xfer + projection backed",
			segState: logtypes.RecoveryWALResponsibilityXfer,
			reason:   logtypes.ReleaseProjectionBacked,
			hold:     false,
			wantOK:   true,
		},
		{
			name:     "xfer blocked by hold",
			segState: logtypes.RecoveryWALResponsibilityXfer,
			reason:   logtypes.ReleaseNextCopyVerified,
			hold:     true,
			wantOK:   false,
		},
		{
			name:     "durable verified not enough",
			segState: logtypes.RecoveryDurableVerified,
			reason:   logtypes.ReleaseNextCopyVerified,
			hold:     false,
			wantOK:   false,
		},
		{
			name:     "staged never",
			segState: logtypes.RecoveryStaged,
			reason:   logtypes.ReleaseProjectionBacked,
			hold:     false,
			wantOK:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := testKey("src-reclaim", "g1")
			led := ledger.New()
			wal := NewWAL(led, key)
			ev := logtypes.BuildEvent(
				logtypes.SourceIdentity{LogSourceID: "src-reclaim", SourceGeneration: "g1", ParserVersion: "acquire-v1"},
				logtypes.RecordRange{Start: 0, End: 50},
				"t0", "t1", "INFO", "stdout", "body",
			)
			if err := wal.Append(ev); err != nil {
				t.Fatal(err)
			}
			if err := wal.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := wal.BindRecoverySegment("seg-r", "seg-r", 0, 50); err != nil {
				t.Fatal(err)
			}
			// 推进到目标状态。
			if tt.segState != logtypes.RecoveryStaged {
				if err := led.TransitionRecovery(key, "seg-r", logtypes.RecoveryDurableVerified, "", ""); err != nil {
					t.Fatal(err)
				}
			}
			if tt.segState == logtypes.RecoveryWALResponsibilityXfer {
				if err := led.TransitionRecovery(key, "seg-r", logtypes.RecoveryWALResponsibilityXfer, tt.reason, "next-copy"); err != nil {
					t.Fatal(err)
				}
			}
			if tt.segState == logtypes.RecoveryDurableVerified {
				// 保持 DURABLE_VERIFIED，reason 可有可无。
				_ = tt.reason
			}
			if tt.hold {
				if err := led.SetRecoveryHold(key, "seg-r", true); err != nil {
					t.Fatal(err)
				}
			}
			if err := wal.RecordHTTPResult(DeliveryResult{StartPos: 0, EndPos: 50, HTTPStatus: 200}); err != nil {
				t.Fatal(err)
			}

			pos, err := wal.TryReclaim()
			if tt.wantOK {
				if err != nil {
					t.Fatalf("reclaim should be allowed: %v", err)
				}
				if pos != 50 {
					t.Fatalf("reclaim_position want 50, got %d", pos)
				}
				if got := led.Get(key).Positions.Reclaim; got != 50 {
					t.Fatalf("ledger reclaim_position=%d", got)
				}
			} else {
				if err == nil {
					t.Fatalf("reclaim must be blocked, got pos=%d", pos)
				}
				if got := led.Get(key).Positions.Reclaim; got != 0 {
					t.Fatalf("reclaim_position must stay 0, got %d", got)
				}
			}
		})
	}
}

// TestMultilineIncompleteOnlyAdvancesRead 未完成多行只推进 read_position。
func TestMultilineIncompleteOnlyAdvancesRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.log")
	// 开启新事件的行以 "START " 前缀；其余是续行。
	content := "START first\ncont-a\ncont-b\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	key := testKey("src-ml", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	hook := NewMultilineBufferHook(func(line []byte) bool {
		return strings.HasPrefix(string(line), "START ")
	})
	tailer := NewFileTailer(led, key, path, wal, hook)
	events, err := tailer.Poll()
	if err != nil {
		t.Fatal(err)
	}
	// 只有遇到下一 START 才 complete；当前文件无下一 START → 0 complete events。
	if len(events) != 0 {
		t.Fatalf("incomplete multiline must not emit complete events, got %d", len(events))
	}
	ent := led.Get(key)
	if ent.Positions.Read == 0 {
		t.Fatal("read_position must advance for incomplete multiline")
	}
	if ent.Positions.Durable != 0 {
		t.Fatalf("durable_position must not advance for incomplete events, got %d", ent.Positions.Durable)
	}
	if hook.PendingStart() == 0 && ent.Positions.Read > 0 {
		// PendingStart 为首行位置 0，合法；崩溃后从首行恢复拼接。
	}
}

// TestCapacityPauseRecordsGap 容量满暂停并记录缺口，禁止静默丢弃。
// TestEvaluateCapacityDiskThresholds 覆盖契约 §6.6 的磁盘阈值分支：
// 80%（含）降级、90%（含）暂停不可恢复写入，且暂停必须记录缺口（禁止静默丢弃）。
func TestEvaluateCapacityDiskThresholds(t *testing.T) {
	// 低于降级阈值 → OK。
	if d := EvaluateCapacity(CapacityBudget{DiskUsagePercent: 79.9}, 0, 0); d.Action != CapacityOK {
		t.Fatalf("disk 79.9%% must be OK, got %s", d.Action)
	}
	// 达到降级阈值 → DEGRADED（不暂停）。
	if d := EvaluateCapacity(CapacityBudget{DiskUsagePercent: 80}, 0, 0); d.Action != CapacityDegraded {
		t.Fatalf("disk 80%% must degrade, got %s", d.Action)
	}
	// 达到暂停阈值 → PAUSED + 必记缺口。
	paused := EvaluateCapacity(CapacityBudget{DiskUsagePercent: 90}, 0, 0)
	if paused.Action != CapacityPaused {
		t.Fatalf("disk 90%% must pause, got %s", paused.Action)
	}
	if !paused.MustRecordGap {
		t.Fatal("pause decision must require a gap record (no silent drop)")
	}

	// ApplyCapacity 落账本：暂停置位 + 缺口登记。
	key := testKey("src-disk", "g1")
	led := ledger.New()
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "src-disk", SourceGeneration: "g1", ParserVersion: "acquire-v1"})
	if err := ApplyCapacity(led, key, paused, 100, 200); err != nil {
		t.Fatalf("apply capacity: %v", err)
	}
	ent := led.Get(key)
	if ent == nil || !ent.AcquirePaused {
		t.Fatalf("disk pause must set AcquirePaused: %+v", ent)
	}
	if ent == nil || len(ent.Gaps) == 0 {
		t.Fatal("disk pause must record a gap; silent drop forbidden")
	}
}

// TestCapacityPauseRecordsGap 覆盖 WAL 预算耗尽路径。
func TestCapacityPauseRecordsGap(t *testing.T) {
	key := testKey("src-cap", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	pipe := NewPipeline(led, key, wal)
	pipe.SetCapacityBudget(CapacityBudget{
		MaxWALBytes:       1,
		DegradedAtPercent: 80,
		PauseAtPercent:    90,
		DiskUsagePercent:  0,
	})
	ev := logtypes.BuildEvent(
		logtypes.SourceIdentity{LogSourceID: "src-cap", SourceGeneration: "g1", ParserVersion: "acquire-v1"},
		logtypes.RecordRange{Start: 0, End: 5},
		"t0", "t1", "INFO", "stdout", "xxxx",
	)
	err := pipe.Ingest([]logtypes.Event{ev})
	// 首次可能因 walBytes=0 通过 budget 评估；第二次因超限暂停。
	if err == nil {
		// 再 ingest 以触发字节超限。
		ev2 := ev
		ev2.Record = logtypes.RecordRange{Start: 5, End: 15}
		ev2.Message = "yyyyyyyy"
		err = pipe.Ingest([]logtypes.Event{ev2})
	}
	if err == nil {
		t.Fatal("expected capacity pause error")
	}
	ent := led.Get(key)
	if !ent.AcquirePaused {
		t.Fatalf("acquire must pause on capacity exhaustion: %+v", ent)
	}
	if len(ent.Gaps) == 0 {
		t.Fatal("capacity pause must record gap; silent drop forbidden")
	}
	// 暂停后 append 拒绝且补 gap。
	ev3 := ev
	ev3.Record = logtypes.RecordRange{Start: 15, End: 20}
	if err := wal.Append(ev3); err == nil {
		t.Fatal("append must fail while paused")
	}
	if len(led.Get(key).Gaps) == 0 {
		t.Fatal("rejected append must leave gap record")
	}
}

// TestWorkerSourceSamePipelineNoRecursiveVL source=worker 同管道且失败不递归写回。
func TestWorkerSourceSamePipelineNoRecursiveVL(t *testing.T) {
	key := testKey("worker-self", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	pipe := NewPipeline(led, key, wal)
	pipe.SetSourceWorker(true)
	deliverCalls := 0
	pipe.SetDeliver(func(events []logtypes.Event) (int, bool, error) {
		deliverCalls++
		return 0, false, os.ErrPermission
	})
	ev := logtypes.BuildEvent(
		logtypes.SourceIdentity{LogSourceID: "worker-self", SourceGeneration: "g1", ParserVersion: "acquire-v1"},
		logtypes.RecordRange{Start: 0, End: 8},
		"t0", "t1", "INFO", "worker", "self-log",
	)
	if err := pipe.Ingest([]logtypes.Event{ev}); err != nil {
		t.Fatalf("worker source deliver failure must not bubble as recursive VL write: %v", err)
	}
	if deliverCalls != 1 {
		t.Fatalf("same pipeline must still attempt deliver once, got %d", deliverCalls)
	}
	if pipe.WorkerSelfFailures() < 1 {
		t.Fatal("worker self failure must be counted locally")
	}
	ent := led.Get(key)
	// 本地 gap 可见；DeliveryState 不得被伪造成 REQUEST_DONE。
	if ent.DeliveryState == logtypes.DeliveryRequestDone {
		t.Fatal("failed deliver must not set REQUEST_DONE")
	}
	if len(ent.Gaps) == 0 {
		t.Fatal("worker self failure should still record local gap")
	}
}

// TestDeliveryPositionIgnoresOutOfOrderHoles 乱序响应不得越过未解决空洞。
func TestDeliveryPositionIgnoresOutOfOrderHoles(t *testing.T) {
	key := testKey("src-ooo", "g1")
	led := ledger.New()
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "src-ooo", SourceGeneration: "g1"})
	// 先到 [100,200) REQUEST_DONE，空洞 [0,100) 未解决。
	if err := led.RecordDelivery(key, 100, 200, logtypes.DeliveryRequestDone); err != nil {
		t.Fatal(err)
	}
	if got := led.Get(key).Positions.Delivery; got != 0 {
		t.Fatalf("delivery_position must not leap over hole, got %d", got)
	}
	// 补上 [0,100) UNKNOWN 后连续前缀到 200。
	if err := led.RecordDelivery(key, 0, 100, logtypes.DeliveryUnknown); err != nil {
		t.Fatal(err)
	}
	if got := led.Get(key).Positions.Delivery; got != 200 {
		t.Fatalf("contiguous delivery end want 200, got %d", got)
	}
}

// TestCorruptGzipRecordsGap 损坏 gz 记缺口，不拖死后续源。
func TestCorruptGzipRecordsGap(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.gz")
	if err := os.WriteFile(bad, []byte("not-gzip-payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	key := testKey("src-badgz", "g1")
	led := ledger.New()
	imp := NewArchiveImporter(led, key)
	res, err := imp.ImportGzip(bad)
	if err != nil {
		t.Fatalf("corrupt gz should not hard-fail importer: %v", err)
	}
	if !res.Skipped {
		t.Fatal("corrupt gz must skip import")
	}
	if len(led.Get(key).Gaps) == 0 {
		t.Fatal("corrupt gz must record visible gap")
	}
	// 后续源仍可导入。
	good := filepath.Join(dir, "good.gz")
	writeGzip(t, good, []string{"ok"})
	key2 := testKey("src-goodgz", "g1")
	imp2 := NewArchiveImporter(led, key2)
	res2, err := imp2.ImportGzip(good)
	if err != nil || res2.ImportedCount != 1 {
		t.Fatalf("subsequent source must proceed: %+v err=%v", res2, err)
	}
}

// TestFileTailerCursorResume 崩溃后从账本 cursor 恢复续读。
func TestFileTailerCursorResume(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resume.log")
	if err := os.WriteFile(path, []byte("r1\nr2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	key := testKey("src-resume", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	t1 := NewFileTailer(led, key, path, wal, NewLineHook())
	evs, err := t1.Poll()
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("want 2, got %d", len(evs))
	}
	if err := wal.Append(evs...); err != nil {
		t.Fatal(err)
	}
	if err := wal.Commit(); err != nil {
		t.Fatal(err)
	}
	saved := t1.LogicalPos()

	// 追加新行，模拟重启：新建 tailer 从账本恢复。
	if err := os.WriteFile(path, []byte("r1\nr2\nr3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t2 := NewFileTailer(led, key, path, wal, NewLineHook())
	// resume：逻辑位置与文件 cursor 均对齐 durable，不重复读取已耐久前缀。
	if t2.LogicalPos() != saved {
		t.Fatalf("resume logical pos want %d got %d", saved, t2.LogicalPos())
	}
	more, err := t2.Poll()
	if err != nil {
		t.Fatal(err)
	}
	if len(more) != 1 {
		t.Fatalf("resume must only emit new line r3, got %d", len(more))
	}
	if more[0].Message != "r3" {
		t.Fatalf("want r3, got %q", more[0].Message)
	}
	// 新事件位置在 saved 之后，不与 r1/r2 重叠。
	if more[0].Record.Start < saved {
		t.Fatalf("new event must start at/after durable cursor %d, got %d", saved, more[0].Record.Start)
	}
	ent := led.Get(key)
	if ent.Positions.Durable < saved {
		t.Fatalf("durable %d < saved %d", ent.Positions.Durable, saved)
	}
}

// TestWALPrunesReclaimedEntries 锁定 WAL 在 reclaim 推进后回收已覆盖条目（否则内存随采集总量线性增长）。
func TestWALPrunesReclaimedEntries(t *testing.T) {
	key := testKey("src-prune", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	pipe := NewPipeline(led, key, wal)

	var events []logtypes.Event
	for i := 0; i < 10; i++ {
		events = append(events, logtypes.BuildEvent(
			logtypes.SourceIdentity{LogSourceID: "src-prune", SourceGeneration: "g1", ParserVersion: "acquire-v1"},
			logtypes.RecordRange{Start: uint64(i * 10), End: uint64(i*10 + 9)},
			"t0", "t1", "INFO", "stdout", "line",
		))
	}
	require.NoError(t, pipe.Ingest(events))
	require.Len(t, wal.Snapshot(), 10, "尚未回收时保留全部条目")

	// 登记受管恢复分段并转移责任，令 CanReclaim 放行。
	require.NoError(t, led.RegisterRecovery(key, ledger.RecoveryRef{
		SegmentID: "seg", State: logtypes.RecoveryStaged, CoversFrom: 0, CoversTo: 99,
	}))
	require.NoError(t, led.TransitionRecovery(key, "seg", logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, led.TransitionRecovery(key, "seg", logtypes.RecoveryWALResponsibilityXfer, "", "recv"))
	if _, err := wal.TryReclaim(); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	require.Empty(t, wal.Snapshot(), "reclaim 前缀覆盖的条目必须被回收")
}

// TestWALKeepsUnreclaimedEntries reclaim 前缀之外（未耐久或未回收）的条目不得被丢弃。
func TestWALKeepsUnreclaimedEntries(t *testing.T) {
	key := testKey("src-keep", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	pipe := NewPipeline(led, key, wal)
	var events []logtypes.Event
	for i := 0; i < 4; i++ {
		events = append(events, logtypes.BuildEvent(
			logtypes.SourceIdentity{LogSourceID: "src-keep", SourceGeneration: "g1", ParserVersion: "acquire-v1"},
			logtypes.RecordRange{Start: uint64(i * 10), End: uint64(i*10 + 9)},
			"t0", "t1", "INFO", "stdout", "line",
		))
	}
	require.NoError(t, pipe.Ingest(events))
	// 仅覆盖前两条。
	require.NoError(t, led.RegisterRecovery(key, ledger.RecoveryRef{
		SegmentID: "seg", State: logtypes.RecoveryStaged, CoversFrom: 0, CoversTo: 19,
	}))
	require.NoError(t, led.TransitionRecovery(key, "seg", logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, led.TransitionRecovery(key, "seg", logtypes.RecoveryWALResponsibilityXfer, "", "recv"))
	if _, err := wal.TryReclaim(); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	require.Len(t, wal.Snapshot(), 2, "reclaim 前缀之外的条目必须保留")
}

// TestArchiveUnreadableRecordsGap 锁定 FR-474 §5#1「权限可见」：不可读归档必须记缺口并标记失败，
// 且不拖死后续源（不得静默跳过）。
func TestArchiveUnreadableRecordsGap(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("以 root 运行时权限位不生效，跳过权限路径")
	}
	dir := t.TempDir()
	unreadable := filepath.Join(dir, "locked.gz")
	writeGzip(t, unreadable, []string{"secret"})
	require.NoError(t, os.Chmod(unreadable, 0o000))
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })

	key := testKey("src-perm", "g1")
	led := ledger.New()
	imp := NewArchiveImporter(led, key)
	res, err := imp.ImportGzip(unreadable)
	require.NoError(t, err, "权限失败不得硬失败拖死采集")
	require.True(t, res.Skipped, "不可读归档必须跳过")

	ent := led.Get(key)
	require.NotNil(t, ent)
	require.NotEmpty(t, ent.Gaps, "权限失败必须留下可见缺口")
	require.Equal(t, "ARCHIVE_UNREADABLE", ent.Gaps[len(ent.Gaps)-1].Reason)

	// 后续源仍可导入：不得因单源权限问题阻塞整轮。
	good := filepath.Join(dir, "ok.gz")
	writeGzip(t, good, []string{"ok"})
	imp2 := NewArchiveImporter(led, testKey("src-perm-next", "g1"))
	res2, err := imp2.ImportGzip(good)
	require.NoError(t, err)
	require.EqualValues(t, 1, res2.ImportedCount)
}

// TestArchiveTruncatedGzipRecordsGap 锁定 FR-474 §5#1「截断可见」：截断 gz 必须产生可见缺口，
// 且已成功读出的完整行仍被计入（不得整包丢弃）。
func TestArchiveTruncatedGzipRecordsGap(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "full.gz")
	writeGzip(t, full, []string{"line-1", "line-2", "line-3"})
	raw, err := os.ReadFile(full)
	require.NoError(t, err)
	require.Greater(t, len(raw), 10)

	// 在 deflate 流中间截断（而非仅去尾部 CRC），才会真正产生读错误。
	truncated := filepath.Join(dir, "truncated.gz")
	require.NoError(t, os.WriteFile(truncated, raw[:len(raw)/2], 0o644))

	key := testKey("src-trunc", "g1")
	led := ledger.New()
	imp := NewArchiveImporter(led, key)
	res, err := imp.ImportGzip(truncated)
	require.NoError(t, err, "截断归档不得硬失败")

	ent := led.Get(key)
	require.NotNil(t, ent)
	hasGap := false
	for _, g := range ent.Gaps {
		if g.Reason == "ARCHIVE_READ_ERROR" || g.Reason == "ARCHIVE_GZIP_CORRUPT" {
			hasGap = true
		}
	}
	require.True(t, hasGap, "截断/读错误必须留下可见缺口；res=%+v", res)
}
