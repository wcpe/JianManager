package retention

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件守「归档对象自身的回收」（FR-501 的最后一块）。
//
// 每条用例都同时断言两件事：**该删的删了**、**不该删的一条没删**。
// 这条路径的失败模式不是「没清干净」（那只是盘没省下来），而是
// 「在不该删的时候删掉了唯一一份副本」——那时现场只表现为「这段日志查不到了」，
// 而平台还会理直气壮地告诉用户「已按保留期清除」。

// purgePolicy 是「归档对象回收已启用」的策略：3 闸仍按需在用例里打开。
func purgePolicy(retentionPeriod time.Duration) Policy {
	p := enabledPolicy()
	p.ArchiveObjectRetention = ArchiveObjectRetentionPolicy{
		Enabled:     true,
		Retention:   retentionPeriod,
		SweepAtHour: 3,
	}
	return p
}

// threeGatePurgePolicy 打开全部三闸（删除的唯一合法前提）。
func threeGatePurgePolicy(retentionPeriod time.Duration) Policy {
	p := purgePolicy(retentionPeriod)
	p.Discard = true
	p.Sweep.VLSweep = true
	p.Sweep.VLDeleteEnabled = true
	return p
}

const purgeRetention = 730 * 24 * time.Hour

// purgeNow 是判定基准时刻；夹具的归档时刻落在它之前 800 天（已过期）。
func purgeNow() time.Time { return time.Date(2027, 6, 1, 3, 0, 0, 0, time.UTC) }

// archivedRef 构造一个「已归档 + 热层已回收 + 归档时刻早于保留期」的候选。
func archivedRef(purged bool) ArchivedObjectRef {
	return ArchivedObjectRef{
		StorageNamespace: "inst:147",
		UTCDay:           "2025-03-01",
		ProviderKind:     "local",
		ObjectPrefix:     "inst:147/2025-03-01/g1",
		ArchivedAt:       purgeNow().Add(-800 * 24 * time.Hour),
		HotReclaimed:     true,
		Purged:           purged,
	}
}

// fakePurgeLister 返回固定候选（可注入枚举失败）。
type fakePurgeLister struct {
	refs []ArchivedObjectRef
	err  error
	// calls 记录枚举次数：用来断言「三闸未全开时连枚举都不做」。
	calls int
}

func (l *fakePurgeLister) ListArchivedObjects() ([]ArchivedObjectRef, error) {
	l.calls++
	if l.err != nil {
		return nil, l.err
	}
	return l.refs, nil
}

// fakeObjectStore 是可注入失败的归档对象存储。
type fakeObjectStore struct {
	mu sync.Mutex
	// objects 是「归档里还剩什么」（按前缀分）。
	objects map[string][]string
	deleted []string
	// listErr / deleteErr / removeErr 按前缀注入。
	listErr   error
	deleteErr map[string]error
	removeErr error
	removed   []string
}

func newFakeObjectStore(objects ...string) *fakeObjectStore {
	return &fakeObjectStore{
		objects:   map[string][]string{"inst:147/2025-03-01/g1": objects},
		deleteErr: map[string]error{},
	}
}

func (s *fakeObjectStore) ListArchiveObjects(_ context.Context, _ string, prefix string) ([]string, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.objects[prefix]...), nil
}

func (s *fakeObjectStore) DeleteArchiveObject(_ context.Context, _ string, prefix, relPath string) error {
	if err, ok := s.deleteErr[relPath]; ok {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, prefix+"/"+relPath)
	remaining := s.objects[prefix][:0]
	for _, o := range s.objects[prefix] {
		if o != relPath {
			remaining = append(remaining, o)
		}
	}
	s.objects[prefix] = remaining
	return nil
}

func (s *fakeObjectStore) RemoveArchivePartition(_ context.Context, _ string, prefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removed = append(s.removed, prefix)
	return s.removeErr
}

func (s *fakeObjectStore) deletedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.deleted)
}

// fakePurgeAuthority 记录三段式调用次序，可注入失败。
type fakePurgeAuthority struct {
	mu          sync.Mutex
	events      []string
	requested   []string
	purged      []string
	rolledBack  []string
	requestErr  error
	completeErr error
	rollbackErr error
	already     bool
}

func (a *fakePurgeAuthority) MarkPurgeRequested(_ context.Context, ns, day, _ string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.requestErr != nil {
		return false, a.requestErr
	}
	a.requested = append(a.requested, ns+"/"+day)
	a.events = append(a.events, "request:"+ns+"/"+day)
	return a.already, nil
}

func (a *fakePurgeAuthority) MarkPurged(_ context.Context, ns, day, _ string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.completeErr != nil {
		return a.completeErr
	}
	a.purged = append(a.purged, ns+"/"+day)
	a.events = append(a.events, "purged:"+ns+"/"+day)
	return nil
}

func (a *fakePurgeAuthority) RollbackPurge(_ context.Context, ns, day, _ string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rolledBack = append(a.rolledBack, ns+"/"+day)
	a.events = append(a.events, "rollback:"+ns+"/"+day)
	return a.rollbackErr
}

func (a *fakePurgeAuthority) snapshot() (events, purged, rolledBack []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.events...), append([]string(nil), a.purged...),
		append([]string(nil), a.rolledBack...)
}

// 契约：**三闸未全开时零删除**，且连候选都不枚举。
//
// 逐闸各测一次：只测「全关/全开」会漏掉某一道闸日后被悄悄绕过。
// 「连枚举都不做」是本实现有意的行为——枚举了再全部保留会让读数看起来像
// 「策略在跑、只是没删到期的」，运维会去查保留期而不是闸门。
func TestArchiveObjectPurgeRequiresAllThreeGates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Policy)
		gate   string
	}{
		{name: "三闸全关（默认）", gate: "discard"},
		{name: "缺意图闸 discard", mutate: func(p *Policy) { p.Sweep.VLSweep = true; p.Sweep.VLDeleteEnabled = true }, gate: "discard"},
		{name: "缺执行闸 sweep.vl_sweep", mutate: func(p *Policy) { p.Discard = true; p.Sweep.VLDeleteEnabled = true }, gate: "vl_sweep"},
		{name: "缺 VL 侧闸 log_vl.delete_enabled", mutate: func(p *Policy) { p.Discard = true; p.Sweep.VLSweep = true }, gate: "delete_enabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := purgePolicy(purgeRetention)
			if tc.mutate != nil {
				tc.mutate(&p)
			}
			if open, _ := p.DeleteGatesOpen(); open {
				t.Fatal("前置条件：本子用例必须处于「三闸未全开」状态")
			}

			lister := &fakePurgeLister{refs: []ArchivedObjectRef{archivedRef(false)}}
			store := newFakeObjectStore("raw/files/a.gz", "manifest.json")
			auth := &fakePurgeAuthority{}
			r := NewArchiveObjectReclaimer(p, lister, store, auth)

			res, err := r.Step(context.Background(), purgeNow())
			if err != nil {
				t.Fatalf("单轮执行失败：%v", err)
			}
			if !res.GatesClosed {
				t.Error("必须报告「被闸门挡住」（否则观测面看不出为什么一条没删）")
			}
			if !strings.Contains(res.GateReason, tc.gate) {
				t.Errorf("原因应点名缺失的那道闸（含 %q），得到 %q", tc.gate, res.GateReason)
			}
			if got := store.deletedCount(); got != 0 {
				t.Fatalf("三闸未全开时**绝不能**删除归档对象，实际删除 %d 个", got)
			}
			if lister.calls != 0 {
				t.Errorf("三闸未全开时连候选都不该枚举，实际枚举 %d 次", lister.calls)
			}
			events, purged, _ := auth.snapshot()
			if len(events) != 0 || len(purged) != 0 {
				t.Errorf("三闸未全开时不得触碰权威态，得到 %v", events)
			}
			st := r.Stats()
			if st.Purged != 0 {
				t.Errorf("未删除时不得记已清除，得到 %d", st.Purged)
			}
			if st.LastRunAt.IsZero() {
				t.Error("即便零行动也必须记下「跑过一轮」（否则抓取侧分不清「没跑」与「被闸门挡住」）")
			}
		})
	}
}

// 契约：未到保留期时零删除，且**不触碰权威态**。
//
// 保留期从**归档时刻**起算（不是热层回收时刻）：运维写 730d 的语义是
// 「这份归档副本留两年」，起点必须是它诞生那一刻。
func TestArchiveObjectPurgeSkipsBeforeRetention(t *testing.T) {
	p := threeGatePurgePolicy(purgeRetention)
	ref := archivedRef(false)
	// 归档时刻挪到「只过了 729 天」。
	ref.ArchivedAt = purgeNow().Add(-729 * 24 * time.Hour)

	lister := &fakePurgeLister{refs: []ArchivedObjectRef{ref}}
	store := newFakeObjectStore("raw/files/a.gz")
	auth := &fakePurgeAuthority{}
	r := NewArchiveObjectReclaimer(p, lister, store, auth)

	res, err := r.Step(context.Background(), purgeNow())
	if err != nil {
		t.Fatalf("单轮执行失败：%v", err)
	}
	if res.Due != 0 {
		t.Errorf("未到保留期不得判为到期，得到 due=%d", res.Due)
	}
	if got := store.deletedCount(); got != 0 {
		t.Fatalf("未到保留期不得删除任何对象，实际删除 %d 个", got)
	}
	events, _, _ := auth.snapshot()
	if len(events) != 0 {
		t.Errorf("未到保留期不得触碰权威态，得到 %v", events)
	}
	if st := r.Stats(); st.Purged != 0 || st.Kept == 0 {
		t.Errorf("读数应为 purged=0 且 kept=1，得到 %+v", st)
	}
}

// 契约：边界值——正好到保留期就该动手（差 1 秒不动）。
//
// 边界写成两条而不是一条：只测「未到点」会让 `<` 写成 `<=` 的缺陷漏网，
// 而那种缺陷会把每一份归档整整多留一天（730d 变成 731d）。
func TestArchiveObjectPurgeRetentionBoundary(t *testing.T) {
	for _, tc := range []struct {
		name       string
		archivedAt time.Time
		wantDue    bool
	}{
		{"差 1 秒到期", purgeNow().Add(-purgeRetention + time.Second), false},
		{"正好到期", purgeNow().Add(-purgeRetention), true},
		{"已过期 1 秒", purgeNow().Add(-purgeRetention - time.Second), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := threeGatePurgePolicy(purgeRetention)
			ref := archivedRef(false)
			ref.ArchivedAt = tc.archivedAt
			plan := p.PlanArchiveObjectPurge(purgeNow(), []ArchivedObjectRef{ref})
			if len(plan) != 1 {
				t.Fatalf("应产出 1 个判定，得到 %d", len(plan))
			}
			if plan[0].Due != tc.wantDue {
				t.Errorf("Due 应为 %v，得到 %v（%s）", tc.wantDue, plan[0].Due, plan[0])
			}
		})
	}
}

// 契约：删除中途失败 ⇒ **回滚权威态**，且绝不标记为已清除。
func TestArchiveObjectPurgeFailureRollsBack(t *testing.T) {
	p := threeGatePurgePolicy(purgeRetention)
	store := newFakeObjectStore("raw/files/a.gz", "raw/files/b.gz", "manifest.json")
	// 第 2 个对象删除失败。
	store.deleteErr["raw/files/b.gz"] = errors.New("目的地不可达（模拟）")
	auth := &fakePurgeAuthority{}
	r := NewArchiveObjectReclaimer(p, &fakePurgeLister{refs: []ArchivedObjectRef{archivedRef(false)}}, store, auth)

	res, err := r.Step(context.Background(), purgeNow())
	if err != nil {
		t.Fatalf("单轮执行失败：%v", err)
	}
	if res.Purged != 0 {
		t.Fatalf("删除失败不得记已清除，得到 purged=%d", res.Purged)
	}
	if res.RolledBack != 1 || res.Failed != 1 {
		t.Errorf("应记 rolledBack=1 且 failed=1，得到 rolledBack=%d failed=%d", res.RolledBack, res.Failed)
	}
	if len(res.Outcomes) != 1 || res.Outcomes[0].Result != ArchiveObjectPurgeRolledBack {
		t.Fatalf("结果应为 rolled_back，得到 %+v", res.Outcomes)
	}
	if res.Outcomes[0].Err == nil {
		t.Error("失败必须带原因")
	}

	events, purged, rolledBack := auth.snapshot()
	if len(purged) != 0 {
		t.Fatalf("失败时**绝不能**标记已清除，得到 %v", purged)
	}
	if len(rolledBack) != 1 {
		t.Fatalf("失败时必须回滚一次，得到 %v", rolledBack)
	}
	// 次序不变量：必须先落「请求」、再回滚；不能跳过请求直接回滚。
	if len(events) != 2 || !strings.HasPrefix(events[0], "request:") || !strings.HasPrefix(events[1], "rollback:") {
		t.Fatalf("事件次序应为 request → rollback，得到 %v", events)
	}
	// 已成功删掉的那个对象不会「恢复」——但权威态必须诚实地保持在未清除。
	if got := store.deletedCount(); got != 1 {
		t.Errorf("失败前应恰好删掉 1 个对象，得到 %d", got)
	}
	if st := r.Stats(); st.Purged != 0 || st.RolledBack != 1 || !strings.Contains(st.LastError, "b.gz") {
		t.Errorf("读数/告警不符：%+v", st)
	}
}

// 契约：**全部删成功才标记完成**，且次序是 request → 逐对象删 → purged。
func TestArchiveObjectPurgeMarksCompleteOnlyAfterAllDeletes(t *testing.T) {
	p := threeGatePurgePolicy(purgeRetention)
	store := newFakeObjectStore("raw/files/a.gz", "raw/files/b.gz", "manifest.json")
	auth := &fakePurgeAuthority{}
	r := NewArchiveObjectReclaimer(p, &fakePurgeLister{refs: []ArchivedObjectRef{archivedRef(false)}}, store, auth)

	res, err := r.Step(context.Background(), purgeNow())
	if err != nil {
		t.Fatalf("单轮执行失败：%v", err)
	}
	if res.Purged != 1 || res.Failed != 0 {
		t.Fatalf("应成功清除 1 个分区，得到 purged=%d failed=%d（%v）", res.Purged, res.Failed, res.Outcomes)
	}
	if got := res.Outcomes[0].Objects; got != 3 {
		t.Errorf("应删掉 3 个对象（两数据 + 一 manifest），得到 %d", got)
	}
	if got := store.deletedCount(); got != 3 {
		t.Errorf("存储侧应记录 3 次删除，得到 %d", got)
	}
	// 空目录清理必须被尝试（它不是必需的，但不该被忘掉——留着空目录会让
	// 「归档占多少盘」的核对多出一堆零字节目录）。
	if len(store.removed) != 1 {
		t.Errorf("清除完成后应尝试移除空的归档分区目录，得到 %v", store.removed)
	}

	events, purged, rolledBack := auth.snapshot()
	if len(rolledBack) != 0 {
		t.Fatalf("全部成功时不得回滚，得到 %v", rolledBack)
	}
	if len(purged) != 1 {
		t.Fatalf("必须标记已清除一次，得到 %v", purged)
	}
	if len(events) != 2 || !strings.HasPrefix(events[0], "request:") || !strings.HasPrefix(events[1], "purged:") {
		t.Fatalf("事件次序应为 request → purged，得到 %v", events)
	}
	if st := r.Stats(); st.Purged != 1 || st.LastRunAt.IsZero() {
		t.Errorf("读数不符：%+v", st)
	}
}

// 契约：`ListArchiveObjects` 返回**空**（目录已不存在 / 已删净）⇒ 幂等成功。
//
// 把它当失败会让一个已达成目标的分区永久卡在「清除进行中」，每轮重试、每轮报警。
// 但 `List` 返回**错误**必须算失败——「读不出来」不等于「里面没东西」。
func TestArchiveObjectPurgeEmptyListIsIdempotentSuccess(t *testing.T) {
	p := threeGatePurgePolicy(purgeRetention)
	store := newFakeObjectStore() // 没有任何对象
	auth := &fakePurgeAuthority{}
	r := NewArchiveObjectReclaimer(p, &fakePurgeLister{refs: []ArchivedObjectRef{archivedRef(false)}}, store, auth)

	res, err := r.Step(context.Background(), purgeNow())
	if err != nil {
		t.Fatalf("空目录必须算成功而不是失败：%v", err)
	}
	if res.Purged != 1 || res.Failed != 0 {
		t.Fatalf("空目录应判为幂等成功，得到 purged=%d failed=%d（%v）", res.Purged, res.Failed, res.Outcomes)
	}
	if got := res.Outcomes[0].Objects; got != 0 {
		t.Errorf("空目录不该报告删除了对象，得到 %d", got)
	}
	_, purged, _ := auth.snapshot()
	if len(purged) != 1 {
		t.Fatalf("空目录也必须落「已清除」终态（否则每轮都会被重新当作候选），得到 %v", purged)
	}
}

// 契约：`ListArchiveObjects` 返回**错误** ⇒ 回滚，零删除。
func TestArchiveObjectPurgeListFailureRollsBack(t *testing.T) {
	p := threeGatePurgePolicy(purgeRetention)
	store := newFakeObjectStore("raw/files/a.gz")
	store.listErr = errors.New("归档目的地不可读（模拟）")
	auth := &fakePurgeAuthority{}
	r := NewArchiveObjectReclaimer(p, &fakePurgeLister{refs: []ArchivedObjectRef{archivedRef(false)}}, store, auth)

	res, err := r.Step(context.Background(), purgeNow())
	if err != nil {
		t.Fatalf("单轮执行失败：%v", err)
	}
	if res.Purged != 0 || res.RolledBack != 1 {
		t.Fatalf("列举失败必须回滚且不得记清除，得到 purged=%d rolledBack=%d", res.Purged, res.RolledBack)
	}
	if got := store.deletedCount(); got != 0 {
		t.Fatalf("列举失败时不得删除任何对象，实际删除 %d", got)
	}
	_, purged, rolledBack := auth.snapshot()
	if len(purged) != 0 || len(rolledBack) != 1 {
		t.Fatalf("应回滚且不标完成，得到 purged=%v rolledBack=%v", purged, rolledBack)
	}
}

// 契约：已清除（Purged）的候选**一个对象都不删**。
//
// 这条分支防的是「Catalog 已清除但归档目录被人工恢复」：那种情况下必须尊重
// 权威记录（人工处置要走重新归档），而不是每轮把人工放回去的文件再删一遍。
func TestArchiveObjectPurgeSkipsAlreadyPurgedCandidate(t *testing.T) {
	p := threeGatePurgePolicy(purgeRetention)
	store := newFakeObjectStore("raw/files/a.gz")
	auth := &fakePurgeAuthority{}
	r := NewArchiveObjectReclaimer(p, &fakePurgeLister{refs: []ArchivedObjectRef{archivedRef(true)}}, store, auth)

	res, err := r.Step(context.Background(), purgeNow())
	if err != nil {
		t.Fatalf("单轮执行失败：%v", err)
	}
	if res.Due != 0 {
		t.Errorf("已清除的候选不该判为到期，得到 due=%d", res.Due)
	}
	if got := store.deletedCount(); got != 0 {
		t.Fatalf("已清除的候选不得再删任何对象，实际删除 %d", got)
	}
	events, _, _ := auth.snapshot()
	if len(events) != 0 {
		t.Errorf("已清除的候选不得触碰权威态，得到 %v", events)
	}
}

// 契约：权威记录显示「已清除」（already=true）时，**一个对象都不许删**。
//
// 与上一条不同：这里候选自己没有 Purged 标记（例如 Catalog 与候选之间有一次
// 更新竞态），但权威态出口在请求步就报「已经清过了」。此时必须尊重权威记录。
func TestArchiveObjectPurgeRespectsAuthorityAlreadyPurged(t *testing.T) {
	p := threeGatePurgePolicy(purgeRetention)
	store := newFakeObjectStore("raw/files/a.gz")
	auth := &fakePurgeAuthority{already: true}
	r := NewArchiveObjectReclaimer(p, &fakePurgeLister{refs: []ArchivedObjectRef{archivedRef(false)}}, store, auth)

	res, err := r.Step(context.Background(), purgeNow())
	if err != nil {
		t.Fatalf("单轮执行失败：%v", err)
	}
	if got := store.deletedCount(); got != 0 {
		t.Fatalf("权威记录说已清除时不得删任何对象，实际删除 %d", got)
	}
	if res.Purged != 0 {
		t.Errorf("幂等命中不该记「本轮清除了一个」，得到 %d", res.Purged)
	}
	if len(res.Outcomes) != 1 || res.Outcomes[0].Result != ArchiveObjectPurgeKept {
		t.Errorf("应记为 kept，得到 %+v", res.Outcomes)
	}
}

// 契约：请求步落盘失败 ⇒ **零删除**（先把权威态写下来才允许动手）。
//
// 反序（先删后改）会留下一个窗口：对象没了而 Catalog 仍说「归档里有」，
// 查询面照旧把用户引向 rehydrate——一条必然失败的死路。
func TestArchiveObjectPurgeRequestFailureDeletesNothing(t *testing.T) {
	p := threeGatePurgePolicy(purgeRetention)
	store := newFakeObjectStore("raw/files/a.gz")
	auth := &fakePurgeAuthority{requestErr: errors.New("journal 不可写（模拟）")}
	r := NewArchiveObjectReclaimer(p, &fakePurgeLister{refs: []ArchivedObjectRef{archivedRef(false)}}, store, auth)

	res, err := r.Step(context.Background(), purgeNow())
	if err != nil {
		t.Fatalf("单轮执行失败：%v", err)
	}
	if got := store.deletedCount(); got != 0 {
		t.Fatalf("权威态没落盘时**绝不能**删除对象，实际删除 %d", got)
	}
	if res.Purged != 0 || res.Failed != 1 {
		t.Fatalf("应记为失败，得到 purged=%d failed=%d", res.Purged, res.Failed)
	}
	if len(res.Outcomes) != 1 || res.Outcomes[0].Result != ArchiveObjectPurgeFailed {
		t.Fatalf("结果应为 failed（不是 rolled_back：根本没开始删），得到 %+v", res.Outcomes)
	}
}

// 契约：对象删完了但**终态标记没落盘** ⇒ 本轮不得记成「已清除」。
//
// 对象确实已经没了（数据面达成目标），但 Catalog 不承认这个终态；下一轮会重试
// （重复删除是幂等的）。谎报已清除会让观测面报出一个 Catalog 并不承认的状态。
func TestArchiveObjectPurgeCompleteFailureIsNotReportedAsPurged(t *testing.T) {
	p := threeGatePurgePolicy(purgeRetention)
	store := newFakeObjectStore("raw/files/a.gz")
	auth := &fakePurgeAuthority{completeErr: errors.New("journal 不可写（模拟）")}
	r := NewArchiveObjectReclaimer(p, &fakePurgeLister{refs: []ArchivedObjectRef{archivedRef(false)}}, store, auth)

	res, err := r.Step(context.Background(), purgeNow())
	if err != nil {
		t.Fatalf("单轮执行失败：%v", err)
	}
	if res.Purged != 0 {
		t.Fatalf("终态没落盘就不得记已清除，得到 %d", res.Purged)
	}
	if res.Failed != 1 {
		t.Fatalf("应记为失败，得到 failed=%d", res.Failed)
	}
	if got := store.deletedCount(); got != 1 {
		t.Errorf("删除本身已经发生（1 个对象），得到 %d", got)
	}
	if st := r.Stats(); !strings.Contains(st.LastError, "清除标记落盘失败") {
		t.Errorf("告警必须说明是「标记没落盘」而不是「删除失败」：%q", st.LastError)
	}
}

// 契约：**回滚也失败**时两个错误都要报出来（回滚失败意味着停在「清除进行中」）。
func TestArchiveObjectPurgeRollbackFailureSurfacesBothErrors(t *testing.T) {
	p := threeGatePurgePolicy(purgeRetention)
	store := newFakeObjectStore("raw/files/a.gz")
	store.deleteErr["raw/files/a.gz"] = errors.New("permission denied（模拟）")
	auth := &fakePurgeAuthority{rollbackErr: errors.New("journal 不可写（模拟）")}
	r := NewArchiveObjectReclaimer(p, &fakePurgeLister{refs: []ArchivedObjectRef{archivedRef(false)}}, store, auth)

	res, err := r.Step(context.Background(), purgeNow())
	if err != nil {
		t.Fatalf("单轮执行失败：%v", err)
	}
	if len(res.Outcomes) != 1 {
		t.Fatalf("应产出 1 个结果，得到 %d", len(res.Outcomes))
	}
	out := res.Outcomes[0]
	if out.Result != ArchiveObjectPurgeFailed {
		t.Fatalf("回滚失败时应记为 failed（而非 rolled_back），得到 %s", out.Result)
	}
	if out.Err == nil || !strings.Contains(out.Err.Error(), "permission denied") ||
		!strings.Contains(out.Err.Error(), "回滚失败") {
		t.Fatalf("必须同时报出删除失败与回滚失败，得到 %v", out.Err)
	}
	// 计数口径：回滚失败**不**计入 rolledBack（那会让「已退回未清除」的数字虚高）。
	if res.RolledBack != 0 || res.Failed != 1 {
		t.Errorf("计数口径不符：rolledBack=%d failed=%d", res.RolledBack, res.Failed)
	}
}

// 契约：候选带阻塞原因（来自 Catalog 复核）时**一条都不删**，且原因被透传到计划里。
//
// 这是「观测面与执行侧同一套判据」的守门用例：适配层把 Catalog 的结论以
// BlockedReason 形式带过来，策略层必须尊重它，而不是自己再判一遍（两处判会分叉）。
func TestArchiveObjectPurgeHonoursCatalogBlockedReason(t *testing.T) {
	p := threeGatePurgePolicy(purgeRetention)
	ref := archivedRef(false)
	ref.BlockedReason = "分区存在待恢复标记，归档对象仍是唯一可用副本"
	store := newFakeObjectStore("raw/files/a.gz")
	auth := &fakePurgeAuthority{}
	r := NewArchiveObjectReclaimer(p, &fakePurgeLister{refs: []ArchivedObjectRef{ref}}, store, auth)

	res, err := r.Step(context.Background(), purgeNow())
	if err != nil {
		t.Fatalf("单轮执行失败：%v", err)
	}
	if got := store.deletedCount(); got != 0 {
		t.Fatalf("有阻塞原因时不得删除任何对象，实际删除 %d", got)
	}
	if res.Due != 0 || res.Kept != 1 {
		t.Errorf("应记 kept=1 且 due=0，得到 due=%d kept=%d", res.Due, res.Kept)
	}
	if !strings.Contains(res.Outcomes[0].Target.Reason, "待恢复") {
		t.Errorf("阻塞原因必须透传到计划里（运维才看得懂为什么没删），得到 %q",
			res.Outcomes[0].Target.Reason)
	}
}

// 契约：开关关闭时**完全不动作**（连候选都不枚举）。
func TestArchiveObjectPurgeDisabledIsNoop(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Policy)
	}{
		{"enabled=false（默认）", func(p *Policy) { p.ArchiveObjectRetention.Enabled = false }},
		{"enabled=true 但保留期为空", func(p *Policy) { p.ArchiveObjectRetention.Retention = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := threeGatePurgePolicy(purgeRetention)
			tc.mutate(&p)

			lister := &fakePurgeLister{refs: []ArchivedObjectRef{archivedRef(false)}}
			store := newFakeObjectStore("raw/files/a.gz")
			r := NewArchiveObjectReclaimer(p, lister, store, &fakePurgeAuthority{})

			res, err := r.Step(context.Background(), purgeNow())
			if err != nil {
				t.Fatalf("未启用时不该报错：%v", err)
			}
			if res.Listed != 0 || res.Purged != 0 || res.GatesClosed {
				t.Errorf("未启用时必须是彻底空操作，得到 %+v", res)
			}
			if lister.calls != 0 {
				t.Errorf("未启用时不该枚举候选，实际 %d 次", lister.calls)
			}
			if st := r.Stats(); st.LastRunAt.IsZero() == false {
				// 未启用时**不该**记「跑过一轮」：那会让抓取侧以为策略在运行。
				t.Errorf("未启用时不该记录运行时刻，得到 %v", st.LastRunAt)
			}
		})
	}
}

// 契约：枚举候选失败要**如实返回错误**（「没有归档分区」与「读不出来」是两件事）。
func TestArchiveObjectPurgeListCandidatesFailureSurfaces(t *testing.T) {
	p := threeGatePurgePolicy(purgeRetention)
	r := NewArchiveObjectReclaimer(p, &fakePurgeLister{err: errors.New("catalog 不可读（模拟）")},
		newFakeObjectStore(), &fakePurgeAuthority{})

	if _, err := r.Step(context.Background(), purgeNow()); err == nil {
		t.Fatal("枚举候选失败必须返回错误（不得静默成「没有分区」）")
	}
}

// 契约：Ready 必须点名缺哪个出口（装配不全等于「配了但什么都不删」）。
func TestArchiveObjectReclaimerReadyNamesMissingOutlet(t *testing.T) {
	p := threeGatePurgePolicy(purgeRetention)
	lister := &fakePurgeLister{}
	store := newFakeObjectStore()
	authority := &fakePurgeAuthority{}

	for _, tc := range []struct {
		name string
		r    *ArchiveObjectReclaimer
		want string
	}{
		{"齐全", NewArchiveObjectReclaimer(p, lister, store, authority), ""},
		{"缺枚举出口", NewArchiveObjectReclaimer(p, nil, store, authority), "枚举"},
		{"缺存储出口", NewArchiveObjectReclaimer(p, lister, nil, authority), "存储"},
		{"缺权威态出口", NewArchiveObjectReclaimer(p, lister, store, nil), "权威态"},
	} {
		ok, reason := tc.r.Ready()
		if tc.want == "" {
			if !ok {
				t.Errorf("%s：应判为齐全，得到 %q", tc.name, reason)
			}
			continue
		}
		if ok {
			t.Errorf("%s：必须判为不齐全", tc.name)
			continue
		}
		if !strings.Contains(reason, tc.want) {
			t.Errorf("%s：原因应含 %q，得到 %q", tc.name, tc.want, reason)
		}
	}
}

// 契约：出口未装配时 Due 目标落「失败」而不是静默跳过（不假装成功）。
func TestArchiveObjectPurgeWithoutStoreFailsLoudly(t *testing.T) {
	p := threeGatePurgePolicy(purgeRetention)
	r := NewArchiveObjectReclaimer(p, &fakePurgeLister{refs: []ArchivedObjectRef{archivedRef(false)}}, nil, nil)

	res, err := r.Step(context.Background(), purgeNow())
	if err != nil {
		t.Fatalf("单轮执行失败：%v", err)
	}
	if res.Purged != 0 {
		t.Fatalf("出口未装配时不得记已清除，得到 %d", res.Purged)
	}
	if res.Failed != 1 {
		t.Fatalf("出口未装配必须如实记失败，得到 failed=%d", res.Failed)
	}
	if st := r.Stats(); st.LastError == "" {
		t.Error("必须留下告警文本（否则只有一个不动的失败计数）")
	}
}

// 契约：PlanArchiveObjectPurge 是纯函数（不看三闸、不看开关），且判定次序正确。
//
// 「不看闸门」是有意的：把闸门塞进纯函数会让「到期了但闸门没开」与
// 「压根没到期」在计划层面无法区分，而运维最需要知道的是「有多少天在等闸门」。
func TestPlanArchiveObjectPurgeIsPureAndOrdered(t *testing.T) {
	p := purgePolicy(purgeRetention) // 三闸全关
	now := purgeNow()

	due := archivedRef(false)
	notDue := archivedRef(false)
	notDue.UTCDay = "2027-05-31"
	notDue.ArchivedAt = now.Add(-10 * 24 * time.Hour)
	purged := archivedRef(true)
	purged.UTCDay = "2025-01-01"
	blocked := archivedRef(false)
	blocked.UTCDay = "2025-02-01"
	blocked.BlockedReason = "热层副本尚未回收"

	plan := p.PlanArchiveObjectPurge(now, []ArchivedObjectRef{notDue, due, purged, blocked})
	if len(plan) != 4 {
		t.Fatalf("应产出 4 条判定，得到 %d", len(plan))
	}
	byDay := map[string]ArchivePurgeTarget{}
	for _, t2 := range plan {
		byDay[t2.Ref.UTCDay] = t2
	}
	if !byDay["2025-03-01"].Due {
		t.Errorf("已过期且无阻塞的分区必须判为到期：%s", byDay["2025-03-01"])
	}
	if byDay["2027-05-31"].Due {
		t.Errorf("未到保留期不得判为到期：%s", byDay["2027-05-31"])
	}
	if byDay["2025-01-01"].Due {
		t.Errorf("已清除的分区不得再判为到期：%s", byDay["2025-01-01"])
	}
	if byDay["2025-02-01"].Due {
		t.Errorf("有阻塞原因的分区不得判为到期：%s", byDay["2025-02-01"])
	}
	// 原因文案必须可读（运维靠它判断「为什么今天没删」）。
	for _, t2 := range plan {
		if t2.Reason == "" {
			t.Errorf("每条判定都必须带原因：%s", t2)
		}
	}
}

// 契约：缺少归档时刻的候选不猜（不删）。
//
// 没有起点就无从计算保留期。猜短了会提前删掉仍有义务保留的副本；
// 猜长了等于永远不清——两者都比「这一轮先不动、等人看」差。
func TestArchiveObjectPurgeSkipsCandidateWithoutArchivedAt(t *testing.T) {
	p := threeGatePurgePolicy(purgeRetention)
	ref := archivedRef(false)
	ref.ArchivedAt = time.Time{}

	store := newFakeObjectStore("raw/files/a.gz")
	r := NewArchiveObjectReclaimer(p, &fakePurgeLister{refs: []ArchivedObjectRef{ref}}, store, &fakePurgeAuthority{})
	if _, err := r.Step(context.Background(), purgeNow()); err != nil {
		t.Fatalf("单轮执行失败：%v", err)
	}
	if got := store.deletedCount(); got != 0 {
		t.Fatalf("缺少归档时刻时不得删除任何对象，实际删除 %d", got)
	}
}

// 契约：`ArchiveObjectRetentionPolicy.Active()` 是两个条件的与。
func TestArchiveObjectRetentionActiveRequiresBoth(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    ArchiveObjectRetentionPolicy
		want bool
	}{
		{"关闭", ArchiveObjectRetentionPolicy{Enabled: false, Retention: time.Hour}, false},
		{"开了但无保留期", ArchiveObjectRetentionPolicy{Enabled: true}, false},
		{"开了且给了保留期", ArchiveObjectRetentionPolicy{Enabled: true, Retention: time.Hour}, true},
	} {
		if got := tc.p.Active(); got != tc.want {
			t.Errorf("%s：Active() 应为 %v，得到 %v", tc.name, tc.want, got)
		}
	}
}

// 契约：越界的小时数回退默认（配错就按默认时刻跑，而不是 0 点跑）。
func TestArchiveObjectRetentionNormalizeSweepAtHour(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want int
	}{
		{in: 24, want: DefaultSweepAtHour},
		{in: -1, want: DefaultSweepAtHour},
		{in: 0, want: 0},
		{in: 23, want: 23},
	} {
		got := ArchiveObjectRetentionPolicy{SweepAtHour: tc.in}.Normalize().SweepAtHour
		if got != tc.want {
			t.Errorf("sweep_at_hour=%d 应归一为 %d，得到 %d", tc.in, tc.want, got)
		}
	}
}

// 契约：候选自带「热层未回收 / 待恢复标记」时，纯函数**自己**就拒绝，不依赖调用方。
//
// 为什么要在两处各判一次：`BlockedReason` 由适配层（Catalog 复核）填。若判据只活在那里，
// 任何新调用方（运维端点、别的调度器、将来的热更路径）漏填一次，就会静默变成
// 「删掉了那天唯一一份副本」。纯函数里重述一遍的代价是几行，收益是这道闸不再
// 取决于调用方的自觉。
func TestPlanArchiveObjectPurgeRefusesSelfEvidentBlockers(t *testing.T) {
	p := threeGatePurgePolicy(purgeRetention)

	notReclaimed := archivedRef(false)
	notReclaimed.HotReclaimed = false
	recoveryRequired := archivedRef(false)
	recoveryRequired.UTCDay = "2025-02-01"
	recoveryRequired.RecoveryRequired = true

	plan := p.PlanArchiveObjectPurge(purgeNow(), []ArchivedObjectRef{notReclaimed, recoveryRequired})
	if len(plan) != 2 {
		t.Fatalf("应产出 2 条判定，得到 %d", len(plan))
	}
	// 计划按 (namespace, day) 排序，故按日期取而不是按下标取——
	// 用下标会让用例依赖排序细节，改排序就假红。
	byDay := map[string]ArchivePurgeTarget{}
	for _, target := range plan {
		byDay[target.Ref.UTCDay] = target
		if target.Due {
			t.Errorf("自带阻塞条件的候选不得判为到期（即便 BlockedReason 为空）：%s", target)
		}
	}
	if got := byDay["2025-03-01"]; !strings.Contains(got.Reason, "热层") {
		t.Errorf("热层未回收的原因应点名热层，得到 %q", got.Reason)
	}
	if got := byDay["2025-02-01"]; !strings.Contains(got.Reason, "待恢复") {
		t.Errorf("待恢复标记的原因应点名它，得到 %q", got.Reason)
	}

	// 执行侧同样不得动手（两道闸各守一层，这里确认最终效果）。
	store := newFakeObjectStore("raw/files/a.gz")
	auth := &fakePurgeAuthority{}
	r := NewArchiveObjectReclaimer(p, &fakePurgeLister{refs: []ArchivedObjectRef{notReclaimed}}, store, auth)
	if _, err := r.Step(context.Background(), purgeNow()); err != nil {
		t.Fatalf("单轮执行失败：%v", err)
	}
	if got := store.deletedCount(); got != 0 {
		t.Fatalf("热层未回收时不得删除任何对象，实际删除 %d", got)
	}
	if _, purged, _ := auth.snapshot(); len(purged) != 0 {
		t.Fatalf("热层未回收时不得标记已清除，得到 %v", purged)
	}
}

// 契约：**保留期为空时一天都不判到期**——即便策略对象是手工构造的（绕过配置校验）。
//
// 这条咬的是一类隐蔽的默认值注入：只要有人让 `ArchiveObjectRetentionPolicy.Normalize()`
// 或 `Policy.Normalize()` 在 Retention 为空时补一个默认值（730d 是典型的候选），
// `PlanArchiveObjectPurge` 就会把每一份归档都判成「那个数字之后到期」——
// 而运维从未说过这个数字，现场也看不出它是被平台补上的。
//
// 配置层已对「开了开关但没保留期」启动即拒，但策略对象也可以经热更/测试构造出来，
// 故执行侧必须自己兜住：**没有保留期 ⇒ 不判到期**（fail-closed 的最后一道）。
func TestPlanArchiveObjectPurgeWithoutRetentionNeverDue(t *testing.T) {
	p := threeGatePurgePolicy(0) // 三闸全开，但保留期为 0
	if p.ArchiveObjectRetention.Active() {
		t.Fatal("前置条件：无保留期时 Active 必须为假")
	}
	plan := p.PlanArchiveObjectPurge(purgeNow(), []ArchivedObjectRef{archivedRef(false)})
	if len(plan) != 1 {
		t.Fatalf("应产出 1 个判定，得到 %d", len(plan))
	}
	if plan[0].Due {
		t.Fatalf("没有保留期时**不得**判为到期（否则会在一个没人说过的时间点开始删归档）：%s", plan[0])
	}
	if !strings.Contains(plan[0].Reason, "保留期") {
		t.Errorf("原因应说明缺少保留期，得到 %q", plan[0].Reason)
	}

	// 同理：Normalize 之后也不得凭空长出一个保留期（那正是「默认值注入」的形态）。
	if n := p.Normalize(); n.ArchiveObjectRetention.Retention != 0 {
		t.Fatalf("Normalize 不得凭空补出归档对象保留期，得到 %s", n.ArchiveObjectRetention.Retention)
	}
	if n := p.ArchiveObjectRetention.Normalize(); n.Retention != 0 {
		t.Fatalf("ArchiveObjectRetentionPolicy.Normalize 不得凭空补出保留期，得到 %s", n.Retention)
	}
}

// 契约：Normalize 的 Policy **不**为归档对象保留期补默认值。
//
// 补一个 730d 意味着「只要打开 enabled，两年后就会无条件开始删归档」——
// 而「留多久」是合规/成本口径，必须由运维说出口。
func TestPolicyNormalizeDoesNotInventArchiveObjectRetention(t *testing.T) {
	p := Policy{Enabled: true, ArchiveObjectRetention: ArchiveObjectRetentionPolicy{Enabled: true}}
	n := p.Normalize()
	if n.ArchiveObjectRetention.Retention != 0 {
		t.Fatalf("不得为归档对象保留期补默认值，得到 %s", n.ArchiveObjectRetention.Retention)
	}
	if n.ArchiveObjectRetention.Active() {
		t.Fatal("无保留期时 Active 必须为假（否则会在猜出来的到期日删数据）")
	}
}

// 契约：Validate 拒绝负的归档对象保留期（与其它保留期同口径）。
func TestPolicyValidateRejectsNegativeArchiveObjectRetention(t *testing.T) {
	p := purgePolicy(purgeRetention)
	p.ArchiveObjectRetention.Retention = -time.Hour
	err := p.Validate()
	if err == nil {
		t.Fatal("负的归档对象保留期必须被拒")
	}
	if !strings.Contains(err.Error(), "archive_object_retention") {
		t.Errorf("错误应点名具体键，得到 %v", err)
	}
}
