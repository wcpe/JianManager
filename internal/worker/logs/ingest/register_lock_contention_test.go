package ingest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 本文件是 2026-10-01 生产事故（CP 下发实例规格时，Worker 侧「登记实例日志采集」四次全
// DeadlineExceeded → 11 台实例无法启动）的回归：**登记路径不得被采集轮的长临界区阻塞**。

// **-race 豁免（窄化、有据、可复核）**：本文件里两条以**墙上时钟**为口径的用例
// （TestRegisterInstanceNotBlockedBySaturatedPollRound 的 1s 预算、
// TestRegisterInstanceLatencyBudgetUnderSaturation 的自适应预算）在 `-race` 构建下 **skip**，
// 依据是竞态检测器把每次内存访问都加了检查——同一夹具实测放慢 10–20 倍，把「一个提交单元」
// 从毫秒级抬到 ~1s 量级，于是绝对阈值不再度量「登记是否被整轮采集挡住」，只度量检测器开销 ✗。
//
// 豁免是**窄的**：只跳过绝对阈值那一层；**结构性断言**（登记必须在该轮结束之前返回、只等待短
// 临界区而不等 cycleMu）在两种构建下都保留 ✓。**生产口径（非 -race）实测数字**（同一夹具，
// 见下方 TestRegisterInstanceLatencyBudgetUnderSaturation 的日志输出口径）：
//   - 登记耗时：85.5ms / 168.6µs（两次忙期采样），8 次登记最坏 **35.6ms**（预算 1s）✓；
//   - 忙期**整轮采集墙钟**：**1.91s / 1.91s**（另一轮 651ms）。注意这不是 cycleMu 的
//     持有时长——cycleMu 只包 poll 工作集快照（runtime.go:3764 上锁、3784 释放）；
//   - `m.mu` 单次最长持有：**27.4ms**（观测到 12 次释放）。
// 实现见 race_on_test.go / race_off_test.go 的 raceEnabled（构建标签 `race`，**不使用
// testing.Short** ✗——Short 表达的是"跑得少一点"，而这里豁免的是"检测器放大了绝对时间"）。
//
// 事故机理（锁审计见 docs/specs/log-ingest-register-lock/spec.md）：
//   - pollOnce 持 cycleMu 跨「逐源 Poll → WAL → persist → VL 投递 → 投影校验」整轮；
//     采集侧饱和时单轮为秒级~分钟级（实测上限 ~1150–1250 行/s，单源单次 poll 2000 行）；
//   - RegisterInstance 此前也取 cycleMu，于是 CP 的 10 秒 RPC 截止时间内必然超时。
//
// 断言口径：**显式驱动一轮耗时长于登记截止的采集**，在该轮进行中给登记计时。
// 旧实现必须等整轮跑完（超时，转红），新实现必须在轮内毫秒级返回（转绿）。

// saturatedProjectionVL 在 projectionVLFixture 之上注入「VL 插入变慢」：复刻饱和采集侧
// 一次插入需要真实网络+索引时间的形态（生产单请求体量受 insertBatchMaxEvents=500 约束）。
// 查询（校验）路径沿用标准夹具，使整轮能正常收敛，而不是靠校验超时拖时间。
type saturatedProjectionVL struct {
	*projectionVLFixture
	insertDelay time.Duration
	inserts     atomic.Int64
	started     chan struct{}
}

func newSaturatedProjectionVL(t *testing.T, delay time.Duration) (*vlsup.Client, *saturatedProjectionVL) {
	t.Helper()
	fixture := &saturatedProjectionVL{
		projectionVLFixture: &projectionVLFixture{},
		insertDelay:         delay,
		started:             make(chan struct{}, 1),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/insert/jsonline" {
			fixture.inserts.Add(1)
			select {
			case fixture.started <- struct{}{}:
			default:
			}
			time.Sleep(fixture.insertDelay)
		}
		fixture.projectionVLFixture.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	return client, fixture
}

// waitForInsertInFlight 等到「本轮采集已经进入投递阶段」——此时 cycleMu 已被持有。
func (f *saturatedProjectionVL) waitForInsertInFlight(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-f.started:
	case <-time.After(timeout):
		t.Fatalf("采集轮未在 %s 内进入投递阶段，夹具未制造出忙期", timeout)
	}
}

// busyIngestFixture 构造一个「忙期」Manager：sources 个文件源，每源各有超过单次 poll
// 上限的积压，且每条 VL 插入被拖慢 → 单轮 pollOnce 达秒级。
type busyIngestFixture struct {
	manager *Manager
	fixture *saturatedProjectionVL
	root    string
}

func newBusyIngestFixture(t *testing.T, sources, linesPerSource int, insertDelay time.Duration) *busyIngestFixture {
	t.Helper()
	client, fixture := newSaturatedProjectionVL(t, insertDelay)
	root := t.TempDir()
	day := runtimeTestUTCDay()
	configs := make([]SourceConfig, 0, sources)
	for i := 0; i < sources; i++ {
		dir := filepath.Join(root, fmt.Sprintf("busy-%d", i))
		require.NoError(t, os.MkdirAll(dir, 0o755))
		logPath := filepath.Join(dir, "latest.log")
		var body strings.Builder
		for line := 0; line < linesPerSource; line++ {
			fmt.Fprintf(&body, "[12:%02d:%02d] [Server thread/INFO]: backlog %d line %d\n",
				line/60, line%60, i, line)
		}
		require.NoError(t, os.WriteFile(logPath, []byte(body.String()), 0o600))
		configs = append(configs, SourceConfig{
			LogSourceID: fmt.Sprintf("busy:%d", i), SourceGeneration: "g1", Path: logPath,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: fmt.Sprintf("busy:%d", i), UTCDay: day,
		})
	}
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: configs,
		VerificationTimeout: 500 * time.Millisecond,
	})
	require.NoError(t, err)
	return &busyIngestFixture{manager: m, fixture: fixture, root: root}
}

// pollRound 是一次**显式驱动**的采集轮。这里测的是**整轮墙钟**，不是 cycleMu 的持有时长：
// pollOnce 只在「读本轮工作集快照」这一段持 cycleMu（runtime.go:3764 上锁、3784 释放，
// workers 在锁内读取），其后分发 / Poll / 落库 / VL 投递全在锁外。
//
// 口径纠正（2026-10-24）：本文件此前写「pollOnce 全程持有 cycleMu，本轮时长就是该锁的
// 单次持有时长」，与实现不符，已改正。被测性质是「采集轮**进行中**登记能否返回」，
// 不依赖锁被持有多久；轮时长只用来证明「该轮确实足够长、登记不是被秒回」。
//
// 为什么不能改用「TryLock 探锁」量化 cycleMu 的持有时间：Go 的 sync.Mutex 在有等待者
// 排队超过 1ms 后进入饥饿模式，此时所有权由 Unlock 直接交给队首等待者、`mutexStarving`
// 位保持置位，而 `TryLock` 一见该位即返回 false——即便锁在那一瞬是空的。于是「持续有排队者」
// 的场景下（后台采集轮 + 250ms ticker 永远有下一个 pollOnce 在排队）**探针会连续数万次
// 全部失败，读出一个假的「无释放」**，用测得的持有时间会失真甚至恒为 0。
type pollRound struct {
	done    chan struct{}
	elapsed atomic.Int64
}

func startPollRound(m *Manager) *pollRound {
	round := &pollRound{done: make(chan struct{})}
	go func() {
		started := time.Now()
		m.pollOnce()
		round.elapsed.Store(int64(time.Since(started)))
		close(round.done)
	}()
	return round
}

func (r *pollRound) finished() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// waitFinished 等本轮结束并返回其实测时长。
func (r *pollRound) waitFinished(t *testing.T, timeout time.Duration) time.Duration {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(timeout):
		t.Fatalf("采集轮未在 %s 内结束", timeout)
	}
	return time.Duration(r.elapsed.Load())
}

// lockHoldSampler 以 TryLock 采样「某把锁连续不可获得」的最长时长，仅用于**忙期没有持续
// 排队者**的锁（本文件只用于 m.mu：轮内除采集自身外只有登记偶尔取它，队列瞬时清空，
// 不会进入饥饿模式）。cycleMu 的持有时间一律用 pollRound 直接测，见其注释。
type lockHoldSampler struct {
	mu    *sync.Mutex
	max   atomic.Int64
	holds atomic.Int64
	fails atomic.Int64
	stop  chan struct{}
	done  chan struct{}
}

func startLockHoldSampler(mu *sync.Mutex) *lockHoldSampler {
	s := &lockHoldSampler{mu: mu, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		var waiting time.Time
		for {
			if mu.TryLock() {
				if !waiting.IsZero() {
					if d := time.Since(waiting); int64(d) > s.max.Load() {
						s.max.Store(int64(d))
					}
					s.holds.Add(1)
					waiting = time.Time{}
				}
				mu.Unlock()
			} else {
				s.fails.Add(1)
				if waiting.IsZero() {
					waiting = time.Now()
				}
			}
			select {
			case <-s.stop:
				return
			case <-time.After(200 * time.Microsecond):
			}
		}
	}()
	return s
}

func (s *lockHoldSampler) stopSampler() (max time.Duration, holds, fails int64) {
	close(s.stop)
	<-s.done
	return time.Duration(s.max.Load()), s.holds.Load(), s.fails.Load()
}

// instanceBindingFor 生成一组实例绑定参数（workDir 由调用方给出）。
func instanceBindingFor(index int) (uuid, targetID, generation string) {
	return fmt.Sprintf("uuid-busy-%d", index), fmt.Sprintf("inst:%d", 900+index), "gen-20261001"
}

// registerOutcome 是**登记返回那一刻**的取样：耗时、错误，以及当时整轮是否已经结束。
//
// roundDone 必须在 RegisterInstance 返回后立刻读（见调用点注释）：它是「登记必须在该轮结束
// 之前返回」这条结构断言的唯一有效取样点。
type registerOutcome struct {
	err       error
	elapsed   time.Duration
	roundDone bool
}

// TestRegisterInstanceNotBlockedBySaturatedPollRound 是事故的直接回归。
//
// 两个子用例对应 CP 的两条真实调用：
//   - new_binding：CP 首次下发规格（新绑定，需建管道 + persist）；
//   - idempotent_rebind：CP 每次启动前的幂等重注册——事故当场的实际形态（绑定已存在，
//     登记本应只有两次 stat + 落库，却同样被整轮采集挡住）。
func TestRegisterInstanceNotBlockedBySaturatedPollRound(t *testing.T) {
	// 登记截止（registerDeadline）：CP 侧 CreateInstance 的 RPC 超时是 10s
	// （internal/controlplane/service/instance.go:registerOnWorkerLocked）。
	// 测试取 1s：既远小于真实 10s（留出 CI 抖动余量），又必须远小于忙期单轮时长，
	// 否则该用例无法区分「被长临界区挡住」与「正常登记开销」。
	const registerDeadline = time.Second
	// roundDeadline 是等待整轮采集结束的上界（原实现里写作字面量 30s，提到常量只为让
	// 「等登记返回」与「等整轮结束」共用同一个界，取值未变）。
	const roundDeadline = 30 * time.Second
	// insertDelay 是单次 VL 插入的注入延迟。取值从 200ms 提到 800ms（FR-498 并发采集轮之后）：
	// 轮内采集已跨源并发重叠，单轮时长不再等于「各源之和」而是「最慢源的路径」，200ms × 2 批
	// 在 6 源并发下只能造出 ≈0.9s 的忙期 → 触发下方「夹具未制造出长于登记截止时间的忙期」的
	// 前置断言而失去判别力（实测 921ms < 1s）。提高单源延迟即可在并发轮下重新造出 >1s 的忙期，
	// 断言本身（登记 < 1s、且在整轮结束前返回）保持不变。
	const insertDelay = 800 * time.Millisecond
	const busySources = 6
	const linesPerSource = 600
	// roundPollConcurrency 是本用例正式轮的源并发度，**只影响本夹具的轮内提交粒度**：
	// 本用例断言的是「采集轮进行中登记能否返回」，不是跨源并行吞吐（那是
	// TestPollRoundOverlapsSourceWaits 的职责），因此把该轮设为逐源串行不改变被测性质。
	//
	// 为什么必须设 1（2026-10-23 实测）：持久化门是单持有者，一次提交的行数随「同时已有
	// WAL 的源数」放大——6 源并发时首个持门者要提交 3618 行，-race 下耗时 3.15–3.22s，
	// 越过 stateindex 的每周期墙钟预算（DefaultCycleMaxDuration=3s）⇒ Incomplete=true ⇒
	// 连持门者自己也被交办 ⇒ 轮内零投递、夹具造不出忙期（失败在前置，与被测性质无关）。
	// 逐源串行后每次提交只覆盖一个源的 600 行，远低于该周期预算；且单调用者=门口无排队，
	// 生产默认 30ms 等门预算也不会被触发。**不改任何预算**：等门 30ms、周期 3s、
	// 800ms 插入延迟、1s 登记门、30s 轮尾全部原值。
	const roundPollConcurrency = 1

	for _, tc := range []struct {
		name        string
		preRegister bool
	}{
		{name: "new_binding"},
		{name: "idempotent_rebind", preRegister: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newBusyIngestFixture(t, busySources, linesPerSource, insertDelay)
			m := fx.manager
			// 仅本用例：正式轮逐源串行（理由见 roundPollConcurrency 注释）。默认并发
			// 8 的生产口径不变，同文件另一用例仍是默认并发。
			m.pollConcurrency = roundPollConcurrency
			uuid, targetID, generation := instanceBindingFor(0)
			workDir := t.TempDir()

			if tc.preRegister {
				// 忙期之前先登记，制造「绑定已存在」的幂等重注册场景。
				require.NoError(t, m.RegisterInstance(InstanceRegistration{
					UUID: uuid, TargetID: targetID, Generation: generation,
					Mode: string(pipeline.ModeStdioPrimary), WorkDir: workDir,
				}))
			}

			round := startPollRound(m)
			fx.fixture.waitForInsertInFlight(t, 10*time.Second)
			require.False(t, round.finished(), "夹具未造出忙期：整轮在登记前就已结束")

			// 「登记是否在该轮结束之前返回」必须在**登记返回的那一刻**取样。
			//
			// 2026-10-23 纠偏：原实现在 select 超时（1s）之后才读 round.finished()，而这个夹具
			// 又强制要求整轮 >1s —— 于是无论登记有没有被挡住，1s 时整轮必然还在跑，inRound 恒为
			// true：该"结构性断言"实际上没有判别力（-race 下唯一的判别就靠被豁免的绝对阈值，
			// 等于没有）。改为在 goroutine 里紧跟 RegisterInstance 返回后立刻取样，断言才真正
			// 表达"登记必须在该轮结束之前返回"，且在两种构建下都成立（不放宽、不新增 skip）。
			result := make(chan registerOutcome, 1)
			started := time.Now()
			go func() {
				err := m.RegisterInstance(InstanceRegistration{
					UUID: uuid, TargetID: targetID, Generation: generation,
					Mode: string(pipeline.ModeStdioPrimary), WorkDir: workDir,
				})
				// 取样点必须在返回之后、任何其它工作之前。
				result <- registerOutcome{
					err:       err,
					elapsed:   time.Since(started),
					roundDone: round.finished(),
				}
			}()

			// 等待登记真正返回（旧实现会一直等到整轮结束）；上界与整轮上界一致，避免挂死。
			var outcome registerOutcome
			select {
			case outcome = <-result:
			case <-time.After(roundDeadline):
				t.Fatalf("登记在 %s 内未返回（整轮仍未结束）", roundDeadline)
			}
			elapsed := outcome.elapsed
			registerErr := outcome.err
			inRound := !outcome.roundDone
			roundElapsed := round.waitFinished(t, roundDeadline)
			// 口径：这是**整轮采集墙钟**，不是 cycleMu 持有时间（cycleMu 只包快照段）。
			t.Logf("整轮采集墙钟 %s；登记耗时 %s；登记返回当刻整轮是否仍在跑=%v",
				roundElapsed, elapsed, inRound)

			// 前置条件：该轮确实远长于登记截止时间（否则「轮内返回」无从判别）。
			// 注意口径：这里证明的是「轮足够长」，不是「cycleMu 被持有得足够久」。
			require.Greater(t, roundElapsed, registerDeadline,
				"夹具未制造出长于登记截止时间的采集轮，用例失去判别力")
			// 判别性断言：登记必须在整轮采集**结束之前**返回。
			// 旧实现（pollOnce 整轮持 cycleMu + 登记也取 cycleMu）必然要等本轮跑完才返回。
			require.True(t, inRound,
				"登记直到整轮采集结束才返回（耗时 %s，单轮 %s）：仍被长临界区阻塞", elapsed, roundElapsed)
			require.NoError(t, registerErr)
			// 绝对阈值只在普通构建下断言（-race 下豁免，理由见文件头）：
			// 结构性断言（inRound：登记必须在该轮**结束之前**返回）在两种构建下都保留 ✓。
			if !raceEnabled {
				require.Less(t, elapsed, registerDeadline,
					"登记耗时 %s 达到/超过截止 %s：仍被长临界区阻塞", elapsed, registerDeadline)
			}

			// 无丢失绑定：内存状态、管道、索引库三处都要落到。
			require.Empty(t, m.MissingInstanceBindings([]string{uuid}))
			require.NotNil(t, m.pipes[targetID+"/stdout/"+generation], "stdout 源应已登记")
			require.NotNil(t, m.pipes[targetID+"/stderr/"+generation], "stderr 源应已登记")
			m.mu.Lock()
			index := m.index
			m.mu.Unlock()
			require.NotNil(t, index)
			rows, err := index.Load()
			require.NoError(t, err)
			persisted, err := indexStateToState(rows)
			require.NoError(t, err)
			require.Equal(t, InstanceBinding{
				UUID: uuid, TargetID: targetID, Generation: generation,
				Mode: pipeline.ModeStdioPrimary, WorkDir: filepath.Clean(workDir),
			}, persisted.Instances[uuid], "登记必须已落库，不能只留在内存")

			// 登记之后采集本身不受影响：追加新行后仍能被采集并投递出去。
			before := fx.fixture.inserts.Load()
			logPath := filepath.Join(fx.root, "busy-0", "latest.log")
			f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600)
			require.NoError(t, err)
			_, err = f.WriteString("[13:00:00] [Server thread/INFO]: after register\n")
			require.NoError(t, err)
			require.NoError(t, f.Close())
			m.pollOnce()
			require.Greater(t, fx.fixture.inserts.Load(), before, "登记不应打断采集轮")
		})
	}
}

// TestRegisterInstanceLatencyBudgetUnderSaturation 量化登记路径的等待面：
// 登记只应等待「短临界区」（m.mu：建管道、写索引），不应等待 cycleMu（整轮采集）。
//
// 判据用两个量对比而不是绝对阈值：忙期**整轮采集墙钟**，与登记实际耗时。
// 两者相差一个数量级（≥4x）以上，才说明阻塞源被换成了短锁。
//
// 口径纠正（2026-10-24）：此前写作「忙期单轮持有 cycleMu 的时长」，与实现不符——
// cycleMu 只包 poll 工作集快照（runtime.go:3764 上锁、3784 释放），其后分发 / Poll /
// 落库 / VL 投递全在锁外，故整轮墙钟 ≠ cycleMu 持有时长。被测性质是「登记不等整轮」，
// 不是「锁被持有得久」。
func TestRegisterInstanceLatencyBudgetUnderSaturation(t *testing.T) {
	if raceEnabled {
		t.Skip("竞态检测器把索引写入放大约 10–20×（同一夹具：生产口径最坏 35.6ms vs 预算 1s；" +
			"-race 下单单元被抬到 ~1s 量级）⇒ 绝对阈值在 -race 下失去判别力；" +
			"结构性断言（登记在该轮结束之前返回、只等短临界区）在两种构建下均保留，见文件头说明")
	}
	if testing.Short() {
		t.Skip("饱和夹具在 -short 下不跑")
	}
	fx := newBusyIngestFixture(t, 4, 600, 200*time.Millisecond)
	m := fx.manager
	uuid, targetID, generation := instanceBindingFor(7)
	workDir := t.TempDir()

	muHold := startLockHoldSampler(&m.mu)
	round := startPollRound(m)
	fx.fixture.waitForInsertInFlight(t, 10*time.Second)

	latencies := make([]time.Duration, 0, 8)
	for i := 0; i < 8; i++ {
		started := time.Now()
		require.NoError(t, m.RegisterInstance(InstanceRegistration{
			UUID: uuid, TargetID: targetID, Generation: generation,
			Mode: string(pipeline.ModeStdioPrimary), WorkDir: workDir,
		}))
		latencies = append(latencies, time.Since(started))
		require.False(t, round.finished(), "第 %d 次登记：整轮采集在登记期内就已结束（夹具太短）", i+1)
	}
	roundElapsed := round.waitFinished(t, 30*time.Second)
	muMax, muHolds, muFails := muHold.stopSampler()
	worst := time.Duration(0)
	for _, l := range latencies {
		if l > worst {
			worst = l
		}
	}
	t.Logf("忙期：整轮采集墙钟 %s（cycleMu 只包快照段，非全程持有）；m.mu 单次最长持有 %s（观测到 %d 次释放，%d 次失败采样）；登记 8 次最坏 %s",
		roundElapsed, muMax, muHolds, muFails, worst)
	// 前置条件：夹具确实造出了忙期。
	require.Greater(t, roundElapsed, 100*time.Millisecond, "夹具未制造出忙期")
	// 登记耗时必须与「整轮采集」脱钩：旧实现是等整轮跑完（worst ≈ 单轮时长），
	// 新实现只等短临界区（worst 为毫秒级）。取 1/4 作为判据留出 CI 抖动余量。
	require.Less(t, worst, roundElapsed/4,
		"登记耗时与整轮采集同级，说明仍在等待长临界区：登记=%s 单轮=%s", worst, roundElapsed)
}
