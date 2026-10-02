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

// TestRotationDetectedWhenReplacementSharesPrefixAndGrows 钉住 inode 判据（用户质疑 1）。
//
// 形态（真机常见）：MC 服务端重启后新建的 latest.log **内容更大**，且开头是与旧文件
// 逐字节相同的固定段（版本 banner / 启动参数 / EULA 那几行）。此时两条旧判据全部失效：
//   - `size < cursor` 不成立（新文件更大）；
//   - 前 512 字节哈希相同（前缀一致）⇒ replaced=false。
//
// ⇒ 判定「未轮转」⇒ 采集器继续用旧 segmentStart/cursor 去读**新文件**：新文件开头的
// cursor 个字节被当成已读跳过（静默丢内容），旧文件的未读尾部永远不会被接管。
//
// 旧判据下本用例必然红：RotationCount 保持 0，且事件里能同时看到「新文件被从中间截读」。
//
// 转红方式（实测）：把 PrepareRotation 里的 `inodeReplaced` 从判据里去掉，本用例立即红。
func TestRotationDetectedWhenReplacementSharesPrefixAndGrows(t *testing.T) {
	dir := t.TempDir()
	latest := filepath.Join(dir, "latest.log")
	rotated := filepath.Join(dir, "latest.log.1")
	// 固定 banner 前缀：必须 ≥ 512 字节，否则身份前缀（上限 512）会覆盖到正文，
	// 哈希判据仍能识破替换——那样就测不到 inode 判据了。
	banner := "Paper 1.21.1 build 42 | Starting minecraft server version 1.21.1\n" +
		strings.Repeat("  [banner] fixed startup log line, identical in every latest.log\n", 12)
	require.Greater(t, len(banner), 512, "夹具前提：共享前缀必须超过身份前缀上限 512")
	oldBody := "old-1\nold-2\nold-3\n"
	if err := os.WriteFile(latest, []byte(banner+oldBody), 0o644); err != nil {
		t.Fatal(err)
	}
	key := testKey("src-inode", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	tailer := NewFileTailer(led, key, latest, wal, NewLineHook())
	tailer.SetRotateTarget(rotated)

	evs, err := tailer.Poll()
	if err != nil {
		t.Fatalf("poll1: %v", err)
	}
	require.Len(t, evs, strings.Count(banner, "\n")+3, "首轮应读出 banner 各行 + 3 行正文")
	require.Equal(t, 0, tailer.RotationCount())
	beforeCursor := tailer.LogicalPos()
	require.Greater(t, beforeCursor, uint64(0))

	// 轮转：旧文件移到 .1；同名路径换上**更大**且**开头逐字节相同**的新文件。
	require.NoError(t, os.Rename(latest, rotated))
	newBody := banner + "new-a\nnew-b\nnew-c\nnew-d\nnew-e\n"
	require.NoError(t, os.WriteFile(latest, []byte(newBody), 0o644))
	fi, err := os.Stat(latest)
	require.NoError(t, err)
	require.Greater(t, uint64(fi.Size()), beforeCursor,
		"夹具前提：替换文件必须比旧游标长，否则尺寸判据就足以识破")

	rotatedFlag, err := tailer.PrepareRotation()
	require.NoError(t, err)
	require.True(t, rotatedFlag,
		"替换文件更大且开头相同（banner）时必须靠 inode 判据识破轮转，否则新文件前段被静默跳过")
	require.Equal(t, 1, tailer.RotationCount())

	evs2, err := tailer.Poll()
	require.NoError(t, err)
	var msgs []string
	for _, ev := range evs2 {
		msgs = append(msgs, ev.Message)
	}
	joined := strings.Join(msgs, "\n")
	require.Contains(t, joined, "new-a", "新文件必须从**头部**重新读起，不得跳过前 cursor 字节")
	require.Contains(t, joined, "new-e")
	// 新文件的首个 banner 行必须**完整**读出（若被跳过前 cursor 字节，这里就只剩后半截）。
	require.Contains(t, joined, banner[:len(banner)-1], "新文件的 banner 行应完整读出")
}

// TestRotationDetectionSurvivesSameSizeReplacement 钉住「同尺寸替换」也走 inode 判据。
//
// 尺寸判据（size < cursor）与「尺寸相等」在旧实现里是盲区：若替换文件与旧文件长度恰好
// 相同、且开头前缀一致，旧判据同样判为「未轮转」。inode 判据与长度无关。
//
// 注意夹具必须真的换 inode：`os.WriteFile` 是对同路径 O_TRUNC，**不换** inode，
// 因此必须先写临时文件再 rename 覆盖（这才对应生产上「新文件替换同名文件」的形态）。
func TestRotationDetectionSurvivesSameSizeReplacement(t *testing.T) {
	dir := t.TempDir()
	latest := filepath.Join(dir, "latest.log")
	body := "common-prefix-line-a\ncommon-prefix-line-b\n"
	require.NoError(t, os.WriteFile(latest, []byte(body), 0o644))
	key := testKey("src-inode-same", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	tailer := NewFileTailer(led, key, latest, wal, NewLineHook())
	tailer.SetRotateTarget(filepath.Join(dir, "latest.log.1"))

	require.Len(t, mustPoll(t, tailer), 2)
	beforeCursor := tailer.LogicalPos()

	// 同名替换：内容长度与旧文件**完全相同**、开头也相同，但换了一个新 inode。
	replacement := filepath.Join(dir, "replacement.tmp")
	require.NoError(t, os.WriteFile(replacement, []byte(body), 0o644))
	require.NoError(t, os.Rename(replacement, latest))
	require.Equal(t, beforeCursor, uint64(len(body)))

	rotatedFlag, err := tailer.PrepareRotation()
	require.NoError(t, err)
	require.True(t, rotatedFlag, "同尺寸同名替换必须靠 inode 识破（尺寸与哈希判据都会漏）")
	require.Equal(t, 1, tailer.RotationCount())
}

func mustPoll(t *testing.T, tailer *FileTailer) []logtypes.Event {
	t.Helper()
	evs, err := tailer.Poll()
	require.NoError(t, err)
	return evs
}

// TestCapacityPauseRecordsGap 覆盖容量门禁的暂停路径（磁盘水位 ≥ 暂停阈值）。
//
// 2026-10-02 改动：原夹具用 `MaxWALBytes: 1` 触发暂停，即「字节预算耗尽 → 暂停」。
// 该路径已**刻意移除**——字节口径改由 WAL 自身的积压上限执行（带滞回、可自愈），
// 门禁里的字节读数只降级不暂停，理由见 capacity.go EvaluateCapacity 的注释。
// 本测试守的不变量未变：**容量耗尽 ⇒ 暂停 + 必记缺口（禁止静默丢）**，
// 只是触发源改为门禁里唯一还能永久暂停的维度（磁盘）。
func TestCapacityPauseRecordsGap(t *testing.T) {
	key := testKey("src-cap", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	pipe := NewPipeline(led, key, wal)
	pipe.SetCapacityBudget(CapacityBudget{
		DegradedAtPercent: 80,
		PauseAtPercent:    90,
		DiskUsagePercent:  95, // ≥90 → PAUSED
	})
	ev := logtypes.BuildEvent(
		logtypes.SourceIdentity{LogSourceID: "src-cap", SourceGeneration: "g1", ParserVersion: "acquire-v1"},
		logtypes.RecordRange{Start: 0, End: 5},
		"t0", "t1", "INFO", "stdout", "xxxx",
	)
	err := pipe.Ingest([]logtypes.Event{ev})
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

// TestCapacityGateReadsBacklogNotLifetimeTotal 钉住 P0：门禁必须读**当前积压**。
//
// 形态（2026-10-02 定位）：门禁曾读采集管道里一个只增不减的累计追加量（`p.walBytes`），
// 而容量 PAUSE 不被积压滞回清除（wal.go maybeResumeBacklogLocked 按原因前缀过滤）。
// 于是「单源累计追加满 max_wal_bytes」那一刻会**永久停采**该源——即使真实积压早已被
// reclaim 清空。现场形态是「配置的 512MiB 上限一到，这个源的日志一个字节都不再采」。
//
// 转红方式（实测）：把 Ingest 里的 `p.wal.Backlog()` 换回累计量计数器，本用例立即红
// ——积压为 0 但累计量已越界，却仍被判 PAUSED。
func TestCapacityGateReadsBacklogNotLifetimeTotal(t *testing.T) {
	key := testKey("src-backlog", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	// 真实积压上限放到很大的值，确保本用例只考察「门禁读哪个数」。
	wal.SetLimits(0, 1<<20)
	pipe := NewPipeline(led, key, wal)
	pipe.SetCapacityBudget(CapacityBudget{
		MaxWALBytes:       1024, // 门禁高水位：当前积压远低于它
		DegradedAtPercent: 80,
		PauseAtPercent:    90,
		DiskUsagePercent:  0,
	})
	delivered := 0
	pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
		delivered++
		return 204, false, nil
	})
	build := func(start, end uint64, msg string) logtypes.Event {
		return logtypes.BuildEvent(
			logtypes.SourceIdentity{LogSourceID: "src-backlog", SourceGeneration: "g1", ParserVersion: "acquire-v1"},
			logtypes.RecordRange{Start: start, End: end},
			"t0", "t1", "INFO", "stdout", msg)
	}

	// 累计追加量远超 MaxWALBytes，但每批都被投递 + 回收 ⇒ **当前积压始终为 0**。
	var pos uint64
	for i := 0; i < 40; i++ {
		ev := build(pos, pos+256, strings.Repeat("x", 256))
		pos += 256
		if err := pipe.Ingest([]logtypes.Event{ev}); err != nil {
			t.Fatalf("第 %d 批被拒：累计量不得作为门禁（当前积压为 0）: %v", i, err)
		}
		// 清空积压：本夹具不起账本恢复分段（真机由恢复分段流程承担），故按已投递前缀
		// 直接剪枝——这正是 reclaim 推进后的**结果**，而本用例考察的是「门禁读哪个数」。
		wal.pruneReclaimed(pos)
		if entries, bytes := wal.Backlog(); entries != 0 || bytes != 0 {
			t.Fatalf("夹具前提不成立：剪枝后积压应为 0，实测 entries=%d bytes=%d", entries, bytes)
		}
	}
	if got := pipe.WALAppendedBytesTotal(); got <= 1024 {
		t.Fatalf("夹具前提不成立：累计追加量 %d 未越过门禁水位 1024", got)
	}
	ent := led.Get(key)
	if ent.AcquirePaused {
		t.Fatalf("累计追加量越过水位不得暂停采集（真实积压为 0）: %+v", ent.PauseReason)
	}
	if len(ent.Gaps) != 0 {
		t.Fatalf("不得因为累计量越过水位而记缺口: %+v", ent.Gaps)
	}
	if delivered == 0 {
		t.Fatal("投递路径应当照常工作")
	}
}

// TestCapacityGateNeverPausesOnByteReading 钉住字节口径的**唯一**动作是降级（可见性），不是暂停。
//
// 为什么必须分开钉：暂停与自愈必须成对。WAL 真实积压上限（wal.go）带滞回、会自己解暂停，
// 而容量门禁的 PAUSE 不被积压滞回清除（maybeResumeBacklogLocked 按原因前缀过滤）。
// 所以只要门禁在字节维度上暂停，就得到一个**不能自愈**的暂停——这正是 P0 的成因。
// 字节读数保留下来只为了高水位可见（DEGRADED 不记缺口、不暂停）。
//
// 转红方式（实测）：把本函数里的 DEGRADED 改回 PAUSED（旧行为），第一条断言立即红。
func TestCapacityGateNeverPausesOnByteReading(t *testing.T) {
	budget := CapacityBudget{MaxWALBytes: 1024, DegradedAtPercent: 80, PauseAtPercent: 90, DiskUsagePercent: 0}
	// 越界：必须仍为 DEGRADED（可见），绝不 PAUSED。
	dec := EvaluateCapacity(budget, 4096, 0)
	if dec.Action == CapacityPaused {
		t.Fatalf("字节口径越界不得暂停（该暂停无法自愈）: %+v", dec)
	}
	if dec.Action != CapacityDegraded {
		t.Fatalf("字节口径越界必须给出可见降级信号，实测 %q", dec.Action)
	}
	if dec.MustRecordGap {
		t.Fatal("字节口径降级不得记缺口（降级不是数据缺失）")
	}
	// 高水位（80%）同样只降级。
	if dec := EvaluateCapacity(budget, 900, 0); dec.Action != CapacityDegraded {
		t.Fatalf("WAL 高水位应降级，实测 %q", dec.Action)
	}
	// 未到高水位：放行。
	if dec := EvaluateCapacity(budget, 1024*79/100, 0); dec.Action != CapacityOK {
		t.Fatalf("WAL 未到高水位应放行，实测 %q", dec.Action)
	}
	// 对照：磁盘口径**必须**仍然暂停（门禁里唯一还能永久暂停的维度）。
	if dec := EvaluateCapacity(CapacityBudget{DiskUsagePercent: 95}, 0, 0); dec.Action != CapacityPaused {
		t.Fatalf("磁盘 ≥90%% 必须暂停，实测 %q", dec.Action)
	}
}

// TestWALBacklogLimitPausesAndClearsOnDrain 钉住：真实积压越界必须暂停，且**该暂停能被清除**。
//
// 两条都要：① 越界即停（背压生效，读端不得无限快于消费端）；
// ② 积压回落到上限一半以下必须自动恢复——否则源会永久停在暂停上，正是 P0 的形态。
//
// 转红方式（实测）：把 SetLimits 的字节上限改成「永不触发」的大值，① 立即红；
// 把 maybeResumeBacklogLocked 的滞回条件从 `> max/2` 改成恒 false，② 立即红。
func TestWALBacklogLimitPausesAndClearsOnDrain(t *testing.T) {
	key := testKey("src-bp", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	// 条目上限 2、字节上限给足：由条目维度触发，便于构造。
	wal.SetLimits(2, 1<<20)
	pipe := NewPipeline(led, key, wal)
	pipe.SetCapacityBudget(CapacityBudget{DegradedAtPercent: 80, PauseAtPercent: 90})
	pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
		return 204, false, nil
	})
	build := func(start, end uint64) logtypes.Event {
		return logtypes.BuildEvent(
			logtypes.SourceIdentity{LogSourceID: "src-bp", SourceGeneration: "g1", ParserVersion: "acquire-v1"},
			logtypes.RecordRange{Start: start, End: end},
			"t0", "t1", "INFO", "stdout", "line")
	}

	// ① 越界即暂停：一批塞 3 条。
	if err := pipe.Ingest([]logtypes.Event{build(0, 4), build(5, 9), build(10, 14)}); err != nil {
		t.Fatalf("首批本身应当成功（越界在追加后判定）: %v", err)
	}
	ent := led.Get(key)
	if !ent.AcquirePaused {
		t.Fatalf("积压 3 条越过上限 2 条必须暂停采集: %+v", ent)
	}
	if !strings.HasPrefix(ent.PauseReason, walBacklogPauseReason) {
		t.Fatalf("暂停原因应为本包的积压原因前缀，实测 %q", ent.PauseReason)
	}
	// 「暂停期间摄取被拒」由既有的 TestWALBacklogLimitPausesAcquisitionWithoutDroppingEntries 覆盖；
	// 本用例刻意**不**制造被拒批次：被拒会补一条未解决缺口，而 ResumeAcquire 要求零缺口，
	// 那会让「② 能否恢复」这条断言变成在考缺口，而不是在考滞回。

	// ② 消化后必须能恢复：投递 + 回收 → 积压回落到上限一半以下。
	if _, err := pipe.DeliverPending(); err != nil {
		t.Fatalf("暂停期排空存量失败: %v", err)
	}
	// 本夹具不起账本恢复分段，故按已投递前缀直接剪枝模拟「回收推进」；
	// 剪枝路径内会做一次滞回恢复评估——这正是生产上唯一会经过的恢复检查点。
	wal.pruneReclaimed(20)
	entries, bytes := wal.Backlog()
	if entries > 1 || bytes > (1<<20)/2 {
		t.Fatalf("夹具前提不成立：积压未回落到低水位 entries=%d bytes=%d", entries, bytes)
	}
	if !wal.EvaluateResume() {
		t.Fatal("积压回落至低水位后必须能解暂停（否则源永久停在暂停上 = P0 形态）")
	}
	if led.Get(key).AcquirePaused {
		t.Fatalf("暂停应已被清除: %+v", led.Get(key).PauseReason)
	}
}

// TestReplayDrainIsRateLimited 钉住回放限速：单轮外发有界，且余量不丢、下一轮接着发。
//
// 为什么要限速：VL 变慢/刚恢复时存量可能数千条，一次全塞进去会把恢复瞬间变成一次
// 自我制造的流量尖峰（恢复 → 打爆 → 再暂停 的振荡）。
// 转红方式（实测）：把 deliverPendingBestEffort 的上限判断去掉，第一条断言立即红。
func TestReplayDrainIsRateLimited(t *testing.T) {
	key := testKey("src-replay", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetLimits(0, 1<<20) // 上限给足，本用例只考察回放限速
	pipe := NewPipeline(led, key, wal)
	pipe.SetMaxReplayEventsPerDrain(2)
	var batches []int
	pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
		batches = append(batches, len(events))
		return 204, false, nil
	})
	build := func(start, end uint64, msg string) logtypes.Event {
		return logtypes.BuildEvent(
			logtypes.SourceIdentity{LogSourceID: "src-replay", SourceGeneration: "g1", ParserVersion: "acquire-v1"},
			logtypes.RecordRange{Start: start, End: end},
			"t0", "t1", "INFO", "stdout", msg)
	}
	// 塞入 6 条已 durable 的存量（走正常 Ingest → Commit）。
	for i := 0; i < 6; i++ {
		start := uint64(i * 10)
		ev := build(start, start+4, "payload-"+string(rune('a'+i)))
		if err := wal.Append(ev); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := wal.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	entries, _ := wal.Backlog()
	if entries != 6 {
		t.Fatalf("夹具前提不成立：期望 6 条存量，实测 %d", entries)
	}

	batchStart := len(batches)
	var deliveredTotal int
	for round := 0; round < 3; round++ {
		pending, err := pipe.DeliverPending()
		if err != nil {
			t.Fatalf("第 %d 轮排空失败: %v", round, err)
		}
		if len(pending) > 2 {
			t.Fatalf("第 %d 轮外发 %d 条，超过限速 2 条", round, len(pending))
		}
		deliveredTotal += len(pending)
	}
	if len(batches) == batchStart {
		t.Fatal("限速不得等于不外发：存量必须被排空")
	}
	for i, n := range batches[batchStart:] {
		if n > 2 {
			t.Fatalf("第 %d 次投递外发 %d 条，超过限速 2 条", i, n)
		}
	}
	if deliveredTotal != 6 {
		t.Fatalf("限速只应摊平节奏、不得丢数据：3 轮共外发 %d 条，期望 6 条", deliveredTotal)
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
	pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
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
	dir := t.TempDir()
	unreadable := filepath.Join(dir, "locked.gz")
	writeGzip(t, unreadable, []string{"secret"})
	// 「不可读」按平台等价构造：Unix 清权限位、Windows 独占句柄（见 makeUnreadable）。
	// 还原先于 t.TempDir 清理执行（t.Cleanup 后进先出），否则目录删不掉。
	t.Cleanup(makeUnreadable(t, unreadable))

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
