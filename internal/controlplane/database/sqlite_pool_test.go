package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
)

// poolTestRow 是本文件专用的最小模型：不引入真实业务模型的 NOT NULL 约束，
// 让并发/计数类断言只依赖本测试自己的表结构。
type poolTestRow struct {
	ID   uint   `gorm:"primaryKey"`
	Name string `gorm:"index"`
	Sort int
}

// openTestPool 建一个走 database.New 全链路的 SQLite 库（含 guardConn 与写闸门）。
func openTestPool(t *testing.T, name string, extraQuery string) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), name+".db") + extraQuery
	db, err := New(config.DatabaseConfig{Driver: "sqlite", DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, derr := db.DB(); derr == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// rawSQLDB 取回底层连接池（经 gorm.GetDBConnector），供测试直接操纵连接与读连接池统计。
func rawSQLDB(t *testing.T, db *gorm.DB) *sql.DB {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NotNil(t, sqlDB)
	return sqlDB
}

func mustSnapshot(t *testing.T, db *gorm.DB) TxGuardSnapshot {
	t.Helper()
	snap, ok := TxGuardSnapshotOf(db)
	require.True(t, ok, "应能取到事务守卫快照（ConnPool 为 gatedConnPool）")
	return snap
}

func mustGuard(t *testing.T, db *gorm.DB) *sqliteTxGuard {
	t.Helper()
	pool, ok := db.ConnPool.(*gatedConnPool)
	require.True(t, ok, "ConnPool 应是 gatedConnPool")
	require.NotNil(t, pool.guard)
	return pool.guard
}

// ---------------------------------------------------------------------------
// ① 核心：毒化连接不被复用
// ---------------------------------------------------------------------------

// TestSQLiteTxPoison_ConnNotReused 是本次改造的**核心回归**：证明「带着未提交事务回到池中」
// 的连接在放开连接数之后**不会被复用**。
//
// 构造方式（沿用 sqlite_txpoison_test.go 的确定性手法，但走 New 建的真连接池）：
//  1. 在池上钉住两条物理连接，读者持读事务占住 SHARED 锁；
//  2. 写者在写事务里 UPDATE 后 COMMIT——rollback journal 模式下 COMMIT 需 EXCLUSIVE，
//     被读者挡住，busy_timeout 耗尽后返回 SQLITE_BUSY，此时 **SQLite 的事务仍然打开**
//     （COMMIT 因 BUSY 失败不会自动回滚，这是 FR-318 的驱动级事实）；
//  3. 让写者连接「回池」（Conn.Close 与请求结束走的是同一条 putConn 路径）。
//
// 本用例刻意把库切回 rollback journal（journal_mode=DELETE）来构造毒化：WAL 下读者挡不住
// 写者 COMMIT，这个场景根本撞不出来。要验证的正是「即便退化到 journal 模式，FR-318 的
// 故障面仍被 IsValid 驱逐覆盖」——即防护不再依赖「WAL 一定可用」这一前提。
//
// 修复前（驱动不实现 driver.Validator）：database/sql 原样把毒连接放回空闲队列且永不驱逐，
// 后续请求抽到它必然复现 `cannot start a transaction within a transaction`。
func TestSQLiteTxPoison_ConnNotReused(t *testing.T) {
	// busy_timeout 调小，让 COMMIT 尽快以 SQLITE_BUSY 失败。
	db := openTestPool(t, "poison_guard", "?_pragma=busy_timeout(50)")
	raw := rawSQLDB(t, db)

	_, err := raw.ExecContext(context.Background(), "PRAGMA journal_mode=DELETE")
	require.NoError(t, err)
	_, err = raw.ExecContext(context.Background(), "CREATE TABLE t (id INTEGER PRIMARY KEY, v INTEGER)")
	require.NoError(t, err)
	_, err = raw.ExecContext(context.Background(), "INSERT INTO t (id, v) VALUES (1, 0)")
	require.NoError(t, err)

	ctx := context.Background()
	reader, err := raw.Conn(ctx)
	require.NoError(t, err)
	defer reader.Close()
	writer, err := raw.Conn(ctx)
	require.NoError(t, err)

	// 读者取 SHARED 锁并持有到事务结束。
	rtx, err := reader.BeginTx(ctx, nil)
	require.NoError(t, err)
	var v int
	require.NoError(t, rtx.QueryRowContext(ctx, "SELECT v FROM t WHERE id = 1").Scan(&v))

	// 写者 UPDATE 拿 RESERVED 锁；COMMIT 需 EXCLUSIVE，被读者挡住。
	wtx, err := writer.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = wtx.ExecContext(ctx, "UPDATE t SET v = 1 WHERE id = 1")
	require.NoError(t, err)
	commitErr := wtx.Commit()
	if commitErr == nil {
		t.Skip("驱动 COMMIT 未再因读锁 BUSY 失败（驱动行为已变化），无法构造毒化连接")
	}
	require.Contains(t, commitErr.Error(), "locked",
		"预期 COMMIT 因 SQLITE_BUSY 失败，实际: %v", commitErr)

	// 毒化已发生并被计数（COMMIT 失败 = 事务可能仍打开）。
	require.GreaterOrEqual(t, mustSnapshot(t, db).DirtyCommits, int64(1),
		"COMMIT 失败应被计入 dirtyCommits")

	// 关键一步：把毒化连接交回池。database/sql 回池前会调 driver.Validator.IsValid，
	// 该连接必须被判为不可复用并被真正关闭（numOpen 减一）。
	before := raw.Stats().OpenConnections
	require.NoError(t, writer.Close())

	require.Eventually(t, func() bool {
		s := mustSnapshot(t, db)
		return s.EvictedConns >= 1 && raw.Stats().OpenConnections == before-1
	}, 2*time.Second, 10*time.Millisecond,
		"毒化连接必须被驱逐（EvictedConns 增长 且 连接被真正关闭）")

	// 逐条固化「不进空闲队列」这一事实：连接数不回升，说明它没有被当作空闲连接保留。
	require.Equal(t, before-1, raw.Stats().OpenConnections,
		"毒化连接必须被关闭而不是放回空闲队列")

	require.NoError(t, rtx.Rollback())
	require.NoError(t, reader.Close())

	// 决定性行为证据：驱逐之后，池上后续取连接不再报嵌套事务错。
	// 若 IsValid 未生效，毒连接会一直留在池里，这里必然复现 FR-318 的线上报错
	// （`cannot start a transaction within a transaction`），也就是「重启 CP 即愈」的那条。
	for i := 0; i < 20; i++ {
		newTx, err := raw.BeginTx(ctx, nil)
		require.NoErrorf(t, err, "第 %d 次取连接不应失败（毒化连接应已被驱逐）", i)
		_, err = newTx.ExecContext(ctx, "UPDATE t SET v = v + 1 WHERE id = 1")
		require.NoErrorf(t, err, "第 %d 次写入不应失败", i)
		require.NoErrorf(t, newTx.Commit(), "第 %d 次提交不应失败", i)
	}

	var finalV int
	require.NoError(t, raw.QueryRowContext(ctx, "SELECT v FROM t WHERE id = 1").Scan(&finalV))
	// 毒化那次 UPDATE 的 COMMIT 因 BUSY 失败 → 未落地；其后的 20 次提交全部生效。
	// 这个数字同时反证了「驱逐不是为了掩盖错误」：失败的那次确实没有静默写入。
	require.Equal(t, 20, finalV, "20 次成功提交应全部生效，且失败的毒化提交不得落地")
}

// TestSQLitePool_ResetSessionRejectsPoisonedConn 覆盖毒化检测的**第二道闸口**。
//
// IsValid 管「回池时」，ResetSession 管「取出时」。前者是主闸口，但如果某条路径绕过了
// putConn 的校验（或未来驱动/标准库行为变化），取出时的复查就是最后一道防线：返回
// driver.ErrBadConn 会让 database/sql 丢弃该连接并在新连接上重试（db.retry），
// 因此业务请求绝不会拿到一条事务中的连接。
func TestSQLitePool_ResetSessionRejectsPoisonedConn(t *testing.T) {
	guard := &sqliteTxGuard{}

	// 干净连接：ResetSession 必须放行（否则每条连接每次复用都要重建，退化成无池）。
	clean := newGuardConn(&stubConn{}, guard)
	require.NoError(t, clean.ResetSession(context.Background()))

	// 污染连接：必须返回 driver.ErrBadConn。
	poisoned := newGuardConn(&stubConn{}, guard)
	poisoned.txOpen.Store(true)
	err := poisoned.ResetSession(context.Background())
	require.ErrorIs(t, err, driver.ErrBadConn,
		"取出时发现连接仍在事务中，必须以 ErrBadConn 让标准库丢弃它")
	require.GreaterOrEqual(t, guard.evictedConns.Load(), int64(1))

	// 事务正常结束后必须恢复放行（否则会误杀所有正常复用，连接池形同虚设）。
	tx, terr := poisoned.Begin()
	require.NoError(t, terr)
	require.NoError(t, tx.Rollback())
	require.NoError(t, poisoned.ResetSession(context.Background()),
		"事务正常结束后连接应可复用")
}

// TestGuardConnPreservesDriverInterfaces 守护包装器不得丢失驱动的可选接口，并固化
// FR-318 赖以成立的驱动事实。
//
// 两个方向都要守：
//   - 原生驱动**不实现** driver.Validator / SessionResetter —— 这正是 FR-318 的根因
//     （database/sql 因此无法识别并驱逐带打开事务的连接）。一旦驱动改版补上这两个接口，
//     本用例会失败提醒我们重新评估防线是否还需要自建。
//   - 包装后**必须保留** ExecerContext / QueryerContext / ConnPrepareContext / ConnBeginTx /
//     Pinger —— 否则 database/sql 的接口断言全部落空、退化到 Prepare 路径，性能与语义都会变。
func TestGuardConnPreservesDriverInterfaces(t *testing.T) {
	probe, err := sql.Open(sqlite.DriverName, filepath.Join(t.TempDir(), "iface.db"))
	require.NoError(t, err)
	base := probe.Driver()
	defer probe.Close()

	rawConn, err := base.Open(filepath.Join(t.TempDir(), "iface2.db"))
	require.NoError(t, err)
	defer rawConn.Close()

	// 驱动层事实：原生连接没有 Validator / SessionResetter。
	_, hasValidator := rawConn.(driver.Validator)
	_, hasResetter := rawConn.(driver.SessionResetter)
	require.False(t, hasValidator,
		"原生驱动已实现 driver.Validator：本方案的判据前提变化，请复核是否还需要自建检测")
	require.False(t, hasResetter,
		"原生驱动已实现 driver.SessionResetter：同上，请复核 FR-318 防线的必要性")

	wrapped := newGuardConn(rawConn, &sqliteTxGuard{})

	// 包装后新增的两个能力（本方案的检测/驱逐入口）。
	require.Implements(t, (*driver.Validator)(nil), wrapped, "包装后必须实现 driver.Validator")
	require.Implements(t, (*driver.SessionResetter)(nil), wrapped, "包装后必须实现 driver.SessionResetter")
	// 包装后必须原样保留驱动已有的 5 个可选接口。
	require.Implements(t, (*driver.ExecerContext)(nil), wrapped, "丢失 ExecerContext 会劣化查询路径")
	require.Implements(t, (*driver.QueryerContext)(nil), wrapped, "丢失 QueryerContext 会劣化查询路径")
	require.Implements(t, (*driver.ConnPrepareContext)(nil), wrapped, "丢失 ConnPrepareContext 会劣化 Prepare")
	require.Implements(t, (*driver.ConnBeginTx)(nil), wrapped, "丢失 ConnBeginTx 会丢失事务 context 支持")
	require.Implements(t, (*driver.Pinger)(nil), wrapped, "丢失 Pinger 会影响探活")
}

// TestSQLiteTxPoison_ControlWithoutGuardReuses 是上面用例的**对照实验**，用于排除
// 「毒化本来就不会发生」这一解释：同样的驱动、同样的锁顺序，只把 guardConn 包装换成直通
// 包装（等价于修复前的驱动形态——不实现 driver.Validator / SessionResetter），毒化连接
// 就会被原样复用，并复现 FR-318 的线上报错。
//
// 两条用例合起来构成差分证据：
//   - 直通（修复前形态）→ 复用毒连接 → `cannot start a transaction within a transaction`
//   - guardConn（本方案）→ 驱逐毒连接 → 后续事务全部成功
//
// 因此「毒连接不被复用」确实归因于本方案，而不是归因于场景没构造出来。
//
// 确定性关键：读者与写者**各用一个 MaxOpenConns=1 的池**，从而把两种角色钉在各自的物理
// 连接上，不依赖「连取两次 Conn 恰好拿到不同连接」这种调度运气。两个池指向同一个库文件。
func TestSQLiteTxPoison_ControlWithoutGuardReuses(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "poison_control.db") + "?_pragma=busy_timeout(50)"

	probe, err := sql.Open(sqlite.DriverName, dsn)
	require.NoError(t, err)
	baseDriver := probe.Driver()
	require.NoError(t, probe.Close())

	// 直通 connector：返回原始驱动连接，不附加任何可选接口 —— 即修复前的驱动形态。
	newPlainPool := func() *sql.DB {
		pool := sql.OpenDB(&plainConnector{base: baseDriver, dsn: dsn})
		pool.SetMaxOpenConns(1)
		pool.SetMaxIdleConns(1)
		return pool
	}
	readerPool := newPlainPool()
	writerPool := newPlainPool()
	defer readerPool.Close()
	defer writerPool.Close()

	ctx := context.Background()
	_, err = readerPool.ExecContext(ctx, "PRAGMA journal_mode=DELETE")
	require.NoError(t, err)
	_, err = readerPool.ExecContext(ctx, "CREATE TABLE t (id INTEGER PRIMARY KEY, v INTEGER)")
	require.NoError(t, err)
	_, err = readerPool.ExecContext(ctx, "INSERT INTO t (id, v) VALUES (1, 0)")
	require.NoError(t, err)

	// 读者取 SHARED 锁并持有到事务结束。
	reader, err := readerPool.Conn(ctx)
	require.NoError(t, err)
	defer reader.Close()
	rtx, err := reader.BeginTx(ctx, nil)
	require.NoError(t, err)
	var v int
	require.NoError(t, rtx.QueryRowContext(ctx, "SELECT v FROM t WHERE id = 1").Scan(&v))

	// 写者：UPDATE 拿 RESERVED 锁，COMMIT 需 EXCLUSIVE 被读者挡住 → SQLITE_BUSY。
	writer, err := writerPool.Conn(ctx)
	require.NoError(t, err)
	wtx, err := writer.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = wtx.ExecContext(ctx, "UPDATE t SET v = 1 WHERE id = 1")
	require.NoError(t, err)
	commitErr := wtx.Commit()
	if commitErr == nil {
		t.Skip("驱动 COMMIT 未再因读锁 BUSY 失败（驱动行为已变化），对照组无法构造")
	}
	require.Contains(t, commitErr.Error(), "locked")

	// 交回写连接：无 Validator / SessionResetter ⇒ database/sql 原样放回该池的空闲队列
	// （这就是修复前的行为，也是「重启即愈」的成因）。
	require.NoError(t, writer.Close())

	// 该池只有一条连接，所以下一次取连接**必然**取到刚才那条毒连接。
	reused, err := writerPool.Conn(ctx)
	require.NoError(t, err)
	defer reused.Close()
	_, beginErr := reused.BeginTx(ctx, nil)
	require.Error(t, beginErr, "对照组：毒化连接被复用时必须复现 FR-318 报错")
	require.Contains(t, beginErr.Error(), "cannot start a transaction within a transaction",
		"对照组应复现「事务内不能开事务」，实际: %v", beginErr)
	t.Logf("对照组复现成功（修复前形态）: %v", beginErr)

	require.NoError(t, rtx.Rollback())
}

// plainConnector 是直通 connector：把底层驱动连接原样返回，不附加任何可选接口。
// 用于模拟修复前的驱动形态（无 driver.Validator / SessionResetter）。
type plainConnector struct {
	base driver.Driver
	dsn  string
}

func (c *plainConnector) Connect(context.Context) (driver.Conn, error) { return c.base.Open(c.dsn) }
func (c *plainConnector) Driver() driver.Driver                        { return c.base }

// TestSQLitePool_DirtyCommitErrorStaysPoisoned 单元固化 guardTx 的失败分支：
// COMMIT / ROLLBACK 失败的连接必须保持「事务中」标记，从而必然被驱逐。
//
// 为什么不能只靠「COMMIT 返回错误」判断：SQLite 仅在部分错误下自动回滚，BUSY 时事务保持
// 打开。分辨错误码既脆弱又容易漏，因此这里一律保持污染标记——代价是偶尔多建一条连接，
// 换来的是不会漏掉真正的悬挂事务（宁可错杀，不可放过）。
func TestSQLitePool_DirtyCommitErrorStaysPoisoned(t *testing.T) {
	guard := &sqliteTxGuard{}
	gc := newGuardConn(&stubConn{}, guard)

	// 正常路径：干净连接必须被判为可用（否则会退化成「每条连接用完即弃」）。
	require.True(t, gc.IsValid(), "无未提交事务的连接必须可用")

	// BEGIN 失败却报「事务内不能开事务」⇒ 连接上确实残留着打开事务，必须标记污染。
	// 这是 FR-317 线上症状的形态（重启 CP 才愈），必须被识别而不是继续复用。
	_, err := gc.trackTx(nil, fmt.Errorf("SQL logic error: cannot start a transaction within a transaction (1)"), nil)
	require.Error(t, err)
	require.False(t, gc.IsValid(), "报嵌套事务错的连接必须被判为不可复用")

	// COMMIT 失败：连接仍打开，必须保持污染。
	gc2 := newGuardConn(&stubConn{}, guard)
	gc2.txOpen.Store(true)
	require.Error(t, (&guardTx{Tx: &failingTx{}, conn: gc2}).Commit())
	require.True(t, gc2.txOpen.Load(), "COMMIT 失败后连接必须仍被标记为事务中")
	require.False(t, gc2.IsValid(), "COMMIT 失败后连接必须不再复用")
	require.GreaterOrEqual(t, guard.dirtyCommits.Load(), int64(1))

	// ROLLBACK 也失败：事务状态未知，必须保持污染。
	gc3 := newGuardConn(&stubConn{}, guard)
	gc3.txOpen.Store(true)
	require.Error(t, (&guardTx{Tx: &failingTx{}, conn: gc3}).Rollback())
	require.False(t, gc3.IsValid(), "ROLLBACK 失败后连接必须不再复用")
	require.GreaterOrEqual(t, guard.dirtyRollbacks.Load(), int64(1))

	// 干净结束的连接必须被交还复用（不能把正常路径也判死）。
	gc4 := newGuardConn(&stubConn{}, guard)
	_, err = gc4.Begin()
	require.NoError(t, err)
	tx, err := gc4.BeginTx(context.Background(), driver.TxOptions{})
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	require.True(t, gc4.IsValid(), "正常回滚后连接应恢复可用")

	require.GreaterOrEqual(t, guard.evictedConns.Load(), int64(3), "三次污染都应触发驱逐计数")
	t.Logf("毒化计数: dirtyCommits=%d dirtyRollbacks=%d evicted=%d",
		guard.dirtyCommits.Load(), guard.dirtyRollbacks.Load(), guard.evictedConns.Load())
}

// ---------------------------------------------------------------------------
// ② 并发读写下不再出现嵌套事务错
// ---------------------------------------------------------------------------

// TestSQLitePool_ConcurrentNoNestedTxError 是 FR-318 压力门在**新形态**下的对应版本：
// 连接数已放开（多连接），并发状态转换写 + 事务内建/删 + 故意回滚 + 慢读者混跑，
// 断言全程不出现 `cannot start a transaction within a transaction` 与 `database is locked`。
//
// 与既有 TestNewSQLiteStressNoNestedTransactionError 的差别：那个用例依赖「单连接无锁竞争」
// 来保证恒绿；本用例直接跑在放开后的多连接池上，绿说明防护来自检测/驱逐与 WAL + 写闸门，
// 而不是来自「回避并发」。
func TestSQLitePool_ConcurrentNoNestedTxError(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "concurrent.db")
	db, err := New(config.DatabaseConfig{Driver: "sqlite", DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { closeDB(t, db) })
	require.NoError(t, db.AutoMigrate(&poolTestRow{}))

	// 确认确实跑在多连接形态（否则本用例退化成与旧用例等价，失去意义）。
	require.Greater(t, mustSnapshot(t, db).MaxOpenConns, 1, "本用例必须跑在放开连接数的形态下")
	require.True(t, mustSnapshot(t, db).WAL, "本用例要求 WAL 生效")

	require.NoError(t, db.Create(&poolTestRow{Name: "concurrent", Sort: 0}).Error)

	var (
		mu       sync.Mutex
		failures []string
	)
	record := func(op string, err error) {
		if err == nil {
			return
		}
		mu.Lock()
		failures = append(failures, fmt.Sprintf("%s: %v", op, err))
		mu.Unlock()
	}

	const (
		writers        = 6
		itersPerWriter = 20
	)
	writersDone := make(chan struct{})

	// 慢读者：长持只读事务，模拟列表页大查询 / swap 抖动放大的持锁窗口。
	var readerWG sync.WaitGroup
	for r := 0; r < 2; r++ {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			for {
				select {
				case <-writersDone:
					return
				default:
				}
				var rows []poolTestRow
				record("reader list", db.Where("name = ?", "concurrent").Find(&rows).Error)
				var n int64
				record("reader count", db.Model(&poolTestRow{}).Count(&n).Error)
			}
		}()
	}

	sentinel := fmt.Errorf("intentional rollback")
	var writerWG sync.WaitGroup
	for w := 0; w < writers; w++ {
		writerWG.Add(1)
		go func(w int) {
			defer writerWG.Done()
			for i := 0; i < itersPerWriter; i++ {
				// ① 状态回写式条件更新（GORM 默认事务）。
				record("update", db.Model(&poolTestRow{}).Where("name = ?", "concurrent").
					Update("sort", (w+i)%7).Error)

				// ② 事务内建 + 删（对应删 bot 链路的显式事务）。
				record("create+delete tx", db.Transaction(func(tx *gorm.DB) error {
					row := &poolTestRow{Name: fmt.Sprintf("r-%d-%d", w, i)}
					if err := tx.Create(row).Error; err != nil {
						return err
					}
					return tx.Unscoped().Delete(row).Error
				}))

				// ③ 故意失败回滚：回滚后连接必须干净可复用。
				if err := db.Transaction(func(tx *gorm.DB) error {
					if err := tx.Create(&poolTestRow{Name: "rollback-me"}).Error; err != nil {
						return err
					}
					return sentinel
				}); err != nil && err != sentinel {
					record("rollback tx", err)
				}
			}
		}(w)
	}

	writerWG.Wait()
	close(writersDone)
	readerWG.Wait()

	mu.Lock()
	defer mu.Unlock()
	require.Empty(t, failures, "并发混跑出现 %d 个错误:\n%s", len(failures), strings.Join(failures, "\n"))

	// 计数侧证据：毒化驱逐应为零（WAL + 写闸门已把 BUSY 面消掉），
	// 且写闸门承担了排队职责。
	snap := mustSnapshot(t, db)
	require.Equal(t, int64(0), snap.EvictedConns,
		"WAL + 写闸门下不应出现需要驱逐的毒化连接（若出现说明并发形态仍有缺口）")
	require.Equal(t, int64(0), snap.DirtyCommits, "WAL 下不应再有 COMMIT 失败")
	require.Greater(t, snap.WriteGateWaits, int64(0), "并发写应经写闸门排队")
	t.Logf("并发混跑计数: gateWaits=%d gateWait=%v maxOpen=%d wal=%v",
		snap.WriteGateWaits, snap.WriteGateDelay, snap.MaxOpenConns, snap.WAL)
}

// ---------------------------------------------------------------------------
// ③ 连接数放开后仍保证写串行正确性
// ---------------------------------------------------------------------------

// TestSQLitePool_WritesStillSerialized 验证写串行正确性：连接数放开后，写仍然只有一个写者。
//
// 判据刻意选择**可复现的计数丢失**而不是「数重叠数」（后者受调度影响，容易假绿）：
// 以事务内的「读-改-写」更新同一行，若写真的串行则最终值必然等于总次数；一旦两个写事务
// 重叠，后写者会基于陈旧快照覆盖前写者，计数必然偏小。
func TestSQLitePool_WritesStillSerialized(t *testing.T) {
	db := openTestPool(t, "write_serial", "")
	require.NoError(t, db.AutoMigrate(&poolTestRow{}))
	require.NoError(t, db.Create(&poolTestRow{Name: "counter", Sort: 0}).Error)
	require.Greater(t, mustSnapshot(t, db).MaxOpenConns, 1, "本用例要求多连接形态")

	const (
		writers        = 8
		itersPerWriter = 15
		want           = writers * itersPerWriter
	)

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < itersPerWriter; i++ {
				err := db.Transaction(func(tx *gorm.DB) error {
					var row poolTestRow
					if err := tx.Where("name = ?", "counter").First(&row).Error; err != nil {
						return err
					}
					return tx.Model(&poolTestRow{}).Where("id = ?", row.ID).
						Update("sort", row.Sort+1).Error
				})
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err, "并发写不应报错（写闸门应串行化，不出现 database is locked）")
	}

	var final poolTestRow
	require.NoError(t, db.Where("name = ?", "counter").First(&final).Error)
	require.Equal(t, want, final.Sort,
		"并发写未串行化：期望 %d 次累加，实际 %d（存在写覆盖）", want, final.Sort)

	snap := mustSnapshot(t, db)
	require.Greater(t, snap.WriteGateWaits, int64(0), "并发写应经写闸门排队")
	require.Equal(t, int64(0), snap.EvictedConns, "串行化正确时不应有连接被驱逐")
}

// TestSQLitePool_ReadNotBlockedByLongWrite 验证本次报障的直接成因被消除：
// 一个长写任务独占写闸门期间，**只读查询仍能正常返回**，不再排队上百秒。
//
// 修复前（单连接）：长事务持唯一连接，所有读请求排队——线上 `SELECT * FROM nodes` 查 1 行
// 也要 4064ms（纯排队），单次持锁最慢 145.8s。
func TestSQLitePool_ReadNotBlockedByLongWrite(t *testing.T) {
	db := openTestPool(t, "read_not_blocked", "")
	require.NoError(t, db.AutoMigrate(&poolTestRow{}))
	require.Greater(t, mustSnapshot(t, db).MaxOpenConns, 1, "本用例要求多连接形态")

	for i := 0; i < 5; i++ {
		require.NoError(t, db.Create(&poolTestRow{Name: fmt.Sprintf("n-%d", i)}).Error)
	}

	// 长写任务：拿住写事务不放（模拟指标卷积 / 大迁移）。
	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	var writeErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		writeErr = db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&poolTestRow{}).Where("name = ?", "n-0").
				Update("sort", 99).Error; err != nil {
				return err
			}
			close(writeStarted)
			<-releaseWrite
			return nil
		})
	}()

	select {
	case <-writeStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("长写任务未能在 10s 内开始")
	}

	// 长写任务持闸门期间，读必须畅通且看到的是已提交快照（写未提交 → 5 行原样）。
	type readResult struct {
		count int64
		took  time.Duration
		err   error
	}
	results := make(chan readResult, 1)
	go func() {
		start := time.Now()
		var n int64
		err := db.Model(&poolTestRow{}).Count(&n).Error
		results <- readResult{count: n, took: time.Since(start), err: err}
	}()

	select {
	case r := <-results:
		require.NoError(t, r.err)
		require.Equal(t, int64(5), r.count, "读应看到长写任务提交前的快照")
		// 读不应等长写任务释放；给足余量但仍远小于「写任务一直不放」的量级。
		require.Less(t, r.took, 3*time.Second,
			"长写任务持闸门期间只读查询仍在排队（耗时 %v）——正是本次报障的形态", r.took)
		t.Logf("长写任务持锁期间只读查询耗时 %v（修复前为纯排队 4064ms 量级）", r.took)
	case <-time.After(15 * time.Second):
		t.Fatal("长写任务期间只读查询被彻底堵死（15s 未返回）——回退到单连接后才会如此")
	}

	close(releaseWrite)
	wg.Wait()
	require.NoError(t, writeErr)
}

// ---------------------------------------------------------------------------
// ④ 逃生阀 / 降级路径
// ---------------------------------------------------------------------------

// TestSQLitePool_EscapeHatchSingleConn 验证逃生阀：环境变量设为 1 时必须完全退回改动前行为。
//
// 逃生阀的意义是「回到已知形态」，因此三项都必须回到原点：单连接、不切 WAL、不启用写闸门。
// 任一残留都会让回退失去确定性（例如 WAL 是持久化属性，会留在数据库文件头上）。
func TestSQLitePool_EscapeHatchSingleConn(t *testing.T) {
	t.Setenv(envSQLiteMaxOpenConns, "1")
	db := openTestPool(t, "escape_hatch", "")

	snap := mustSnapshot(t, db)
	require.Equal(t, 1, snap.MaxOpenConns, "逃生阀应把连接数收敛为 1")
	require.False(t, snap.WAL, "逃生阀路径不得切换 WAL（必须回到已知形态）")
	require.False(t, snap.Degraded, "逃生阀是显式选择，不应报降级")
	require.False(t, snap.WriteGateActive, "单连接下不应启用写闸门")
	require.Equal(t, 1, rawSQLDB(t, db).Stats().MaxOpenConnections)

	// 原行为仍可用：建表、写入、事务、回滚都要正常。
	require.NoError(t, db.AutoMigrate(&poolTestRow{}))
	require.NoError(t, db.Create(&poolTestRow{Name: "escape"}).Error)

	sentinel := fmt.Errorf("intentional")
	err := db.Transaction(func(tx *gorm.DB) error {
		if cerr := tx.Create(&poolTestRow{Name: "rollback-me"}).Error; cerr != nil {
			return cerr
		}
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)

	var n int64
	require.NoError(t, db.Model(&poolTestRow{}).Where("name = ?", "rollback-me").Count(&n).Error)
	require.Equal(t, int64(0), n, "回滚的事务不应留下数据")

	// journal_mode 未被切换（仍是默认的 delete，即改动前形态）。
	var mode string
	require.NoError(t, rawSQLDB(t, db).QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode))
	require.Equal(t, "delete", strings.ToLower(mode), "逃生阀路径不得改动持久化属性")
}

// TestSQLitePool_WALFailureDegradesToSingleConn 验证自动降级路径：WAL 不可用时必须退回
// 单连接，而不是带着「无 WAL + 多连接」这个**比原状更糟**的组合继续跑。
//
// 构造：先把库切成 rollback journal 并关闭，再把目录置为非可写，使新连接的 WAL 切换失败
// （PRAGMA journal_mode=WAL 报 "attempt to write a readonly database"）。
func TestSQLitePool_WALFailureDegradesToSingleConn(t *testing.T) {
	t.Setenv(envSQLiteMaxOpenConns, "")

	dir := t.TempDir()
	dsn := filepath.Join(dir, "readonly.db")

	seed, err := New(config.DatabaseConfig{Driver: "sqlite", DSN: dsn})
	require.NoError(t, err)
	require.NoError(t, seed.Exec("PRAGMA journal_mode=DELETE").Error)
	require.NoError(t, seed.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY)").Error)
	closeDB(t, seed)

	// 目录非可写：WAL 切换需要创建 -wal/-shm，必然失败。
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	db, err := New(config.DatabaseConfig{Driver: "sqlite", DSN: dsn})
	if err != nil {
		// 只读目录也可能让打开本身失败（取决于权限模型）；这同属「WAL 不可用」，
		// 真正要守的不变式是「绝不放行无 WAL 的多连接」。
		t.Skipf("只读目录下连打开都失败，无法构造 WAL 失败场景: %v", err)
	}
	t.Cleanup(func() { closeDB(t, db) })

	snap := mustSnapshot(t, db)
	if snap.WAL {
		t.Skip("该环境下 WAL 在非可写目录仍可启用，无法构造降级场景")
	}
	require.True(t, snap.Degraded, "WAL 不可用时必须标记降级")
	require.Equal(t, 1, snap.MaxOpenConns,
		"WAL 不可用时连接数必须收敛为 1（否则读者挡 COMMIT 的毒化扳机重新出现）")
	require.False(t, snap.WriteGateActive, "降级为单连接后不应启用写闸门")
	require.NotEmpty(t, snap.DegradeReason, "降级必须带原因，便于事后排障")
	require.Equal(t, 1, rawSQLDB(t, db).Stats().MaxOpenConnections)
	t.Logf("降级证据: reason=%q maxOpen=%d", snap.DegradeReason, snap.MaxOpenConns)

	// 降级路径仍可读（本场景目录非可写，写入必然失败，故只验证读路径通畅；
	// 「写仍串行可用」由 TestSQLitePool_EscapeHatchSingleConn 在可写库上覆盖）。
	var n int64
	require.NoError(t, db.Table("t").Count(&n).Error)
	require.Equal(t, int64(0), n)
}

// TestSQLitePool_MemoryDSNStaysSingleConn 守护：内存库不得放开连接数。
//
// 为什么是硬约束：`:memory:` 下**每条连接是各自独立的库**，放开连接数会让不同请求看到
// 不同的（空）数据库，静默产生「数据时有时无」的诡异现象；且内存库不支持 WAL
// （journal_mode 恒为 memory），多连接只带来锁竞争而无读并发收益。
func TestSQLitePool_MemoryDSNStaysSingleConn(t *testing.T) {
	t.Setenv(envSQLiteMaxOpenConns, "")
	for _, dsn := range []string{":memory:", "file::memory:?cache=shared"} {
		db, err := New(config.DatabaseConfig{Driver: "sqlite", DSN: dsn})
		require.NoError(t, err, "dsn=%s", dsn)
		snap, ok := TxGuardSnapshotOf(db)
		require.True(t, ok, "dsn=%s", dsn)
		require.True(t, snap.MemoryDSN, "dsn=%s 应被识别为内存库", dsn)
		require.Equal(t, 1, snap.MaxOpenConns, "dsn=%s 内存库必须单连接", dsn)
		require.False(t, snap.WriteGateActive, "dsn=%s 单连接下不该有写闸门", dsn)

		// 单连接下的读写仍正常，且能看到自己写入的数据。
		// 这一步是内存库单连接约束的**行为证据**：若放开连接数，:memory: 的后续请求
		// 会拿到另一条连接（即另一个空库），这里的 Count 就会查不到刚写入的行。
		require.NoError(t, db.AutoMigrate(&poolTestRow{}), "dsn=%s", dsn)
		require.NoError(t, db.Create(&poolTestRow{Name: "mem"}).Error, "dsn=%s", dsn)
		var n int64
		require.NoError(t, db.Model(&poolTestRow{}).Count(&n).Error)
		require.Equal(t, int64(1), n, "dsn=%s 内存库必须能看到自己写入的数据", dsn)
		closeDB(t, db)
	}
}

// TestSQLitePool_WriteGateNotLeakedOnContextCancel 验证写闸门不会泄漏。
//
// 为什么必须测：闸门获取在 gorm 侧（取连接之前，否则排队中的写者会各占一条连接把读饿死），
// 而释放必须落到**驱动层事务**——database/sql 在请求 context 被取消时会自行回滚并回收连接
// （(*Tx).awaitDone），那条路径上 gorm 侧的包装器不会被调用。若释放只挂在 gorm 侧，
// 一个被取消的写事务就会永久占住闸门 → 之后所有写都挂死，比原问题更糟。
//
// 判定强度：池被显式收敛到「2 条连接 + 闸门开启」，且取消后**连做多轮写**。闸门若泄漏，
// 第一轮就会永久阻塞，用例以 10s 超时失败；反之每轮都能完成。
func TestSQLitePool_WriteGateNotLeakedOnContextCancel(t *testing.T) {
	db := openTestPool(t, "gate_leak", "")
	require.NoError(t, db.AutoMigrate(&poolTestRow{}))
	require.NoError(t, db.Create(&poolTestRow{Name: "gate"}).Error)

	// 提高连接数让闸门处于激活态（默认推导可能因 CPU 限制为 2，两者都已 >1）。
	require.True(t, mustSnapshot(t, db).WriteGateActive, "本用例要求写闸门激活")
	require.NoError(t, db.Create(&poolTestRow{Name: "gate2"}).Error)

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&poolTestRow{}).Where("name = ?", "gate").
				Update("sort", 1).Error; err != nil {
				return err
			}
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("写事务未在 10s 内开始")
	}
	cancel()
	wg.Wait()

	// 闸门必须已释放：连做多轮写事务都要能完成。
	for round := 0; round < 3; round++ {
		done := make(chan error, 1)
		go func(round int) {
			done <- db.Transaction(func(tx *gorm.DB) error {
				return tx.Model(&poolTestRow{}).Where("name = ?", "gate2").
					Update("sort", round+3).Error
			})
		}(round)
		select {
		case err := <-done:
			require.NoErrorf(t, err, "第 %d 轮：取消的写事务不得泄漏写闸门", round)
		case <-time.After(10 * time.Second):
			t.Fatalf("写闸门被泄漏：context 取消后第 %d 轮写事务仍然挂死", round)
		}
	}
}

// ---------------------------------------------------------------------------
// 连接池参数推导与驱动的桩
// ---------------------------------------------------------------------------

// TestResolveSQLiteMaxOpenConns 固化连接数推导规则，防止被无声改动。
func TestResolveSQLiteMaxOpenConns(t *testing.T) {
	// 默认：目标「只读查询不被一个长写任务堵死」⇒ 至少 2，且不超过 CPU 并行度。
	got := resolveSQLiteMaxOpenConns(0)
	require.GreaterOrEqual(t, got, minSQLiteMaxOpenConns, "默认推导不得低于 2（否则放弃读并发）")
	require.LessOrEqual(t, got, defaultSQLiteMaxOpenConns)

	// 显式覆盖生效，但有防呆上限。
	require.Equal(t, 7, resolveSQLiteMaxOpenConns(7))
	require.Equal(t, maxSQLiteMaxOpenConns, resolveSQLiteMaxOpenConns(maxSQLiteMaxOpenConns+100))

	// 环境变量解析：非法值忽略、1 是逃生阀语义。
	t.Setenv(envSQLiteMaxOpenConns, "")
	require.Equal(t, 0, sqliteMaxOpenConnsOverride())
	t.Setenv(envSQLiteMaxOpenConns, "1")
	require.Equal(t, 1, sqliteMaxOpenConnsOverride())
	t.Setenv(envSQLiteMaxOpenConns, "abc")
	require.Equal(t, 0, sqliteMaxOpenConnsOverride(), "非法值应被忽略并回落默认")
	t.Setenv(envSQLiteMaxOpenConns, "0")
	require.Equal(t, 0, sqliteMaxOpenConnsOverride(), "0 非法（<1）应被忽略")
}

// TestIsMemorySQLiteDSN 守护内存库识别：误判会让 :memory: 放开连接数（各连接各库）。
func TestIsMemorySQLiteDSN(t *testing.T) {
	require.True(t, isMemorySQLiteDSN(":memory:"))
	require.True(t, isMemorySQLiteDSN("file::memory:?cache=shared"))
	require.True(t, isMemorySQLiteDSN("file:x?mode=memory&cache=shared"))
	require.False(t, isMemorySQLiteDSN("data/jianmanager.db"))
	require.False(t, isMemorySQLiteDSN("file:data/jianmanager.db?_pragma=busy_timeout(5000)"))
}

// stubConn 是最小的 driver.Conn 桩，仅用于驱动层毒化状态的单元验证。
type stubConn struct{}

func (s *stubConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (s *stubConn) Close() error                        { return nil }
func (s *stubConn) Begin() (driver.Tx, error)           { return &stubTx{}, nil }

type stubTx struct{}

func (s *stubTx) Commit() error   { return nil }
func (s *stubTx) Rollback() error { return nil }

// failingTx 是提交与回滚都失败的事务，用于覆盖 guardTx 的最坏路径。
type failingTx struct{}

func (f *failingTx) Commit() error   { return fmt.Errorf("commit failed: database is locked") }
func (f *failingTx) Rollback() error { return fmt.Errorf("rollback failed") }
