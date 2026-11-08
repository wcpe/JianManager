package retention

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// 本文件守 FR-501 的 `ActionArchiveCopy`（硬规则**次选**路径）：导出 → 压缩 → 校验 → 回收。
//
// 每条用例都刻意断言两件事：①该发生的发生了；②**不该发生的没发生**——
// 尤其是「原物有没有被删」。这条路径的失败模式不是「没导出」，而是
// 「导出了/没导出都照样删了热层」，而那种事故在现场只表现为「日志不见了」。

// archiveCopyPolicy 打开导出归档开关（其余闸门保持默认关闭）。
//
// 刻意不叫 enabledPolicy + 手改：这个开关是**新增路径的唯一入口**，
// 用一个命名明确的构造器能让「哪些用例故意打开了它」在文件里一眼可见。
func archiveCopyPolicy() Policy {
	p := enabledPolicy()
	p.ArchiveCopy.Enabled = true
	return p
}

// fakeExporter 记录导出调用，可注入失败与结果。
type fakeExporter struct {
	calls    []string
	failWith error
	result   ArchiveExportResult
}

func (x *fakeExporter) ExportPartition(_ context.Context, ns, day string) (ArchiveExportResult, error) {
	x.calls = append(x.calls, ns+"/"+day)
	if x.failWith != nil {
		return ArchiveExportResult{}, x.failWith
	}
	res := x.result
	if res.ObjectCount == 0 {
		res = ArchiveExportResult{
			ObjectCount: 1, SourceBytes: 1024, StoredBytes: 256,
			Location:     "local " + ns + "/" + day + "/g1",
			ProviderKind: "local",
			ObjectPrefix: ns + "/" + day + "/g1",
			// 证据必须完整：Catalog 侧的证据校验会逐项检查，
			// 这里给全是为了让「回收成功」的用例测到执行器本身的次序，
			// 而不是被证据缺失挡在第一步（证据缺失有专门的用例）。
			ManifestChecksum: "sha256:manifest-" + ns + "-" + day,
			CoverageComplete: true,
		}
	}
	return res, nil
}

// fakeAuthority 记录权威态迁移的调用次序（mark → [rollback] → reclaimed）。
//
// 记录**次序**而不只是计数：本路径最容易写错的地方就是「先删后改权威态」，
// 只数次数无法发现反序。
type fakeAuthority struct {
	marked      []string
	rollbacks   []string
	reclaimed   []string
	released    []string
	events      []string
	markErr     error
	rollbackErr error
	freezeErr   error
}

func (a *fakeAuthority) FreezeForArchive(_ context.Context, ns, day string) error {
	a.events = append(a.events, "freeze:"+ns+"/"+day)
	return a.freezeErr
}

func (a *fakeAuthority) ReleaseArchiveFreeze(_ context.Context, ns, day string, reason string) error {
	a.released = append(a.released, ns+"/"+day)
	a.events = append(a.events, "release:"+ns+"/"+day)
	return nil
}

func (a *fakeAuthority) MarkArchived(_ context.Context, ns, day string, ev ArchiveEvidence) error {
	if a.markErr != nil {
		return a.markErr
	}
	a.marked = append(a.marked, ns+"/"+day)
	a.events = append(a.events, "mark:"+ns+"/"+day)
	if ev.ProviderKind == "" || ev.ObjectPrefix == "" || ev.ManifestChecksum == "" || ev.ObjectCount <= 0 {
		return errors.New("执行器必须把完整归档证据交给权威态迁移（provider/prefix/checksum/对象数）")
	}
	return nil
}

func (a *fakeAuthority) RollbackArchived(_ context.Context, ns, day string, reason string) error {
	if a.rollbackErr != nil {
		return a.rollbackErr
	}
	a.rollbacks = append(a.rollbacks, ns+"/"+day)
	a.events = append(a.events, "rollback:"+ns+"/"+day)
	_ = reason
	return nil
}

func (a *fakeAuthority) MarkHotReclaimed(_ context.Context, ns, day string) error {
	a.reclaimed = append(a.reclaimed, ns+"/"+day)
	a.events = append(a.events, "reclaimed:"+ns+"/"+day)
	return nil
}

// threeGatePolicy 把删除三闸全部打开（回收热层的唯一合法前提）。
//
// 三闸 = 意图闸（discard）+ 执行闸（sweep.vl_sweep）+ VL 侧闸（log_vl.delete_enabled）。
// 注意它**同时**保留了 archive_copy 开关：本组用例测的就是「导出成功之后的回收」。
func threeGatePolicy() Policy {
	p := archiveCopyPolicy()
	p.Discard = true
	p.Sweep.VLSweep = true
	p.Sweep.VLDeleteEnabled = true
	return p
}

// dueOutcomes 过滤出**本轮真正动手**的结果（排除 not_due）。
//
// archiveFixture() 里刻意混了一个尚未到点的分区（20260930）：它同样会出现在计划里，
// 但执行阶段不碰它。若断言时不过滤，就会把「未到点」误判成「实现漏了动作」。
func dueOutcomes(out []ArchiveOutcome) []ArchiveOutcome {
	res := make([]ArchiveOutcome, 0, len(out))
	for _, o := range out {
		if o.Result == ArchiveNotDue {
			continue
		}
		res = append(res, o)
	}
	return res
}

// 用例①：archiver 可用 + mover 不可用 ⇒ 计划必须是 archive_copy（而不是 blocked）。
//
// 在此之前，这条状态唯一能落的地方是 `blocked`（保留原物 + 告警），
// 于是「其实有归档目的地」这件事在观测面上完全看不出来。
func TestArchiveCopyPlannedWhenMoverMissingButArchiverReady(t *testing.T) {
	p := archiveCopyPolicy()
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

	plan := p.PlanArchive(hv, archiveFixture(), false, true)
	if len(plan) == 0 {
		t.Fatal("前置条件：夹具应产出计划")
	}
	for _, tg := range plan {
		if tg.Action == ActionBlocked {
			t.Errorf("归档出口可用时不应落 blocked：%s", tg)
		}
		if tg.Action != ActionArchiveCopy {
			t.Errorf("应规划为 archive_copy，得到 %s（%s）", tg.Action, tg)
		}
	}
}

// 用例②：mover 可用时仍然优先 move_to_cold，**即使**归档出口也可用。
//
// 次序不可变（变异实验 N13 暴露过的真实缺陷）：把 archive_copy 判在 move 之前，
// 会让「冷层完全健康」的部署突然改走导出路径——数据虽然还在，但从「可快查」
// 退化成「要回读才能查」，而且热层回收的语义也完全不同。
func TestArchiveCopyNeverOutranksMoveToCold(t *testing.T) {
	// 同时打开三闸与归档出口：这是最容易写反次序的组合
	// （三闸全开时确实允许回收，但「允许回收」不等于「优先导出」）。
	p := threeGatePolicy()
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

	plan := p.PlanArchive(hv, archiveFixture(), true, true)
	if len(plan) == 0 {
		t.Fatal("前置条件：夹具应产出计划")
	}
	for _, tg := range plan {
		if tg.Action != ActionMoveToCold {
			t.Errorf("冷层可用时必须优先搬运，得到 %s（%s）", tg.Action, tg)
		}
	}

	// 执行层同样不得调用导出出口。
	ex := NewArchiveExecutor(p, &fakeMover{})
	x := &fakeExporter{}
	ex.SetArchiveExporter(x)
	ex.SetRemover(&fakeRemover{})
	_ = ex.Execute(context.Background(), hv, archiveFixture())
	if len(x.calls) != 0 {
		t.Errorf("mover 可用时不得走导出归档，实际调用 %v", x.calls)
	}
}

// 用例③：archive_copy 优先于 discard。
//
// 理由：既然存在一条能留下副本的路，「知情放弃」的前提（没有别的承接物）就不成立。
// 这只会让删除更少，不会更多——故它是**收紧**而不是放宽。
func TestArchiveCopyOutranksDiscard(t *testing.T) {
	p := archiveCopyPolicy()
	p.Discard = true // 意图闸打开，但执行闸与 VL 闸仍关着
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

	plan := p.PlanArchive(hv, archiveFixture(), false, true)
	for _, tg := range plan {
		if tg.Action == ActionDiscard {
			t.Errorf("有归档目的地时不应选择裸删：%s", tg)
		}
	}
}

// 用例④：导出失败 ⇒ 保留原物，且**绝不**触达删除/回收路径。
func TestArchiveCopyExportFailureKeepsOriginal(t *testing.T) {
	p := threeGatePolicy() // 三闸全开：若实现错误地在导出失败后仍回收，本用例会立刻发现
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

	ex := NewArchiveExecutor(p, nil)
	x := &fakeExporter{failWith: errors.New("归档目的地不可达（模拟）")}
	ex.SetArchiveExporter(x)
	ex.SetArchiveAuthority(&fakeAuthority{})
	rm := &fakeRemover{}
	ex.SetRemover(rm)

	out := ex.Execute(context.Background(), hv, archiveFixture())
	if len(x.calls) == 0 {
		t.Fatal("前置条件：应确实尝试过导出")
	}
	due := dueOutcomes(out)
	var copied, kept int
	for _, o := range due {
		if o.Result == ArchiveCopied || o.Result == ArchiveCopiedKeptOriginal {
			t.Errorf("导出失败不得声称已归档：%s", o.Target)
		}
		if o.Result == ArchiveKeptOriginal {
			kept++
			if o.Err == nil {
				t.Errorf("保留原物必须带原因：%s", o.Target)
			}
		}
	}
	if kept == 0 || copied != 0 {
		t.Fatalf("导出失败必须全部落「保留原物」，得到 kept=%d copied=%d", kept, copied)
	}
	if kept != len(due) {
		t.Fatalf("到期分区数 %d 与保留原物数 %d 不一致（有分区被漏掉）", len(due), kept)
	}
	if len(rm.calls) != 0 {
		t.Fatalf("导出失败**绝不能**回收热层，实际删除 %v", rm.calls)
	}
	st := ex.Stats()
	if st.ArchiveCopied != 0 || st.ArchiveCopyReclaimed != 0 {
		t.Errorf("导出失败时归档读数必须为 0，得到 %+v", st)
	}
	if st.KeptOriginal != int64(kept) {
		t.Errorf("观测面必须记下保留原物件数：期望 %d 得到 %d", kept, st.KeptOriginal)
	}
	if !strings.Contains(st.LastError, "保留原物") {
		t.Errorf("最近告警必须说明「已保留原物」，得到 %q", st.LastError)
	}
}

// 用例⑤：校验不通过 ⇒ 保留原物，不回收。
//
// 「校验不通过」在导出侧表现为 ExportPartition 返回错误（它负责逐文件回读 + sha256），
// 因此执行器这一侧看到的仍是「错误 ⇒ 保留原物」。本用例把这一点钉死：
// 执行器**不得**因为「导出已经跑过一轮」就跳过校验结论。
func TestArchiveCopyVerificationFailureKeepsOriginal(t *testing.T) {
	p := threeGatePolicy()
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

	ex := NewArchiveExecutor(p, nil)
	ex.SetArchiveExporter(&fakeExporter{
		failWith: errors.New("归档对象 raw/files/part-1.gz 回读校验失败（写入 aaa，回读 bbb）"),
	})
	ex.SetArchiveAuthority(&fakeAuthority{})
	rm := &fakeRemover{}
	ex.SetRemover(rm)

	out := ex.Execute(context.Background(), hv, archiveFixture())
	if len(rm.calls) != 0 {
		t.Fatalf("校验失败**绝不能**回收热层，实际删除 %v", rm.calls)
	}
	due := dueOutcomes(out)
	if len(due) == 0 {
		t.Fatal("前置条件：夹具应产出到期分区")
	}
	for _, o := range due {
		if o.Result != ArchiveKeptOriginal {
			t.Errorf("校验失败必须落「保留原物」，得到 %s（%s）", o.Result, o.Target)
		}
	}
}

// 用例⑥：校验通过但三闸未全开 ⇒ 保留原物（副本已安全，但还没获准删）。
//
// 这是用户护栏的核心要求：**回收热层必须受与 ActionDiscard 相同的三闸约束**。
// 逐闸各测一次，避免「只测了全关/全开、漏了某一个单闸」——那种漏测会让
// 某一道闸日后被悄悄绕过而无人发现。
func TestArchiveCopyReclaimRequiresAllThreeGates(t *testing.T) {
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name   string
		mutate func(*Policy)
	}{
		{name: "三闸全关（默认）"},
		{name: "缺意图闸 discard", mutate: func(p *Policy) { p.Sweep.VLSweep = true; p.Sweep.VLDeleteEnabled = true }},
		{name: "缺执行闸 sweep.vl_sweep", mutate: func(p *Policy) { p.Discard = true; p.Sweep.VLDeleteEnabled = true }},
		{name: "缺 VL 侧闸 log_vl.delete_enabled", mutate: func(p *Policy) { p.Discard = true; p.Sweep.VLSweep = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := archiveCopyPolicy()
			if tc.mutate != nil {
				tc.mutate(&p)
			}
			// 前提：确实不是三闸全开
			if open, _ := p.DeleteGatesOpen(); open {
				t.Fatal("前置条件：本子用例必须处于「三闸未全开」状态")
			}

			ex := NewArchiveExecutor(p, nil)
			x := &fakeExporter{}
			ex.SetArchiveExporter(x)
			ex.SetArchiveAuthority(&fakeAuthority{})
			rm := &fakeRemover{}
			ex.SetRemover(rm)

			out := ex.Execute(context.Background(), hv, archiveFixture())
			if len(x.calls) == 0 {
				t.Fatal("前置条件：导出应确实执行过")
			}
			if len(rm.calls) != 0 {
				t.Fatalf("三闸未全开时**绝不能**回收热层，实际删除 %v", rm.calls)
			}
			due := dueOutcomes(out)
			if len(due) == 0 {
				t.Fatal("前置条件：夹具应产出到期分区")
			}
			for _, o := range due {
				if o.Result != ArchiveCopiedKeptOriginal {
					t.Errorf("导出成功但未回收应记 %s，得到 %s（%s）",
						ArchiveCopiedKeptOriginal, o.Result, o.Target)
				}
			}
			st := ex.Stats()
			if st.ArchiveCopied != int64(len(due)) {
				t.Errorf("导出成功数应被记下：期望 %d 得到 %d", len(due), st.ArchiveCopied)
			}
			if st.ArchiveCopyReclaimed != 0 {
				t.Errorf("未回收时不得记已回收，得到 %d", st.ArchiveCopyReclaimed)
			}
			if st.ArchiveCopyKeptOriginal != int64(len(due)) {
				t.Errorf("应记「导出成功但保留原物」%d 件，得到 %d", len(due), st.ArchiveCopyKeptOriginal)
			}
		})
	}
}

// 用例⑦：校验通过 + 三闸全开 + 已装配归档权威态与删除出口 ⇒ 回收热层。
//
// 2026-10 收紧：除了三闸与删除出口，还必须装配 **ArchiveAuthority**。
// 理由：删一个仍属 HOT 权威的日分区，会让查询规划器指向一个不存在的目录
// （数据在查询面上失联）。因此「先改成 ARCHIVED 权威态」是删除的前置条件，
// 而「没有 authority 就不删」本身也是一条必须被固定住的用例（见用例⑦b）。
func TestArchiveCopyReclaimsWhenVerifiedAndGatesOpen(t *testing.T) {
	p := threeGatePolicy()
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

	ex := NewArchiveExecutor(p, nil)
	ex.SetArchiveExporter(&fakeExporter{})
	auth := &fakeAuthority{}
	ex.SetArchiveAuthority(auth)
	rm := &fakeRemover{}
	ex.SetRemover(rm)

	out := ex.Execute(context.Background(), hv, archiveFixture())
	due := dueOutcomes(out)
	var copied int
	for _, o := range due {
		if o.Result != ArchiveCopied {
			t.Errorf("校验通过 + 三闸全开应记 %s，得到 %s（%s）", ArchiveCopied, o.Result, o.Target)
		}
		if o.TargetDirID == "" {
			t.Errorf("归档成功后必须记录「去哪」：%s", o.Target)
		}
		copied++
	}
	if copied == 0 {
		t.Fatal("前置条件：夹具应产出到期分区")
	}
	if len(rm.calls) != copied {
		t.Fatalf("每个校验通过的分区都应回收一次热层：期望 %d 得到 %d（%v）", copied, len(rm.calls), rm.calls)
	}
	// 次序不变量：必须先迁移权威态、再删（反序会让查询面短暂指向不存在的目录）。
	if len(auth.marked) != copied {
		t.Fatalf("每个回收的分区都必须先迁移到 ARCHIVED：期望 %d 得到 %d", copied, len(auth.marked))
	}
	for i := range auth.events {
		if i == 0 {
			continue
		}
	}
	// 每个分区三个事件，次序固定：freeze（导出前置）→ mark（改权威态）→ reclaimed（回收闭环）。
	if len(auth.events) != 3*copied {
		t.Fatalf("每个分区应各留 freeze/mark/reclaimed 三条审计：期望 %d 得到 %d（%v）",
			3*copied, len(auth.events), auth.events)
	}
	for i := 0; i < len(auth.events); i += 3 {
		if !strings.HasPrefix(auth.events[i], "freeze:") {
			t.Fatalf("第 %d 个事件应为 freeze（导出前置），得到 %q", i, auth.events[i])
		}
		if !strings.HasPrefix(auth.events[i+1], "mark:") {
			t.Fatalf("第 %d 个事件应为 mark（先改权威态再删），得到 %q", i+1, auth.events[i+1])
		}
		if !strings.HasPrefix(auth.events[i+2], "reclaimed:") {
			t.Fatalf("第 %d 个事件应为 reclaimed（后标记回收），得到 %q", i+2, auth.events[i+2])
		}
	}
	st := ex.Stats()
	if st.ArchiveCopied != int64(copied) || st.ArchiveCopyReclaimed != int64(copied) {
		t.Errorf("读数不符：copied=%d reclaimed=%d 期望 %d", st.ArchiveCopied, st.ArchiveCopyReclaimed, copied)
	}
	if st.ArchiveCopyKeptOriginal != 0 {
		t.Errorf("全部回收成功时不应有「保留原物」，得到 %d", st.ArchiveCopyKeptOriginal)
	}
	if st.ArchiveRolledBack != 0 {
		t.Errorf("全部回收成功时不应有回滚，得到 %d", st.ArchiveRolledBack)
	}
}

// 用例⑦b：三闸全开但没有归档权威态迁移出口 ⇒ **不删**。
//
// 这是本次改动新增的第二道判据，也是「数据在查询面上失联」的唯一防线：
// 没有 Catalog 侧的 ARCHIVED 权威态，删除就没有任何可核验的承接物。
func TestArchiveCopyWithoutAuthorityKeepsOriginal(t *testing.T) {
	p := threeGatePolicy()
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

	ex := NewArchiveExecutor(p, nil)
	ex.SetArchiveExporter(&fakeExporter{})
	rm := &fakeRemover{}
	ex.SetRemover(rm) // 删除出口在，但权威态迁移出口缺席

	ok, reason := ex.ArchiveReadyForReclaim()
	if ok {
		t.Fatal("前置条件：未装配 authority 时不得宣称可回收")
	}
	if !strings.Contains(reason, "权威态") {
		t.Errorf("原因应点名缺失的是权威态迁移出口，得到 %q", reason)
	}

	out := ex.Execute(context.Background(), hv, archiveFixture())
	if len(rm.calls) != 0 {
		t.Fatalf("没有归档权威态时**绝不能**删热层，实际删除 %v", rm.calls)
	}
	due := dueOutcomes(out)
	for _, o := range due {
		if o.Result != ArchiveCopiedKeptOriginal {
			t.Errorf("应落「导出成功但保留原物」，得到 %s（%s）", o.Result, o.Target)
		}
	}
}

// 用例⑦c：回收失败 ⇒ 必须回滚权威态，原物保留且如实上报。
//
// 反序或漏回滚会让 Catalog 长期宣称「已归档」而热层数据仍在：
// 那既不是「省了盘」也不是「数据可查」，而是一个谁也说不清的中间态。
func TestArchiveCopyReclaimFailureRollsBack(t *testing.T) {
	p := threeGatePolicy()
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

	ex := NewArchiveExecutor(p, nil)
	ex.SetArchiveExporter(&fakeExporter{})
	auth := &fakeAuthority{}
	ex.SetArchiveAuthority(auth)
	rm := &fakeRemover{failWith: errors.New("热层目录被占用（模拟）")}
	ex.SetRemover(rm)

	out := ex.Execute(context.Background(), hv, archiveFixture())
	due := dueOutcomes(out)
	if len(due) == 0 {
		t.Fatal("前置条件：夹具应产出到期分区")
	}
	for _, o := range due {
		if o.Result != ArchiveCopiedKeptOriginal {
			t.Errorf("回收失败应落「导出成功但保留原物」，得到 %s（%s）", o.Result, o.Target)
		}
		if o.Err == nil || !strings.Contains(o.Err.Error(), "已回滚为热层权威") {
			t.Errorf("必须如实上报已回滚，得到 %v", o.Err)
		}
	}
	if len(auth.rollbacks) != len(due) {
		t.Fatalf("每个回收失败都必须回滚一次：期望 %d 得到 %d（%v）", len(due), len(auth.rollbacks), auth.rollbacks)
	}
	if len(auth.reclaimed) != 0 {
		t.Errorf("回收失败时不得标记 reclaimed，得到 %v", auth.reclaimed)
	}
	st := ex.Stats()
	if st.ArchiveRolledBack != int64(len(due)) {
		t.Errorf("回滚读数应为 %d，得到 %d", len(due), st.ArchiveRolledBack)
	}
	if st.ArchiveCopyReclaimed != 0 {
		t.Errorf("回收失败不得记已回收，得到 %d", st.ArchiveCopyReclaimed)
	}
}

// 用例⑧：三闸全开但没有删除出口 ⇒ 保留原物（宁占盘不假报回收）。
//
// 与 ActionDiscard 的 RET-3 同一取舍：观测面不能报「已回收」而数据还在。
func TestArchiveCopyWithoutRemoverKeepsOriginal(t *testing.T) {
	p := threeGatePolicy()
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

	ex := NewArchiveExecutor(p, nil)
	ex.SetArchiveExporter(&fakeExporter{})
	// 刻意不装配 Remover

	out := ex.Execute(context.Background(), hv, archiveFixture())
	due := dueOutcomes(out)
	if len(due) == 0 {
		t.Fatal("前置条件：夹具应产出到期分区")
	}
	for _, o := range due {
		if o.Result != ArchiveCopiedKeptOriginal {
			t.Errorf("没有删除出口时必须落「保留原物」，得到 %s（%s）", o.Result, o.Target)
		}
	}
	if st := ex.Stats(); st.ArchiveCopyReclaimed != 0 {
		t.Errorf("没有删除出口时不得记已回收，得到 %d", st.ArchiveCopyReclaimed)
	}
}

// 用例⑨：三闸全开、删除出口也装配了，但回收本身失败 ⇒ 保留原物 + 告警。
func TestArchiveCopyReclaimFailureKeepsOriginal(t *testing.T) {
	p := threeGatePolicy()
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

	ex := NewArchiveExecutor(p, nil)
	ex.SetArchiveExporter(&fakeExporter{})
	ex.SetRemover(&fakeRemover{failWith: errors.New("热层目录被占用（模拟）")})

	out := ex.Execute(context.Background(), hv, archiveFixture())
	due := dueOutcomes(out)
	if len(due) == 0 {
		t.Fatal("前置条件：夹具应产出到期分区")
	}
	for _, o := range due {
		if o.Result != ArchiveCopiedKeptOriginal {
			t.Errorf("回收失败必须落「保留原物」，得到 %s（%s）", o.Result, o.Target)
		}
		if o.Err == nil {
			t.Errorf("回收失败必须带原因：%s", o.Target)
		}
	}
	if st := ex.Stats(); st.ArchiveCopyReclaimed != 0 {
		t.Errorf("回收失败不得记已回收，得到 %d", st.ArchiveCopyReclaimed)
	}
}

// 用例⑩：开关关闭时，即使装配了导出出口，计划也**不会**产出 archive_copy。
//
// 这是「默认值不改变行为」的守门用例：默认部署升级后，行为必须与改动前完全一致。
func TestArchiveCopyDisabledKeepsBlockedBehaviour(t *testing.T) {
	p := enabledPolicy() // archive_copy 关闭
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

	ex := NewArchiveExecutor(p, nil)
	x := &fakeExporter{}
	ex.SetArchiveExporter(x) // 出口在，但开关关着

	if ex.ArchiverAvailable() {
		t.Fatal("开关关闭时 ArchiverAvailable 必须为 false")
	}
	out := ex.Execute(context.Background(), hv, archiveFixture())
	for _, o := range out {
		if o.Target.Action != ActionBlocked {
			t.Errorf("开关关闭时计划应为 blocked，得到 %s（%s）", o.Target.Action, o.Target)
		}
	}
	if len(x.calls) != 0 {
		t.Errorf("开关关闭时不得调用导出出口，实际 %v", x.calls)
	}
}

// 用例⑪：开关打开但没有装配出口 ⇒ 计划仍为 blocked（不能承诺做不到的事）。
func TestArchiveCopyEnabledWithoutExporterStaysBlocked(t *testing.T) {
	p := archiveCopyPolicy()
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

	ex := NewArchiveExecutor(p, nil) // 没有导出出口
	out := ex.Execute(context.Background(), hv, archiveFixture())
	for _, o := range out {
		if o.Target.Action != ActionBlocked {
			t.Errorf("出口未装配时计划应为 blocked，得到 %s（%s）", o.Target.Action, o.Target)
		}
	}
}

// 用例⑫：可重试迁移缺少恢复入口时，归档出口**不能**把它救成 archive_copy。
//
// 导出归档是从冻结快照另存一份，既不推进也不修复 COLD 迁移状态机；
// 把「已导出」当成「迁移已续跑」会让一个真正卡住的分区从观测面上消失。
func TestArchiveCopyNeverSubstitutesForMigrationRetry(t *testing.T) {
	p := archiveCopyPolicy()
	part := PartitionRef{
		StorageNamespace: "inst:1", UTCDay: "20261001",
		Owner: OwnerColdName, InFlight: true, Retryable: true,
	}
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)

	plan := p.PlanArchive(now, []PartitionRef{part}, false, true)
	if len(plan) != 1 {
		t.Fatalf("可重试分区应进入计划，得到 %d 条", len(plan))
	}
	if plan[0].Action != ActionBlocked {
		t.Fatalf("导出归档无法续跑迁移状态机，应保持 blocked，得到 %s", plan[0].Action)
	}
	if !plan[0].Due {
		t.Fatal("可重试分区必须保持 Due=true，否则恢复永远不会被尝试")
	}
}

// 用例⑬：三闸判定的唯一来源口径（Policy.DeleteGatesOpen）。
//
// 单独测它是为了让「哪道闸没开」这句话本身成为契约：观测面与启动日志都靠它定位。
func TestDeleteGatesOpenReportsMissingGate(t *testing.T) {
	open := threeGatePolicy()
	if ok, reason := open.DeleteGatesOpen(); !ok {
		t.Fatalf("三闸全开时必须判定为开，得到 %q", reason)
	}

	for _, tc := range []struct {
		name string
		p    Policy
		want string
	}{
		{"缺意图闸", func() Policy { p := threeGatePolicy(); p.Discard = false; return p }(), "discard"},
		{"缺执行闸", func() Policy { p := threeGatePolicy(); p.Sweep.VLSweep = false; return p }(), "vl_sweep"},
		{"缺VL侧闸", func() Policy { p := threeGatePolicy(); p.Sweep.VLDeleteEnabled = false; return p }(), "delete_enabled"},
	} {
		ok, reason := tc.p.DeleteGatesOpen()
		if ok {
			t.Errorf("%s：必须判定为未全开", tc.name)
			continue
		}
		if !strings.Contains(reason, tc.want) {
			t.Errorf("%s：原因应点名缺失的那道闸（含 %q），得到 %q", tc.name, tc.want, reason)
		}
	}
}
