package ingest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func sixFixEvent(source SourceConfig, day string, start, end uint64, message string) logtypes.Event {
	return logtypes.BuildEvent(logtypes.SourceIdentity{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1"}, logtypes.RecordRange{Start: start, End: end}, day+"T12:00:00Z", day+"T12:00:01Z", "INFO", "stdout", message)
}

func TestIngestSixFixMaterializationSourceDayMatrix(t *testing.T) {
	for _, state := range []string{"unknown", "written", "verified", "published"} {
		t.Run(state, func(t *testing.T) {
			m, err := newTestManager(t, Options{Root: t.TempDir(), Catalog: catalog.New(nil), MaterializationLimits: &stateindex.MaterializationLimits{WindowCopies: 2, HourlyCopies: 3, HourlyBytes: 1 << 20}})
			require.NoError(t, err)
			source := SourceConfig{LogSourceID: "matrix", SourceGeneration: "g1", UTCDay: "2026-10-22"}
			events := []logtypes.Event{sixFixEvent(source, source.UTCDay, 0, 10, "稳定窗口")}
			for copy := 0; copy < 2; copy++ {
				row, release, err := m.reserveMaterialization(context.Background(), source, fmt.Sprintf("copy-%d", copy), events)
				require.NoError(t, err)
				release()
				require.NoError(t, m.updateMaterialization(context.Background(), row, state, state == "verified" || state == "published", state == "published", "成功、失败、未知均不退款"))
			}
			_, release, err := m.reserveMaterialization(context.Background(), source, "copy-over", events)
			if release != nil {
				release()
			}
			require.ErrorIs(t, err, ErrMaterializationBudget)
			for _, other := range []SourceConfig{{LogSourceID: "other", SourceGeneration: "g1", UTCDay: source.UTCDay}, {LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, UTCDay: "2026-10-23"}} {
				row, release, err := m.reserveMaterialization(context.Background(), other, "fresh", events)
				require.NoError(t, err)
				require.Positive(t, row.ID)
				release()
			}
		})
	}
}

func TestIngestSixFixMaterializationBudgetSurvivesThreeOpens(t *testing.T) {
	root := t.TempDir()
	source := SourceConfig{LogSourceID: "restart-budget", SourceGeneration: "g1", UTCDay: "2026-10-22"}
	events := []logtypes.Event{sixFixEvent(source, source.UTCDay, 0, 10, "重启不退款")}
	for open := 0; open < 3; open++ {
		m, err := New(Options{Root: root, Catalog: catalog.New(nil), MaterializationLimits: &stateindex.MaterializationLimits{WindowCopies: 2, HourlyCopies: 3, HourlyBytes: 1 << 20}})
		require.NoError(t, err)
		row, release, reserveErr := m.reserveMaterialization(context.Background(), source, fmt.Sprintf("generation-%d", open), events)
		if release != nil {
			release()
		}
		if open < 2 {
			require.NoError(t, reserveErr)
			require.Positive(t, row.ID)
		} else {
			require.ErrorIs(t, reserveErr, ErrMaterializationBudget)
		}
		rows, listErr := m.index.ListMaterializations(context.Background(), source.LogSourceID+"/g1", source.UTCDay, "", 0, 200)
		require.NoError(t, listErr)
		require.Len(t, rows, min(open+1, 2))
		require.NoError(t, m.Stop())
	}
}

func TestIngestSixFixHourlyBudgetCannotBeBypassedByNewContent(t *testing.T) {
	m, err := newTestManager(t, Options{Root: t.TempDir(), Catalog: catalog.New(nil), MaterializationLimits: &stateindex.MaterializationLimits{WindowCopies: 4, HourlyCopies: 2, HourlyBytes: 1 << 20}})
	require.NoError(t, err)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	m.rebuilds.now = func() time.Time { return now }
	source := SourceConfig{LogSourceID: "hourly", SourceGeneration: "g1", UTCDay: "2026-10-22"}
	for i := 0; i < 3; i++ {
		events := []logtypes.Event{sixFixEvent(source, source.UTCDay, 0, 10, fmt.Sprintf("新内容%d", i))}
		_, release, err := m.reserveMaterialization(context.Background(), source, fmt.Sprintf("gen-%d", i), events)
		if release != nil {
			release()
		}
		if i < 2 {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, ErrMaterializationBudget)
		}
	}
	now = now.Add(time.Hour + time.Nanosecond)
	_, release, err := m.reserveMaterialization(context.Background(), source, "after-hour", []logtypes.Event{sixFixEvent(source, source.UTCDay, 0, 10, "新窗口")})
	require.NoError(t, err)
	release()
}

func TestIngestSixFixEvidenceFailureAcrossDaysAndRecovery(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := hotRetentionTestSource(t, root)
	oldDay := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02")
	events := []logtypes.Event{hotRetentionTestEvent(oldDay, 0, 10, "昨天"), hotRetentionTestEvent(source.UTCDay, 10, 20, "今天")}
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer broken.Close()
	badClient, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: broken.URL})
	require.NoError(t, err)
	m.vlReadRoute = func(s SourceConfig, _ *catalog.Record) (*vlsup.Client, error) {
		if s.UTCDay == source.UTCDay {
			return badClient, nil
		}
		return client, nil
	}
	require.Empty(t, m.landedVisibleInVL(source, events), "前一天成功、当天503也不能返原整批")
	key := source.LogSourceID + "/" + source.SourceGeneration
	p := m.pipes[key]
	require.NoError(t, p.Ledger().RecordGap(p.Key(), 0, 20, ledger.GapReasonDeliverError, "跨天失败"))
	m.resolveGapsLandedByDelivery(source, events)
	require.Equal(t, 1, p.Ledger().UnresolvedGapCount(p.Key()))
	require.Empty(t, m.verifiedRuns(key))
	m.vlReadRoute = func(SourceConfig, *catalog.Record) (*vlsup.Client, error) { return client, nil }
	m.resolveGapsLandedByDelivery(source, events)
	require.Zero(t, p.Ledger().UnresolvedGapCount(p.Key()), "只有服务恢复且逐字段证据成立后才能消解")
	require.NoError(t, m.persist())
	loaded, err := m.loadStateFromIndex()
	require.NoError(t, err)
	require.Equal(t, []ledger.PositionRange{{From: 0, To: 20}}, loaded.Sources[key].VerifiedRuns)
	require.Equal(t, 2, fixture.rowCount())
}

func TestIngestSixFixDeferredPersistFailureNeverSends(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	require.NoError(t, os.WriteFile(source.Path, []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: boundary\n"), 0600))
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}, PersistHotBudget: time.Millisecond})
	require.NoError(t, err)
	p := m.pipes[source.LogSourceID+"/"+source.SourceGeneration]
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m.persistStepHold = func() { once.Do(func() { close(entered); <-release }) }
	require.NoError(t, p.Ledger().PauseAcquire(p.Key(), "触发持久化计划"))
	require.NoError(t, p.Ledger().ResumeAcquire(p.Key()))
	finished := make(chan error, 1)
	go func() { finished <- m.persist() }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("持久化屏障未到达")
	}
	_, pollErr := p.Poll()
	require.Error(t, pollErr, "交办并非已完成持久化")
	require.Zero(t, fixture.insertAttemptCount())
	require.NoError(t, m.index.Close())
	close(release)
	require.Error(t, <-finished, "真实异步持久化失败不得授权外发")
	_, deliverErr := p.DeliverPending()
	require.Error(t, deliverErr)
	require.Zero(t, fixture.insertAttemptCount())
	require.NotEmpty(t, p.WAL().Snapshot(), "正文责任必须原样留在WAL")
}

func TestIngestSixFixDeferredPersistQuietSourceDrainsWithoutNewInput(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	m, err := New(Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}, PersistHotBudget: time.Millisecond, VerificationTimeout: time.Second})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	started := false
	defer func() {
		cancel()
		if started {
			<-done
		}
		require.NoError(t, m.Stop())
	}()
	m.SetSelfHealInterval(time.Millisecond)
	p := m.pipes[source.LogSourceID+"/"+source.SourceGeneration]
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m.persistStepHold = func() { once.Do(func() { close(entered); <-release }) }
	require.NoError(t, p.Ledger().PauseAcquire(p.Key(), "建立持久化屏障"))
	require.NoError(t, p.Ledger().ResumeAcquire(p.Key()))
	persisted := make(chan error, 1)
	go func() { persisted <- m.persist() }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("真实持久化屏障未进入")
	}
	// 与真实短验相同：766条不足以暂停源，但超出单次500条排空，且之后不再有新输入。
	batch := make([]logtypes.Event, 0, 766)
	for i := 0; i < 766; i++ {
		batch = append(batch, hotRetentionTestEvent(source.UTCDay, uint64(i*10), uint64(i*10+10), fmt.Sprintf("交办后静默%d", i)))
	}
	require.NoError(t, p.WAL().Append(batch...))
	require.NoError(t, p.WAL().Commit())
	_, err = p.DeliverPending()
	require.ErrorIs(t, err, ErrPersistDeferred)
	require.Zero(t, fixture.insertAttemptCount(), "交办尚未落库时必须零外发")
	entry := p.Ledger().Get(p.Key())
	require.False(t, entry.AcquirePaused)
	require.Zero(t, ledger.UnresolvedGapCountOf(entry), "静默重试不得依赖伪造缺口")
	close(release)
	require.NoError(t, <-persisted)
	started = true
	go func() { defer close(done); m.Start(ctx) }()
	require.Eventually(t, func() bool {
		pos, ok := p.Positions()
		return ok && pos.Delivery == batch[len(batch)-1].Record.End && pos.Reclaim == pos.Delivery
	}, 5*time.Second, 10*time.Millisecond, "无暂停、无缺口、无新输入的耐久欠账必须被已有维护路径完整排空")
	require.Equal(t, len(batch), fixture.rowCount(), "真实VL写入不能丢失、重复或只排空第一批")
	backlog, _ := p.WAL().Backlog()
	require.Zero(t, backlog)
	loaded, err := m.loadStateFromIndex()
	require.NoError(t, err)
	require.NotEmpty(t, loaded.Sources[p.Key().String()].VerifiedRuns)
}

func TestIngestSixFixReplaySubsetDoesNotPublishOrAckFutureBodies(t *testing.T) {
	for _, check := range []string{"published-prefix", "local-body-is-not-ack"} {
		t.Run(check, func(t *testing.T) {
			client, fixture, _ := newReconcileVL(t)
			root := t.TempDir()
			source := hotRetentionTestSource(t, root)
			m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}, MaxReplayEventsPerDrain: 2})
			require.NoError(t, err)
			p := m.pipes[source.LogSourceID+"/"+source.SourceGeneration]
			batch := make([]logtypes.Event, 0, 5)
			for i := 0; i < 5; i++ {
				batch = append(batch, hotRetentionTestEvent(source.UTCDay, uint64(i*10), uint64(i*10+10), fmt.Sprintf("未来正文%d", i)))
			}
			if check == "local-body-is-not-ack" {
				// 较新的尾部已先成功，随后恢复出更早的未交付WAL；不能因本地全量正文洗掉中段。
				_, err = m.deliver(source, batch[4:], false)
				require.NoError(t, err)
			}
			require.NoError(t, p.WAL().Append(batch...))
			require.NoError(t, p.WAL().Commit())
			if check == "local-body-is-not-ack" {
				require.NoError(t, p.WAL().RecordHTTPResult(acquire.DeliveryResult{StartPos: 40, EndPos: 50, HTTPStatus: 200}))
			}
			require.NoError(t, m.persist())
			loaded, err := m.loadStateFromIndex()
			require.NoError(t, err)
			persisted := loaded.Sources[p.Key().String()]
			// 2026-10-29 新契约：WAL 已改为**纯内存队列**，`source_wal` 表已删 ⇒ 索引里不再有
			// 恢复 WAL 行。旧断言（`Len(persisted.WAL, N)`）失去对象，改成两部分：
			//   ① 索引里确实不带 WAL（防止有人把表悄悄加回来）；
			//   ② 恢复输入就是内存队列快照（与 buildRebuiltSourceOutsideLock 同源）。
			// 被测关切（“重放子集不得发布/ACK 未来正文”）一字未变——它只看恢复输入的内容，
			// 不关心那些内容存在表里还是存在内存里。
			require.Empty(t, persisted.WAL, "WAL 已不落盘：索引不得再携带恢复 WAL 行")
			persisted.WAL = p.WAL().Snapshot()
			// 2026-10-29：旧夹具在这里对 `local-body-is-not-ack` 减一（“已 ACK 的那条已转入事件段”）。
			// 那个减一依赖旧架构的“ACK 即转段 + 从队列搜走”机制；新语义下条目要等 `Record.End <= Reclaim`
			// 才离开内存队列，而本例 [0,40) 是真空洞 ⇒ Reclaim 恒为 0 ⇒ 五条全在队列里（实测）。
			// 被测关切是“完整本地正文不得授权 ACK 未来段”，它只要求恢复输入是**完整**的，
			// 不要求其中某条已离开队列 ⇒ 这里断言完整五条，后面“中段仍缺”的断言一字未改。
			require.Len(t, persisted.WAL, len(batch), "恢复输入必须是完整的未投递正文（内存队列快照）")
			// 模拟恢复入口已装载、尚未启动恢复worker的输入，保留真实SQLite正文与资格。
			m.mu.Lock()
			m.state.Sources[p.Key().String()] = persisted
			m.mu.Unlock()
			canonical, err := m.canonicalRecoveryEvents(p.Key().String(), persisted)
			require.NoError(t, err)
			require.Len(t, canonical, len(batch), "实际段正文与恢复WAL的并集必须完整")
			if check == "published-prefix" {
				_, err = m.deliver(source, batch[:2], true)
				require.NoError(t, err)
				require.Equal(t, 2, fixture.rowCount())
				rec, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
				require.Equal(t, uint64(20), publishedEvidenceClosed(rec, source), "完整canonical输入中的未来正文不能抬高本轮只写两条的封闭前缀")
				return
			}
			_, err = p.DeliverPending()
			require.NoError(t, err)
			require.Equal(t, 3, fixture.rowCount(), "只有旧前两条和较新一条真实落库，中间两条仍缺失")
			before, ok := p.Positions()
			require.True(t, ok)
			require.Equal(t, uint64(20), before.Delivery)
			require.Len(t, durableEvents(t, m, p.Key().String()), 5, "恢复完整正文已落本地段，但未全写VL")
			// 权威事件段可以保存完整Durable正文，但不能据此把尚未写VL的后两条返回204/ACK。
			_, err = p.DeliverPending()
			require.Error(t, err, "本地已有正文不等于外部已写入，不得把缺失后缀伪装成投递成功")
			after, ok := p.Positions()
			require.True(t, ok)
			require.Equal(t, before.Delivery, after.Delivery)
			require.Equal(t, before.Reclaim, after.Reclaim)
			require.Equal(t, 3, fixture.rowCount())
			require.NotEmpty(t, p.WAL().Snapshot(), "未落库的责任必须继续留在WAL")
			require.Equal(t, 1, m.drainRebuilds(context.Background()), "缺失日必须转入真实预算化修复")
			for drain := 0; drain < 3; drain++ {
				_, err = p.DeliverPending()
				require.NoError(t, err)
			}
			final, ok := p.Positions()
			require.True(t, ok)
			require.Equal(t, uint64(50), final.Delivery)
			require.Equal(t, final.Delivery, final.Reclaim)
			require.Equal(t, 8, fixture.rowCount(), "只允许一次完整预算化副本；旧三行保留、新五行完备")
		})
	}
}

func TestIngestSixFixNewTailCannotSealKnownSameDayDebt(t *testing.T) {
	for _, sameDay := range []bool{true, false} {
		t.Run(fmt.Sprintf("same-day=%t", sameDay), func(t *testing.T) {
			client, fixture, _ := newReconcileVL(t)
			root := t.TempDir()
			source := hotRetentionTestSource(t, root)
			m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}})
			require.NoError(t, err)
			p := m.pipes[source.LogSourceID+"/"+source.SourceGeneration]
			oldDay := source.UTCDay
			if !sameDay {
				day, err := time.Parse("2006-01-02", oldDay)
				require.NoError(t, err)
				oldDay = day.AddDate(0, 0, -1).Format("2006-01-02")
			}
			old := hotRetentionTestEvent(oldDay, 0, 10, "尚未投递的旧正文")
			tail := hotRetentionTestEvent(source.UTCDay, 10, 20, "新的真实尾部")
			require.NoError(t, p.WAL().Append(old, tail))
			require.NoError(t, p.WAL().Commit())
			require.NoError(t, m.persist())
			loaded, err := m.loadStateFromIndex()
			require.NoError(t, err)
			// 2026-10-29：WAL 已不落盘，`source_wal` 表已删 ⇒ 从索引读回的 `persisted.WAL` 恒为空。
			// 新契约下「恢复输入」= 内存队列快照（见 buildRebuiltSourceOutsideLock），
			// 所以这里显式补上，否则重建看不到旧欠账，后面的“补齐后才追平”断言失去对象。
			recovery := loaded.Sources[p.Key().String()]
			require.Empty(t, recovery.WAL, "WAL 已不落盘：索引不得再携带恢复 WAL 行")
			recovery.WAL = p.WAL().Snapshot()
			require.Len(t, recovery.WAL, 2, "恢复输入必须包含旧欠账与新尾（内存队列快照）")
			m.mu.Lock()
			m.state.Sources[p.Key().String()] = recovery
			m.mu.Unlock()
			_, err = m.deliver(source, []logtypes.Event{tail}, false)
			require.Equal(t, 1, fixture.rowCount(), "旧欠账不能阻挡新尾实际物理写入")
			rec, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
			if !sameDay {
				require.NoError(t, err, "其他UTC日欠账不能用全源Start==closed硬卡合法新日")
				require.Equal(t, uint64(20), publishedEvidenceClosed(rec, source))
				return
			}
			require.Error(t, err, "同日已知未投递正文不能因新尾maxEnd被宣称完整前缀")
			require.Zero(t, publishedEvidenceClosed(rec, source))
			require.Equal(t, 1, m.drainRebuilds(context.Background()))
			rec, _ = m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
			require.Equal(t, uint64(20), publishedEvidenceClosed(rec, source), "只有实际补齐并逐字段校验后才可追平")
			_, err = p.DeliverPending()
			require.NoError(t, err)
			positions, ok := p.Positions()
			require.True(t, ok)
			require.Equal(t, uint64(20), positions.Reclaim)
		})
	}
}

func TestIngestSixFixFreshBatchDuplicateDoesNotRequireOldProjection(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("conflict=%t", conflict), func(t *testing.T) {
			client, fixture, _ := newReconcileVL(t)
			root := t.TempDir()
			source := hotRetentionTestSource(t, root)
			limits := stateindex.MaterializationLimits{WindowCopies: 1, HourlyCopies: 1, HourlyBytes: 1 << 20}
			m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}, MaterializationLimits: &limits})
			require.NoError(t, err)
			a := hotRetentionTestEvent(source.UTCDay, 0, 10, "正文甲")
			b := a
			if conflict {
				b = hotRetentionTestEvent(source.UTCDay, 0, 10, "正文乙")
				require.Equal(t, a.EventID, b.EventID)
				require.NotEqual(t, a.CanonicalHash, b.CanonicalHash)
			}
			_, release, err := m.reserveMaterialization(context.Background(), source, "已用完的后台份额", []logtypes.Event{a})
			require.NoError(t, err)
			release()
			_, err = m.deliver(source, []logtypes.Event{a, b}, false)
			require.NoError(t, err, "本批新身份不能因seen已被规划器更新而冒充旧正文，后台预算也不能阻塞普通尾部")
			require.Equal(t, 1, fixture.rowCount(), "相同内容批内去重，异hash冲突也不能无声多写副本")
			require.Zero(t, m.RebuildStats().Scheduled)
			rec, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
			require.NotNil(t, rec)
			if conflict {
				require.Positive(t, rec.PublishedProjection.ConflictCount, "异hash负控必须留下冲突，不能伪装无歧义")
			} else {
				require.Zero(t, rec.PublishedProjection.ConflictCount)
			}
		})
	}
}

func TestIngestSixFixLocalIdentityVariantsUseCanonicalWinner(t *testing.T) {
	for _, fullFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("full-first=%t", fullFirst), func(t *testing.T) {
			client, fixture, _ := newReconcileVL(t)
			root := t.TempDir()
			source := hotRetentionTestSource(t, root)
			m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}})
			require.NoError(t, err)
			key := source.LogSourceID + "/" + source.SourceGeneration
			full := hotRetentionTestEvent(source.UTCDay, 0, 10, "遗留完整正文，比残行更长")
			partial := hotRetentionTestEvent(source.UTCDay, 0, 10, "残行")
			require.Equal(t, full.EventID, partial.EventID)
			require.NotEqual(t, full.CanonicalHash, partial.CanonicalHash)
			if !fullFirst {
				require.NoError(t, m.events.Append(key, []logtypes.Event{partial}))
			}
			_, err = m.deliver(source, []logtypes.Event{full}, false)
			require.NoError(t, err)
			if fullFirst {
				require.NoError(t, m.events.Append(key, []logtypes.Event{partial}))
			}
			m.bumpEventStoreEpoch(key)
			canonical, err := m.canonicalRecoveryEvents(key, savedSource(t, m, key))
			require.NoError(t, err)
			require.Len(t, canonical, 1)
			require.Equal(t, full.CanonicalHash, canonical[0].CanonicalHash, "遗留段两种物理顺序都必须取相同canonical胜者")
			require.Len(t, m.landedVisibleInVL(source, canonical), 1, "当前VL只能有完整胜者，不伪造残行已投递")
			_, err = m.deliver(source, []logtypes.Event{partial}, false)
			require.NoError(t, err, "新资格门必须复用既有较长者胜裁定，而非最后物理行胜")
			require.Equal(t, 1, fixture.rowCount(), "重投遗留残行不能重写或阻塞已确认canonical副本")
			require.Zero(t, m.RebuildStats().Scheduled)
		})
	}
}

func TestIngestSixFixRawEvidenceUsesStableCanonicalTime(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	require.NoError(t, os.WriteFile(source.Path, []byte("raw canonical line\n"), 0o600))
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	p := m.pipes[source.LogSourceID+"/"+source.SourceGeneration]
	batch, err := p.Poll()
	require.NoError(t, err)
	require.Len(t, batch, 1)
	require.Empty(t, batch[0].EventTimeUTC, "夹具必须真实走只有稳定摄取时间的Raw事件")
	require.NotEmpty(t, batch[0].IngestTimeUTC)
	positions, ok := p.Positions()
	require.True(t, ok)
	require.Positive(t, positions.Durable)
	require.Equal(t, positions.Durable, positions.Reclaim, "回收取证必须按写VL时相同的稳定时间/哈希规范化，不得永久滞留Raw正文")
	require.Equal(t, 1, fixture.rowCount())
	require.Len(t, m.landedVisibleInVL(source, batch), 1)
	bad := batch[0]
	bad.Message = "被篡改的Raw正文"
	require.Empty(t, m.landedVisibleInVL(source, []logtypes.Event{bad}), "规范化不能给错误正文伪造可见性")
	bad = batch[0]
	bad.IngestTimeUTC = "not-a-stable-time"
	require.Empty(t, m.landedVisibleInVL(source, []logtypes.Event{bad}))
}

func TestIngestSixFixDeferredHoleDoesNotPollutePublishedActiveGeneration(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	require.NoError(t, os.WriteFile(source.Path, []byte("[12:00:01] [Server thread/INFO]: A\n[12:00:02] [Server thread/INFO]: B\n"), 0o600))
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}, PersistHotBudget: time.Millisecond})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	p := m.pipes[key]
	a, err := p.Poll()
	require.NoError(t, err)
	require.Len(t, a, 1)
	m.mu.Lock()
	active := m.activeTails[key].generation
	m.mu.Unlock()
	require.NotEmpty(t, active, "必须先有已确认且在PP中的活跃代次")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m.persistStepHold = func() { once.Do(func() { close(entered); <-release }) }
	require.NoError(t, p.Ledger().PauseAcquire(p.Key(), "触发真实持久化屏障"))
	require.NoError(t, p.Ledger().ResumeAcquire(p.Key()))
	persisted := make(chan error, 1)
	go func() { persisted <- m.persist() }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("真实持久化屏障未进入")
	}
	appendLogLine(t, source.Path, "[12:00:03] [Server thread/INFO]: C\n")
	_, err = p.Poll()
	require.ErrorIs(t, err, ErrPersistDeferred)
	require.Equal(t, 1, fixture.rowCount(), "B在交办完成前不得外发")
	close(release)
	require.NoError(t, <-persisted)
	require.NoError(t, m.persist())
	m.persistHotBudget = time.Second
	appendLogLine(t, source.Path, "[12:00:04] [Server thread/INFO]: boundary\n")
	_, err = p.Poll()
	require.Error(t, err, "同日B尚未确认，新尾C不能越过旧前缀")
	require.Equal(t, 2, fixture.rowCount(), "新尾C仍允许实际物理写，但必须隔离于旧已发布代次")
	var pending []logtypes.Event
	for _, entry := range p.WAL().Snapshot() {
		if entry.Durable {
			pending = append(pending, entry.Event)
		}
	}
	require.Len(t, pending, 2)
	_, err = p.DeliverPending()
	require.NoError(t, err)
	proof := append(append([]logtypes.Event(nil), a...), pending...)
	require.Len(t, m.landedVisibleInVL(source, proof), 3, "排空后PP并集必须恰含A/B/C，不能暴露两个C")
	positions, ok := p.Positions()
	require.True(t, ok)
	require.Equal(t, positions.Durable, positions.Reclaim, "不能仅Delivery到顶而WAL仍因重复投影无法回收")
	beforeConfirm := fixture.insertAttemptCount()
	require.Equal(t, 1, m.drainRebuilds(context.Background()))
	require.Equal(t, beforeConfirm, fixture.insertAttemptCount(), "补齐后只读确认即可，不能再造完整副本修复自造重复")
	rows, err := m.index.ListMaterializations(context.Background(), key, source.UTCDay, "", 0, 200)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestIngestSixFixLostActiveAppendRetryCannotExposeDuplicate(t *testing.T) {
	for _, scenario := range []string{"同批恢复", "后续尾部已隔离", "预算不足"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := &reconcileVLFixture{}
			var insertMu sync.Mutex
			insertCount := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/insert/jsonline" {
					insertMu.Lock()
					insertCount++
					lose := insertCount == 2
					insertMu.Unlock()
					if lose {
						payload, readErr := vlInsertRequestBody(r)
						if readErr != nil {
							http.Error(w, readErr.Error(), http.StatusBadRequest)
							return
						}
						fixture.mu.Lock()
						fixture.insertAttempts++
						fixture.inserts = append(fixture.inserts, payload)
						fixture.rows = append(fixture.rows, parseReconcileRows(payload)...)
						fixture.mu.Unlock()
						conn, _, hijackErr := w.(http.Hijacker).Hijack()
						if hijackErr == nil {
							_ = conn.Close()
						}
						return
					}
				}
				fixture.ServeHTTP(w, r)
			}))
			defer server.Close()
			client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
			require.NoError(t, err)
			root := t.TempDir()
			source := hotRetentionTestSource(t, root)
			require.NoError(t, os.WriteFile(source.Path, []byte("[12:00:01] [Server thread/INFO]: A\n[12:00:02] [Server thread/INFO]: C\n"), 0o600))
			m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}})
			require.NoError(t, err)
			key := source.LogSourceID + "/" + source.SourceGeneration
			m.confirmRequeueBackoff = 2 * time.Second // 只缩短等待；退避/顺延逻辑本身不变
			p := m.pipes[key]
			a, err := p.Poll()
			require.NoError(t, err)
			require.Len(t, a, 1)
			part := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
			before, _ := m.cat.Get(part)
			m.mu.Lock()
			active := m.activeTails[key].generation
			m.mu.Unlock()
			require.NotEmpty(t, active)
			appendLogLine(t, source.Path, "[12:00:03] [Server thread/INFO]: boundary\n")
			_, err = p.Poll()
			require.Error(t, err, "服务端实际落行但关闭连接，调用方必须保持响应未知")
			require.Equal(t, 2, fixture.rowCount())
			lost, _ := m.cat.Get(part)
			require.Equal(t, sourcePublicationToken(before, source), sourcePublicationToken(lost, source), "响应未知不能修改旧封闭前缀")
			var pending []logtypes.Event
			for _, entry := range p.WAL().Snapshot() {
				if entry.Durable {
					pending = append(pending, entry.Event)
				}
			}
			require.Len(t, pending, 1)
			proof := append(append([]logtypes.Event(nil), a...), pending...)
			clock := time.Now()
			m.rebuilds.now = func() time.Time { return clock }
			if scenario == "后续尾部已隔离" {
				appendLogLine(t, source.Path, "[12:00:04] [Server thread/INFO]: next-boundary\n")
				_, err = p.Poll()
				require.Error(t, err, "未来尾部即使物理写入，也不能替响应未知的C发布")
				require.Equal(t, 3, fixture.rowCount())
				_, guardErr := m.writeProjection(source, proof, "projection-guard", pending, nil, false)
				require.ErrorIs(t, guardErr, errProjectionVerifyFailed, "非取代面仍必须逐字段拒绝旧代隐藏副本进入未来并集")
				guarded, _ := m.cat.Get(part)
				require.Equal(t, sourcePublicationToken(before, source), sourcePublicationToken(guarded, source))
			}
			if scenario == "预算不足" {
				m.materializationLimits = stateindex.MaterializationLimits{WindowCopies: 1, HourlyCopies: 1, HourlyBytes: 1 << 20}
				_, release, reserveErr := m.reserveMaterialization(context.Background(), source, "spent-window", []logtypes.Event{hotRetentionTestEvent(source.UTCDay, 0, 10, "已花费的其他稳定窗口")})
				require.NoError(t, reserveErr)
				release()
			}
			// 同批重试的资格不能依赖调用方恰好传replay=true；未知物理行也不能被新代并集解封两次。
			_, err = m.deliver(source, pending, false)
			switch scenario {
			case "预算不足":
				require.ErrorIs(t, err, ErrMaterializationBudget)
				require.Equal(t, 2, fixture.rowCount(), "预算拒绝必须零新增物理写入，不能借必要压缩份额绕过")
			default:
				require.NoError(t, err)
				require.Len(t, m.landedVisibleInVL(source, proof), 2, "重试成功后的允许投影中每个ID必须恰一份")
			}
			if scenario == "预算不足" {
				retained, _ := m.cat.Get(part)
				require.Equal(t, sourcePublicationToken(before, source), sourcePublicationToken(retained, source), "不确定或预算拒绝时，不能扩大旧PP")
				positions, ok := p.Positions()
				require.True(t, ok)
				require.Less(t, positions.Reclaim, positions.Durable)
				require.NotEmpty(t, p.WAL().Snapshot(), "实际WAL责任必须仍在")
			}
			if scenario == "预算不足" {
				_, err = p.DeliverPending()
				require.ErrorIs(t, err, ErrMaterializationBudget)
				clock = clock.Add(time.Hour + time.Nanosecond)
			}
			_, err = p.DeliverPending()
			require.NoError(t, err)
			require.Len(t, m.landedVisibleInVL(source, proof), 2, "真实恢复后PP必须无重复，不能只让Delivery到顶")
			positions, ok := p.Positions()
			require.True(t, ok)
			require.Equal(t, positions.Durable, positions.Reclaim, "必须有ACK与当前逐字段证据后实际回收WAL")
			rows, err := m.index.ListMaterializations(context.Background(), key, source.UTCDay, "", 0, 200)
			require.NoError(t, err)
			spent := 0
			if scenario == "预算不足" {
				spent = 1
			}
			require.LessOrEqual(t, len(rows), 1+spent, "可以只读取证复用；若完整取代则必须只有一次受预算约束的副本")
			published := 0
			for _, row := range rows {
				require.Equal(t, "rebuild", row.Class, "失败修复不得借正常压缩保留份额绕过重建预算")
				require.Positive(t, row.ReservedBytes)
				if row.Published {
					require.True(t, row.Verified)
					published++
				} else {
					require.Equal(t, "unknown", row.State, "早先未知成本不得退款或伪装成已验证")
				}
			}
			require.Equal(t, 1, published)
		})
	}
}

func TestIngestSixFixVerifiedTailClearsResidualPending(t *testing.T) {
	client, _, _ := newReconcileVL(t)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	require.NoError(t, os.WriteFile(source.Path, []byte("[12:00:01] [Server thread/INFO]: 已发布尾部\n"), 0o600))
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	_, err = m.deliver(source, []logtypes.Event{hotRetentionTestEvent(source.UTCDay, 0, 10, "首个已确认尾部")}, false)
	require.NoError(t, err)
	m.mu.Lock()
	stale := m.state.Sources[key]
	stale.PublicationPending = true
	m.state.Sources[key] = stale
	m.mu.Unlock()
	// 现场形态：PP 已 complete、CV 与 Durable 对齐、WAL/SQLite 零残留，只有标志仍是 true。
	// 尾采批次刻意不替它清（那会让已排队的确认任务退化成整窗副本），必须由确认路径收口。
	next := hotRetentionTestEvent(source.UTCDay, 10, 20, "后续正常尾部")
	_, err = m.deliver(source, []logtypes.Event{next}, false)
	require.NoError(t, err)
	m.mu.Lock()
	pendingAfterTail := m.state.Sources[key].PublicationPending
	m.mu.Unlock()
	require.True(t, pendingAfterTail, "尾采不得代确认任务清pending，否则会退回整窗重写")
	confirmed, err := m.tryConfirmPendingPublication(source, key, m.state.Sources[key])
	require.NoError(t, err)
	require.True(t, confirmed, "完整可见且权威未变时，确认路径必须收口")
	m.mu.Lock()
	pendingAfterConfirm := m.state.Sources[key].PublicationPending
	m.mu.Unlock()
	require.False(t, pendingAfterConfirm)
	require.False(t, cutoverPendingFor(m, key), "cutover不能仍因publication_pending拒绝")
	require.Len(t, m.landedVisibleInVL(source, []logtypes.Event{next}), 1)
}

func TestIngestSixFixInconclusiveConfirmIsRequeued(t *testing.T) {
	client, _, _ := newReconcileVL(t)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	require.NoError(t, os.WriteFile(source.Path, []byte("[12:00:01] [Server thread/INFO]: 已发布尾部\n"), 0o600))
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	m.confirmRequeueBackoff = time.Millisecond // 只缩短等待，不改任何被测判据
	key := source.LogSourceID + "/" + source.SourceGeneration
	_, err = m.deliver(source, []logtypes.Event{hotRetentionTestEvent(source.UTCDay, 0, 10, "首个已确认尾部")}, false)
	require.NoError(t, err)
	m.mu.Lock()
	stale := m.state.Sources[key]
	stale.PublicationPending = true
	m.state.Sources[key] = stale
	m.mu.Unlock()
	// 现场形态：确认取证与在飞写入重叠 ⇒ inconclusive ⇒ 本轮不写任何天。
	m.mu.Lock()
	m.publicationInflight[key] = 1
	m.mu.Unlock()
	ok, _ := m.scheduleRebuildConfirmPendingAsync(source, []string{source.UTCDay}, "pending_unconfirmed")
	require.True(t, ok)
	// 用**真实 worker 入口**（runRebuildRequest，非测试用 drain）驱动，才能覆盖生产那条
	// 「确认未完成 ⇒ 按退避重排下一次确认」的路径。
	m.rebuilds.mu.Lock()
	first := m.rebuilds.pickLocked()
	m.rebuilds.mu.Unlock()
	require.NotNil(t, first)
	m.runRebuildRequest(context.Background(), first)
	m.mu.Lock()
	m.publicationInflight[key] = 0
	pendingAfterInconclusive := m.state.Sources[key].PublicationPending
	m.mu.Unlock()
	require.True(t, pendingAfterInconclusive, "取证未完成不得清pending")
	// 关键判据：必须有下一次确认任务被真实重排（否则安静源只能等默认15分钟对账）。
	require.Eventually(t, func() bool {
		m.rebuilds.mu.Lock()
		_, queued := m.rebuilds.pending[key]
		m.rebuilds.mu.Unlock()
		return queued
	}, 2*time.Second, time.Millisecond, "inconclusive后必须在退避后自动重排确认任务")
	m.rebuilds.mu.Lock()
	m.rebuilds.attempts[key] = &rebuildAttemptState{}
	m.rebuilds.mu.Unlock()
	require.Positive(t, m.drainRebuilds(context.Background()), "重排的确认任务必须真的可执行")
	m.mu.Lock()
	pendingAfterRecheck := m.state.Sources[key].PublicationPending
	m.mu.Unlock()
	require.False(t, pendingAfterRecheck, "第二次确认完整可见后必须收口")
	require.False(t, cutoverPendingFor(m, key))
}

// TestIngestSixFixSilentQuietSourceReconfirmsPending 钉住生产 r5 的静默源形态：
// 确认取证因与在飞写入重叠/权威变化而没出结论（inconclusive 或 CAS stale），随后**输入停止**
// （WAL 排空、Delivery==Durable、不再有新批次）——此时唯一能清掉 pending 的只有「既有 worker
// 按有界退避再跑一次完整确认」。用例断言：不依赖任何新的尾部成功、也不依赖默认 15 分钟对账，
// pending 必须在短退避后的重排确认里收口，且全程零新增物理写入。
func TestIngestSixFixSilentQuietSourceReconfirmsPending(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	require.NoError(t, os.WriteFile(source.Path, []byte("[12:00:01] [Server thread/INFO]: 已发布尾部\n"), 0o600))
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	m.confirmRequeueBackoff = time.Millisecond
	key := source.LogSourceID + "/" + source.SourceGeneration
	_, err = m.deliver(source, []logtypes.Event{hotRetentionTestEvent(source.UTCDay, 0, 10, "首个已确认尾部")}, false)
	require.NoError(t, err)
	m.mu.Lock()
	stale := m.state.Sources[key]
	stale.PublicationPending = true
	m.state.Sources[key] = stale
	// 现场形态:确认开始时该源仍有在飞写入 ⇒ 取证不可信 ⇒ inconclusive（不写任何天）。
	m.publicationInflight[key] = 1
	m.mu.Unlock()
	insertsBefore := fixture.insertAttemptCount()
	// 真实 worker 循环：与生产同一条 goroutine 路径（RunRebuildWorker），不是测试专用 drain。
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.RunRebuildWorker(ctx) }()
	defer func() { cancel(); <-done }()
	ok, _ := m.scheduleRebuildConfirmPendingAsync(source, []string{source.UTCDay}, "pending_unconfirmed")
	require.True(t, ok)
	// 在飞写入结束、输入停止：此后没有任何新尾部批次，pending 只能由重排的确认收口。
	m.mu.Lock()
	m.publicationInflight[key] = 0
	m.mu.Unlock()
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !m.state.Sources[key].PublicationPending
	}, 10*time.Second, 5*time.Millisecond, "静默源必须由既有worker的重排只读确认清掉pending")
	// 零新增物理写入：确认是纯只读取证，不得再造副本，也不得靠新尾部成功来"顺手"清标志。
	require.Equal(t, insertsBefore, fixture.insertAttemptCount(), "确认路径不得新增物理写入")
	positions, ok := m.pipes[key].Positions()
	require.True(t, ok)
	require.Equal(t, positions.Durable, positions.Reclaim)
	require.False(t, cutoverPendingFor(m, key))
}

// TestIngestSixFixCASStaleConfirmIsRequeued 覆盖 r5 的另一条真实出口：
// 完整取证做完、最后一步 CAS 发现本源发布版本已变（新尾刚发布），确认**失败但不是数据失败**。
// 这条路径过去只看 outcome.Err != nil 就直接收工、不续排，于是 pending 一直留着；
// 正式反例要求它在有界退避后由既有 worker 再确认一次并收口。
func TestIngestSixFixCASStaleConfirmIsRequeued(t *testing.T) {
	client, _, _ := newReconcileVL(t)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	require.NoError(t, os.WriteFile(source.Path, []byte("[12:00:01] [Server thread/INFO]: 已发布尾部\n"), 0o600))
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	m.confirmRequeueBackoff = time.Millisecond
	key := source.LogSourceID + "/" + source.SourceGeneration
	_, err = m.deliver(source, []logtypes.Event{hotRetentionTestEvent(source.UTCDay, 0, 10, "首个已确认尾部")}, false)
	require.NoError(t, err)
	m.mu.Lock()
	stale := m.state.Sources[key]
	stale.PublicationPending = true
	m.state.Sources[key] = stale
	m.mu.Unlock()
	// 制造"取证期间本源发布版本变了"：直接把发布 epoch 推进一格，确认末尾的 CAS 检查即失效。
	m.publishMu.Lock()
	if m.publicationEpochs == nil {
		m.publicationEpochs = map[string]map[string]uint64{}
	}
	if m.publicationEpochs[key] == nil {
		m.publicationEpochs[key] = map[string]uint64{}
	}
	m.publicationEpochs[key][source.UTCDay]++
	m.publishMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.RunRebuildWorker(ctx) }()
	defer func() { cancel(); <-done }()
	ok, _ := m.scheduleRebuildConfirmPendingAsync(source, []string{source.UTCDay}, "pending_unconfirmed")
	require.True(t, ok)
	// 输入停止（不再有新尾部批次）：pending 只能由重排的只读确认收口。
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !m.state.Sources[key].PublicationPending
	}, 10*time.Second, 5*time.Millisecond, "CAS失效后必须由既有worker重排确认并收口")
	require.False(t, cutoverPendingFor(m, key))
	m.rebuilds.mu.Lock()
	pendingTimers := len(m.rebuilds.confirmTimers)
	m.rebuilds.mu.Unlock()
	require.LessOrEqual(t, pendingTimers, 1, "同一源最多保留一个待触发的重排定时器")
}

// TestIngestSixFixNaturalConfirmTailOverlapReconfirms 是 r5 的**自然场景**反例（不手置 inflight、
// 不直接清物化台账）：
//  1. 首启尾部成功发布，随后置 pending（模拟"上次发布未获确认"）；
//  2. 确认取证在**真实 VL 查询途中**被 HTTP 屏障挂住；
//  3. 该源此刻发起真实新尾批次并成功发布（真实 p.Poll → 真实写入 + 校验 + 发布）；
//  4. 放行确认 ⇒ 它读到的是过期快照 ⇒ 本轮以 CAS 失效结束，**pending 不得被清**；
//  5. 此后输入停止（不再有新批次、WAL 排空）：pending 只能由既有 worker 的续排只读确认收口。
//
// 断言同时锁住：首轮确实失败（有 Partial/Pending 结论）、续排确认零新增物理插入、
// 最终 WAL 为零且 cutover 不再报 publication_pending。
func TestIngestSixFixNaturalConfirmTailOverlapReconfirms(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	require.NoError(t, os.WriteFile(source.Path, []byte("[12:00:01] [Server thread/INFO]: 首批\n[12:00:02] [Server thread/INFO]: 首批边界\n"), 0o600))
	key := source.LogSourceID + "/" + source.SourceGeneration
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	m.confirmRequeueBackoff = 2 * time.Second // 给首轮失败留出独立观察窗口，与后续断言约定的退避一致。
	p := m.pipes[key]
	first, err := p.Poll()
	require.NoError(t, err)
	require.Len(t, first, 1, "首启尾部必须真实发布成功")
	require.Equal(t, 1, fixture.rowCount(), "首启尾部必须真的写进VL")
	m.mu.Lock()
	stale := m.state.Sources[key]
	stale.PublicationPending = true
	m.state.Sources[key] = stale
	m.mu.Unlock()

	gate := &queryGate{entered: make(chan struct{}), release: make(chan struct{})}
	fixture.mu.Lock()
	fixture.queryGate = gate
	fixture.mu.Unlock()
	defer func() {
		fixture.mu.Lock()
		fixture.queryGate = nil
		fixture.mu.Unlock()
		select {
		case <-gate.release:
		default:
			close(gate.release)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.RunRebuildWorker(ctx) }()
	defer func() { cancel(); <-done }()
	insertsBefore := fixture.insertAttemptCount()
	ok, _ := m.scheduleRebuildConfirmPendingAsync(source, []string{source.UTCDay}, "pending_unconfirmed")
	require.True(t, ok)
	select {
	case <-gate.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("确认取证未进入真实 VL 查询")
	}
	// 确认被钉住期间：该源出现真实新批次，但 VL 侧真实失败（503）⇒ 它既不能发布、也不能清 pending。
	// 这正是 r5 的形态：确认与新写入重叠、且源侧还有未排空的持久正文。
	fixture.mu.Lock()
	fixture.failInsert = true
	fixture.mu.Unlock()
	appendLogLine(t, source.Path, "[12:00:02] [Server thread/INFO]: 与确认重叠且失败的新尾\n")
	_, err = p.Poll()
	require.Error(t, err, "VL 写失败必须如实报错，正文留在 WAL")
	fixture.mu.Lock()
	fixture.failInsert = false
	fixture.mu.Unlock()
	insertsAfterTail := fixture.insertAttemptCount()
	require.Greater(t, insertsAfterTail, insertsBefore, "新批次必须真的尝试写过 VL")
	// 放行确认 ⇒ 它基于过期快照，本轮必须**失败/未出结论**（inconclusive 或 CAS 失效），
	// 不得清掉 pending。重排在 2s 退避之后才触发，故此处读到的是**首轮**的真实结论。
	statsBeforeTail := m.RebuildStats()
	close(gate.release)
	require.Eventually(t, func() bool {
		now := m.RebuildStats()
		return now.Partial > statsBeforeTail.Partial || now.Failed > statsBeforeTail.Failed
	}, 2*time.Second, 5*time.Millisecond, "首轮确认必须因与新尾重叠而未成功，而不是悄悄清掉 pending")
	m.mu.Lock()
	pendingAfterOverlap := m.state.Sources[key].PublicationPending
	m.mu.Unlock()
	require.True(t, pendingAfterOverlap, "首轮失败时不得清pending")
	// 此后输入停止：只剩既有 worker 的重排（先只读确认）；确认通过即收口，不通过才落回重建。
	// 因此这里不强求"零写入"，而是要求：**收口之后**不再有新增插入尝试，且 WAL 真正排空。
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !m.state.Sources[key].PublicationPending
	}, 15*time.Second, 5*time.Millisecond, "续排确认必须最终收口pending")
	require.False(t, cutoverPendingFor(m, key))
	settled := fixture.insertAttemptCount()
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, settled, fixture.insertAttemptCount(), "收口后不得再有新的物理写入（不得靠反复重写刷绿）")
	positions, ok := p.Positions()
	require.True(t, ok)
	require.Equal(t, positions.Read, positions.Durable)
	// 关键判据：pending 之所以能清，必须是**这些正文在 VL 里真的逐字段可见**，
	// 而不是靠把标志抹掉。用与生产同源的证据面函数核对，并把失败那批也算进去。
	var durable []logtypes.Event
	for _, entry := range p.WAL().Snapshot() {
		if entry.Durable {
			durable = append(durable, entry.Event)
		}
	}
	require.NotEmpty(t, durable, "夹具必须仍有真实持久正文")
	visible := m.landedVisibleInVL(source, durable)
	require.Len(t, visible, len(durable), "清 pending 的前提是所有持久正文都已在 VL 中逐字段可见")
}

// TestIngestSixFixNaturalConfirmTailOverlapWALZeroReconfirms 是 r5「纯只读续排」的正控：
// 先让确认在真实 VL 查询途中被一次性 HTTP 屏障挂住，期间该源真实新尾**成功**发布并与确认重叠
// （导致首轮确认 inconclusive/Partial），**随后输入停止、WAL 排空**；此后唯一能动的是既有 worker
// 的只读确认。断言：首轮确有 Partial、收口后**零新增物理插入（含零副本）**、D==R 且 WAL 为空、
// cutover 的 LedgerReady 为真——即"WAL0 仍 pending → 只读续排收口"的真实形态。
func TestIngestSixFixNaturalConfirmTailOverlapWALZeroReconfirms(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	require.NoError(t, os.WriteFile(source.Path, []byte("[12:00:01] [Server thread/INFO]: 首批\n[12:00:02] [Server thread/INFO]: 首批边界\n"), 0o600))
	key := source.LogSourceID + "/" + source.SourceGeneration
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	m.confirmRequeueBackoff = 2 * time.Second
	p := m.pipes[key]
	_, err = p.Poll()
	require.NoError(t, err)
	require.Equal(t, 1, fixture.rowCount())
	m.mu.Lock()
	stale := m.state.Sources[key]
	stale.PublicationPending = true
	m.state.Sources[key] = stale
	m.mu.Unlock()

	gate := &queryGate{entered: make(chan struct{}), release: make(chan struct{})}
	fixture.mu.Lock()
	fixture.queryGate = gate
	fixture.mu.Unlock()
	defer func() {
		fixture.mu.Lock()
		fixture.queryGate = nil
		fixture.mu.Unlock()
		select {
		case <-gate.release:
		default:
			close(gate.release)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.RunRebuildWorker(ctx) }()
	defer func() { cancel(); <-done }()
	ok, _ := m.scheduleRebuildConfirmPendingAsync(source, []string{source.UTCDay}, "pending_unconfirmed")
	require.True(t, ok)
	select {
	case <-gate.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("确认取证未进入真实 VL 查询")
	}
	// 重叠：真实新尾成功发布（写 VL + 逐字段校验 + 发布，全真实路径）。
	appendLogLine(t, source.Path, "[12:00:03] [Server thread/INFO]: 与确认重叠的新尾\n")
	_, err = p.Poll()
	require.NoError(t, err, "成功新尾必须真实发布")
	require.Equal(t, 2, fixture.rowCount())
	statsBefore := m.RebuildStats()
	close(gate.release)
	require.Eventually(t, func() bool {
		now := m.RebuildStats()
		return now.Partial > statsBefore.Partial || now.Failed > statsBefore.Failed
	}, 5*time.Second, 5*time.Millisecond, "首轮确认必须因与新尾重叠而未成功")
	// 输入停止：不再 append、不再 Poll。此后只可能由既有 worker 的只读确认收口。
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !m.state.Sources[key].PublicationPending
	}, 20*time.Second, 5*time.Millisecond, "WAL 已排空且无新输入时，只读续排必须收口 pending")
	insertsAtSettle := fixture.insertAttemptCount()
	time.Sleep(300 * time.Millisecond)
	require.Equal(t, insertsAtSettle, fixture.insertAttemptCount(), "收口后不得再有物理写入（含副本）")
	require.Equal(t, 2, fixture.rowCount(), "不得出现额外投影副本行")
	positions, ok := p.Positions()
	require.True(t, ok)
	require.Equal(t, positions.Read, positions.Durable)
	require.Equal(t, positions.Durable, positions.Reclaim, "WAL必须真正排空")
	require.Empty(t, p.WAL().Snapshot(), "WAL 为空：这正是 r5 的形态")
	require.True(t, m.CutoverReadiness().LedgerReady, "cutover 必须就绪，且不再报 publication_pending")
}

// cutoverPendingFor 报告 cutover 闸门是否仍对该源报 publication_pending（与真实闸门同源）。
func cutoverPendingFor(m *Manager, key string) bool {
	for _, reason := range m.CutoverReadiness().Reasons {
		if reason == key+":publication_pending" {
			return true
		}
	}
	return false
}

func TestIngestSixFixResidualPendingSurvivesWhenRealDebtRemains(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	require.NoError(t, os.WriteFile(source.Path, []byte("[12:00:01] [Server thread/INFO]: 已发布尾部\n"), 0o600))
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	p := m.pipes[key]
	_, err = m.deliver(source, []logtypes.Event{hotRetentionTestEvent(source.UTCDay, 0, 10, "首个已确认尾部")}, false)
	require.NoError(t, err)
	m.mu.Lock()
	stale := m.state.Sources[key]
	stale.PublicationPending = true
	m.state.Sources[key] = stale
	m.mu.Unlock()
	// 真实欠账形态：VL 写入失败 ⇒ 该批已持久但尚未外发，必须在保留 pending 的同时被识别为残留正文。
	fixture.mu.Lock()
	fixture.failInsert = true
	fixture.mu.Unlock()
	appendLogLine(t, source.Path, "[12:00:02] [Server thread/INFO]: 已持久未外发\n")
	_, err = p.Poll()
	require.Error(t, err, "VL 写入失败必须如实报错，正文留在 WAL")
	fixture.mu.Lock()
	fixture.failInsert = false
	fixture.mu.Unlock()
	var durable []logtypes.Event
	for _, entry := range p.WAL().Snapshot() {
		if entry.Durable {
			durable = append(durable, entry.Event)
		}
	}
	require.NotEmpty(t, durable, "真实欠账必须仍在 WAL 中")
	residual, residualErr := m.hasResidualUnpublishedDebt(context.Background(), source, nil)
	require.NoError(t, residualErr)
	require.True(t, residual, "已持久但未外发的同日正文必须被识别为真实欠账")
	m.mu.Lock()
	pendingAfterTail := m.state.Sources[key].PublicationPending
	m.mu.Unlock()
	require.True(t, pendingAfterTail, "有未发布正文时不得清pending")
}

func TestIngestSixFixPermitFreezeDrainsBeforeSnapshot(t *testing.T) {
	fixture := &reconcileVLFixture{}
	var m *Manager
	var source SourceConfig
	var freezeErr error
	var once sync.Once
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/insert/jsonline" {
			once.Do(func() { _, freezeErr = cat.BeginDayMigration(source.UTCDay, catalog.OwnerCold, "cold-day") })
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	root := t.TempDir()
	source = hotRetentionTestSource(t, root)
	require.NoError(t, cat.Put(catalog.NewStableRecord(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}, catalog.OwnerHot, 1, "hot-day")))
	m, err = newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	event := hotRetentionTestEvent(source.UTCDay, 0, 10, "在途许可完整发布")
	_, err = m.deliver(source, []logtypes.Event{event}, false)
	require.NoError(t, err)
	require.ErrorIs(t, freezeErr, catalog.ErrWritePermitsActive)
	rec, ok := cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
	require.True(t, ok)
	require.Equal(t, uint64(10), publishedEvidenceClosed(rec, source))
	before := fixture.insertAttemptCount()
	late := source
	late.StorageNamespace = "new-namespace"
	late.LogSourceID = "new-source"
	_, err = m.writeProjection(late, []logtypes.Event{sixFixEvent(late, late.UTCDay, 0, 10, "冻结日新源")}, "late-copy", []logtypes.Event{sixFixEvent(late, late.UTCDay, 0, 10, "冻结日新源")}, nil, false)
	require.ErrorIs(t, err, catalog.ErrPhysicalDayFrozen)
	require.Equal(t, before, fixture.insertAttemptCount(), "新namespace在任何物理写之前拒绝")
}

func TestIngestSixFixVerifyBudgetObservesRealQueue(t *testing.T) {
	m := &Manager{verifyLimiter: newVerifyLimiter(VerifyBudget{MaxInFlight: 1, MaxPerSecond: 1000})}
	require.NoError(t, m.verifyLimiter.Acquire(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.verifyLimiter.Acquire(ctx) }()
	require.Eventually(t, func() bool { return m.VerifyBudgetStats().Waiting == 1 }, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	m.verifyLimiter.Release()
	stats := m.VerifyBudgetStats()
	require.Equal(t, uint64(1), stats.Acquired)
	require.Equal(t, uint64(1), stats.Completed)
	require.Equal(t, uint64(1), stats.Rejected)
	require.Equal(t, uint64(1), stats.WaitCount)
	require.Positive(t, stats.WaitNanoseconds)
	require.Zero(t, stats.Waiting)
	require.Zero(t, stats.InFlight)
}
