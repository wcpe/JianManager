package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// 本文件是防护的**机制化替代**。原先靠 `SetMaxOpenConns(1)` 消灭「池内同库锁竞争」
// 这一毒化前提来回避问题；连接数一旦放开该前提即失效，所以必须换成「检测 + 驱逐」：
//
//	① 毒化检测与驱逐（guardConn / guardTx）：把「连接带未提交事务」这一状态显式追踪，
//	   并通过 driver.Validator.IsValid 让 database/sql 在**连接回池前**主动丢弃它；
//	② WAL（enableSQLiteWAL）：让读不再阻塞写、写不再阻塞读，这是放开连接数的前提
// 	   （rollback journal 模式下读者持 SHARED 锁挡写者 COMMIT，正是的扳机）；
//	③ 写闸门（sqliteWriteGate / gatedConnPool）：把 SQLite「同一时刻只有一个写者」的
//	   语义在应用层显式化并串行排队，消除写-写 SQLITE_BUSY（也让调用方不必依赖
//	   busy_timeout 是否够长）。
//
// 三者正交：② 决定「读能不能并行」，③ 决定「写会不会撞锁」，① 决定「万一还是撞了会怎样」。
// ① 单独存在也能覆盖故障面，②③ 负责让 ① 极少被触发。

const (
	// defaultSQLiteMaxOpenConns 是 SQLite 连接池默认上限，推导依据见 resolveSQLiteMaxOpenConns。
	defaultSQLiteMaxOpenConns = 4
	// minSQLiteMaxOpenConns 是自动推导的下限：1 等于放弃读并发（应改用逃生阀显式表达）。
	minSQLiteMaxOpenConns = 2
	// maxSQLiteMaxOpenConns 是显式覆盖值的防呆上限：连接数远超可用并行度只会增加
	// SQLite 的 WAL 索引内存与上下文切换开销，收益为负。
	maxSQLiteMaxOpenConns = 64
)

// envSQLiteMaxOpenConns 是连接数逃生阀：设为 1 即完全退回改动前的行为
// （单连接 + 不切 WAL + 不做写闸门），用于个别环境下新机制不可用时的一键回退。
// 无需改配置文件、无需动数据库文件，重启 CP 即生效。
const envSQLiteMaxOpenConns = "JIANMANAGER_SQLITE_MAX_OPEN_CONNS"

// sqliteTxGuard 汇总毒化检测/驱逐与写闸门的可观测计数。
//
// 这些计数不能只停留在内部：毒化驱逐是异常事件，必须能在事后排障时回答
// 「有没有发生过、发生了几次、是不是闸门一直在排队」，而不是静默自愈。
type sqliteTxGuard struct {
	// dirtyCommits 是 COMMIT 返回错误（事务可能仍打开）的次数。
	dirtyCommits atomic.Int64
	// evictedConns 是因「连接带未提交事务」被 database/sql 丢弃的连接数。
	evictedConns atomic.Int64
	// dirtyRollbacks 是 ROLLBACK 也失败的次数（连接必然被驱逐，属最坏路径）。
	dirtyRollbacks atomic.Int64
	// gateWaits / gateWaitNanos 是写闸门排队次数与累计排队时长。
	gateWaits     atomic.Int64
	gateWaitNanos atomic.Int64

	// lastEvictLogNanos 是最近一条「连接毒化驱逐」Error 告警的纳秒时间戳（零点 = 从未打过）。
	// 频控用（修 CP-9）：驱逐事件在短时压力下可能被 database/sql 反复触发，
	// Error 级告警若逐次打印会刷屏冲掉其他日志；计数仍逐次累加，告警按窗口合并。
	lastEvictLogNanos atomic.Int64

	plan sqlitePoolPlan
}

// TxGuardSnapshot 是 SQLite 事务守卫的一次性快照，供诊断与回归断言使用。
type TxGuardSnapshot struct {
	DirtyCommits    int64 // COMMIT 失败（事务悬挂）次数
	EvictedConns    int64 // 因带未提交事务被驱逐的连接数
	DirtyRollbacks  int64 // ROLLBACK 亦失败次数
	WriteGateWaits  int64 // 写闸门排队次数
	WriteGateDelay  time.Duration
	MaxOpenConns    int
	WAL             bool
	Degraded        bool
	DegradeReason   string
	MemoryDSN       bool
	WriteGateActive bool
}

// TxGuardSnapshotOf 取出某连接上的事务守卫快照；非本包构建的连接（如测试里直接
// gorm.Open 的内存库）返回 false。
func TxGuardSnapshotOf(db *gorm.DB) (TxGuardSnapshot, bool) {
	if db == nil {
		return TxGuardSnapshot{}, false
	}
	pool, ok := db.ConnPool.(*gatedConnPool)
	if !ok || pool == nil || pool.guard == nil {
		return TxGuardSnapshot{}, false
	}
	g := pool.guard
	return TxGuardSnapshot{
		DirtyCommits:    g.dirtyCommits.Load(),
		EvictedConns:    g.evictedConns.Load(),
		DirtyRollbacks:  g.dirtyRollbacks.Load(),
		WriteGateWaits:  g.gateWaits.Load(),
		WriteGateDelay:  time.Duration(g.gateWaitNanos.Load()),
		MaxOpenConns:    g.plan.MaxOpenConns,
		WAL:             g.plan.WAL,
		Degraded:        g.plan.Degraded,
		DegradeReason:   g.plan.Reason,
		MemoryDSN:       g.plan.Memory,
		WriteGateActive: pool.gate != nil && g.plan.MaxOpenConns > 1,
	}, true
}

// sqlitePoolPlan 记录 SQLite 连接池的启动决策，供启动日志与回归测试断言。
type sqlitePoolPlan struct {
	MaxOpenConns int
	WAL          bool   // 是否成功启用 WAL
	Degraded     bool   // 是否因 WAL 不可用而降级为单连接
	Reason       string // 降级/收敛原因（空 = 无特殊原因）
	Memory       bool   // 是否内存库
}

// ---------------------------------------------------------------------------
// 毒化检测与驱逐
// ---------------------------------------------------------------------------

// guardConnector 让每条物理连接都经过 guardConn 包装。
//
// 必须落在 driver.Conn 这一层（而不是在 sql.Open 之后包装 *sql.DB）：driver.Validator /
// driver.SessionResetter 都是 database/sql 对 driver.Conn 做的**接口断言**，只有包装进
// 连接生产环节才可能被看到——里驱动「不实现 Validator」正是同一层的事。
type guardConnector struct {
	base  driver.Driver
	dsn   string
	guard *sqliteTxGuard
}

func (c *guardConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return newGuardConn(conn, c.guard), nil
}

func (c *guardConnector) Driver() driver.Driver { return c.base }

// guardConn 包装驱动原生连接，追踪「本连接上是否有已 BEGIN 未结束的事务」。
//
// 注意可选接口必须**显式转发**：database/sql 会在连接上断言 driver.ExecerContext /
// driver.QueryerContext / driver.ConnPrepareContext / driver.Pinger 等，若包装器只嵌入
// driver.Conn 接口，这些断言会全部落空并退化为 Prepare 路径（性能与语义都会变）。
type guardConn struct {
	base  driver.Conn
	guard *sqliteTxGuard

	// 底层能力缓存。若驱动某天不再实现某接口，对应字段为 nil，转发方法返回
	// driver.ErrSkip 让 database/sql 退化到 Prepare 路径，而不是空指针崩溃。
	execerCtx   driver.ExecerContext
	queryerCtx  driver.QueryerContext
	preparerCtx driver.ConnPrepareContext
	beginnerCtx driver.ConnBeginTx
	pinger      driver.Pinger

	// txOpen 为真表示连接上存在未提交事务（毒化状态）。
	// 只由持有该连接的 goroutine 读写，用 atomic 是为了兜住 Close 与查询的并发。
	txOpen atomic.Bool
}

func newGuardConn(base driver.Conn, guard *sqliteTxGuard) *guardConn {
	c := &guardConn{base: base, guard: guard}
	c.execerCtx, _ = base.(driver.ExecerContext)
	c.queryerCtx, _ = base.(driver.QueryerContext)
	c.preparerCtx, _ = base.(driver.ConnPrepareContext)
	c.beginnerCtx, _ = base.(driver.ConnBeginTx)
	c.pinger, _ = base.(driver.Pinger)
	return c
}

func (c *guardConn) Prepare(query string) (driver.Stmt, error) { return c.base.Prepare(query) }

// Close 转发底层关闭。database/sql 驱逐连接时调用它；SQLite 的 sqlite3_close_v2 在最后一条
// 语句关闭后会**回滚悬挂事务并释放锁**，所以驱逐不只是「不再复用」，还顺带解开了写锁。
func (c *guardConn) Close() error { return c.base.Close() }

func (c *guardConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if c.preparerCtx == nil {
		return c.base.Prepare(query)
	}
	return c.preparerCtx.PrepareContext(ctx, query)
}

func (c *guardConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.execerCtx == nil {
		return nil, driver.ErrSkip
	}
	return c.execerCtx.ExecContext(ctx, query, args)
}

func (c *guardConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.queryerCtx == nil {
		return nil, driver.ErrSkip
	}
	return c.queryerCtx.QueryContext(ctx, query, args)
}

func (c *guardConn) Ping(ctx context.Context) error {
	if c.pinger == nil {
		return nil
	}
	return c.pinger.Ping(ctx)
}

// Begin 是 driver.Conn 的必需方法；database/sql 在连接实现 ConnBeginTx 时走 BeginTx。
func (c *guardConn) Begin() (driver.Tx, error) {
	tx, err := c.base.Begin()
	return c.trackTx(tx, err, nil)
}

func (c *guardConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	// 写闸门持有权随 context 落到驱动层：database/sql 在请求 context 被取消时会自行回滚
	// 事务并回收连接（(*Tx).awaitDone），那条路径只有驱动层事务会被通知到。
	// 不在这里接住持有权，写闸门就会在「写事务被 ctx 打断」时永久泄漏。
	hold, _ := ctx.Value(writeHoldCtxKey{}).(*writeHold)

	if c.beginnerCtx == nil {
		tx, err := c.base.Begin()
		return c.trackTx(tx, err, hold)
	}
	tx, err := c.beginnerCtx.BeginTx(ctx, opts)
	return c.trackTx(tx, err, hold)
}

// trackTx 在事务开启成功时标记连接为「事务中」。
func (c *guardConn) trackTx(tx driver.Tx, err error, hold *writeHold) (driver.Tx, error) {
	if err != nil {
		// BEGIN 失败却报「事务内不能开事务」，说明连接上**确实**残留着打开的事务
		// （例如上一轮 COMMIT 因 BUSY 悬挂、而当时无人驱逐）。此时必须把连接标记为污染，
		// 否则它会一直被复用并持续撞同一堵墙——这正是线上「重启 CP 即愈」的形态。
		if isNestedTransactionError(err) {
			c.txOpen.Store(true)
		}
		hold.Release()
		return nil, err
	}
	c.txOpen.Store(true)
	return &guardTx{Tx: tx, conn: c, hold: hold}, nil
}

// IsValid 实现 driver.Validator，是防护的核心。
//
// database/sql 的 (*DB).putConn 在连接回到池之前**无条件**调用 validateConnection
// （只要错误不是 driver.ErrBadConn），后者断言 driver.Validator 并取其结果：返回 false 即把
// 错误改写成 driver.ErrBadConn，进而 dc.Close() —— 连接既不进空闲队列，也不会被直接交给
// 等待中的请求。因此「带未提交事务的连接不再被复用」由标准库自身保证，不依赖任何调用方
// 记得清理。
//
// 为什么它比「靠单连接回避」更安全：单连接的防护成立前提是「池内不存在同库锁竞争」，
// 一旦有第二方连接（或未来放开连接数）前提即失效；这里是直接对毒化状态本身做判定，
// 与竞争是否存在无关。
func (c *guardConn) IsValid() bool {
	if !c.txOpen.Load() {
		return true
	}
	c.guard.logEvicted(stagePoolPut)
	return false
}

// ResetSession 实现 driver.SessionResetter，是毒化检测的**第二道**闸口（取自池时复查）。
//
// database/sql 在把空闲连接交给新请求前会调用它（(*DB).conn → driverConn.resetSession）。
// 返回 driver.ErrBadConn 会让标准库丢弃该连接并换一条（调用方在 db.retry 里重试），
// 因此即便出现「IsValid 没拦住的残留污染」，也不会被交给业务请求。
//
// 与 IsValid 的分工：IsValid 管**回池时**（主要闸口），ResetSession 管**取出时**（兜底）。
// 两处都判同一个原子标志，代价各是一次原子读。
func (c *guardConn) ResetSession(context.Context) error {
	if !c.txOpen.Load() {
		return nil
	}
	c.guard.logEvicted(stageSessionReset)
	return driver.ErrBadConn
}

// 驱逐告警的入口标识（日志字段用）。
const (
	stagePoolPut      = "pool_put"
	stageSessionReset = "session_reset"
)

// evictLogMinInterval 是毒化连接驱逐告警的频控窗口（修 CP-9）：
// ResetSession 返回 ErrBadConn 后 database/sql 的 (*DB).retry 会重试，重试失败又会走一次
// putConn ⇒ 同一条脏连接可能在短时内反复走进 IsValid/ResetSession，而无条件的 slog.Error
// 会把日志刷到淹没其他条目。计数（evictedConns）仍逐次累加——它是累计真源，必须精确；
// 这里只把 Error 级告警按窗口合并，窗口内的同类事件降级为 Debug，排障时仍可追溯。
const evictLogMinInterval = 10 * time.Second

// logEvicted 记录一条连接毒化驱逐：累加计数 + 打印（可能被频控合并的）Error 告警。
// stage 区分入口（回池 / 取出时复查）。
func (g *sqliteTxGuard) logEvicted(stage string) {
	total := g.evictedConns.Add(1)
	msg := "SQLite 连接带未提交事务回池，已驱逐（不再复用）"
	if stage == stageSessionReset {
		msg = "SQLite 复用前发现连接仍处于事务中，丢弃换新连接"
	}
	if !g.logEvictionRateLimited(msg,
		"stage", stage,
		"evicted_total", total,
		"dirty_commit_total", g.dirtyCommits.Load(),
		"dirty_rollback_total", g.dirtyRollbacks.Load()) {
		// 频控窗口内：降级 Debug 留痕。不丢失「发生过」这一事实（计数已在），
		// 也不让同一事件在压力下一次又一次占据 Error 级。
		slog.Debug("SQLite 连接毒化驱逐（频控期内省略 Error 级告警）",
			"stage", stage, "evicted_total", total)
	}
}

// logEvictionRateLimited 按时间窗频控打印告警：首次（或距上次不足一窗后再次到达）必打，
// 其余跳过。返回本次是否真的打印。多 goroutine 并发时只有 CAS 胜出者打印，
// 窗口边界少打一条无伤大雅（计数不受影响）。
func (g *sqliteTxGuard) logEvictionRateLimited(msg string, kv ...any) bool {
	now := time.Now().UnixNano()
	last := g.lastEvictLogNanos.Load()
	if last != 0 && now-last < int64(evictLogMinInterval) {
		return false
	}
	if !g.lastEvictLogNanos.CompareAndSwap(last, now) {
		return false
	}
	slog.Error(msg, kv...)
	return true
}

// guardTx 包装驱动事务，把真实的结束与否回报给连接。
type guardTx struct {
	driver.Tx
	conn *guardConn
	hold *writeHold
}

func (t *guardTx) Commit() error {
	defer t.hold.Release()
	err := t.Tx.Commit()
	if err == nil {
		t.conn.txOpen.Store(false)
		return nil
	}
	// COMMIT 失败：SQLite 只在部分错误下自动回滚，**SQLITE_BUSY 时事务保持打开**
	// （见 sqlite_txpoison_test.go 的驱动级固化）。这里不去分辨错误码，一律保持
	// txOpen=true 让 IsValid 驱逐该连接——代价是偶尔多建一条连接（当 COMMIT 因
	// 非 BUSY 原因失败、SQLite 已自动回滚时），换来的是不会漏掉真正的悬挂事务。
	t.conn.guard.dirtyCommits.Add(1)
	slog.Warn("SQLite COMMIT 失败，连接将不再复用",
		"error", err,
		"dirty_commit_total", t.conn.guard.dirtyCommits.Load())
	return err
}

func (t *guardTx) Rollback() error {
	defer t.hold.Release()
	err := t.Tx.Rollback()
	if err == nil {
		t.conn.txOpen.Store(false)
		return nil
	}
	// ROLLBACK 失败（罕见）：事务状态未知，连接必须报废而不是复用。
	t.conn.guard.dirtyRollbacks.Add(1)
	slog.Warn("SQLite ROLLBACK 失败，连接将不再复用",
		"error", err,
		"dirty_rollback_total", t.conn.guard.dirtyRollbacks.Load())
	return err
}

// sqliteErrorCoder 匹配驱动错误上的 SQLite 结果码访问器（glebarez/go-sqlite 的 *Error
// 实现了 Code() int）。用匿名接口断言而不是 import 驱动包，避免把本包与某个驱动版本耦合。
type sqliteErrorCoder interface{ Code() int }

// sqliteErrError 是 SQLITE_ERROR 的结果码。
const sqliteErrError = 1

// isNestedTransactionError 判定「事务内不能开事务」这一毒化特征错误。
func isNestedTransactionError(err error) bool {
	if err == nil {
		return false
	}
	var coder sqliteErrorCoder
	if errors.As(err, &coder) && coder.Code() != sqliteErrError {
		return false
	}
	return strings.Contains(err.Error(), "cannot start a transaction within a transaction")
}

// ---------------------------------------------------------------------------
// 写闸门：把 SQLite 的单写者语义显式化
// ---------------------------------------------------------------------------

// sqliteWriteGate 串行化写事务。
//
// 为什么需要：SQLite 任意时刻只允许一个写事务，多连接并发写会以 SQLITE_BUSY 收场——
// 在 rollback journal 模式下更糟（读者能挡住写者 COMMIT，直接把连接毒化）。原先单连接
// 顺带覆盖了这一点，放开连接数后必须显式补回。
//
// 为什么这不会比单连接更差：单连接本身就是一把覆盖「读 + 写」的全局不可重入锁——若代码里
// 存在「事务内再取连接」的调用，单连接下早已死锁，因此本闸门（只锁写）不可能引入新的死锁面。
//
// 为什么等待者不占连接：闸门在 gorm 的 ConnPool.BeginTx 里获取，早于 (*sql.DB).BeginTx
// 去池里取连接，所以排队中的写请求不会把连接池占满、把读请求挤到没连接可用。
type sqliteWriteGate struct {
	token chan struct{}
}

func newSQLiteWriteGate() *sqliteWriteGate {
	return &sqliteWriteGate{token: make(chan struct{}, 1)}
}

// acquire 取得写权限，返回释放函数。ctx 取消时放弃等待（与单连接下等连接的语义一致）。
func (g *sqliteWriteGate) acquire(ctx context.Context, guard *sqliteTxGuard) (func(), error) {
	select {
	case g.token <- struct{}{}:
		return g.release(), nil
	default:
	}

	guard.gateWaits.Add(1)
	start := time.Now()
	select {
	case g.token <- struct{}{}:
		guard.gateWaitNanos.Add(int64(time.Since(start)))
		return g.release(), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *sqliteWriteGate) release() func() {
	var once sync.Once
	return func() { once.Do(func() { <-g.token }) }
}

// gatedConnPool 在 *sql.DB 之外补上写闸门。
//
// 它同时实现 gorm.ConnPoolBeginner（拿到所有 gorm 显式事务的入口）与 gorm.GetDBConnector
// （让 (*gorm.DB).DB() 仍能取回真实 *sql.DB，外部对连接池的读取/调参不被遮挡）。
type gatedConnPool struct {
	db    *sql.DB
	gate  *sqliteWriteGate
	guard *sqliteTxGuard
}

func (p *gatedConnPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return p.db.PrepareContext(ctx, query)
}

func (p *gatedConnPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return p.db.ExecContext(ctx, query, args...)
}

func (p *gatedConnPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return p.db.QueryContext(ctx, query, args...)
}

func (p *gatedConnPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return p.db.QueryRowContext(ctx, query, args...)
}

func (p *gatedConnPool) GetDBConn() (*sql.DB, error) { return p.db, nil }

// Ping 转发到真实连接池：gorm.Open 初始化后会对 ConnPool 做一次 Ping 探活，
// 缺了这次探活会被静默跳过（ConnPool 已被换成本包装器）。
func (p *gatedConnPool) Ping() error { return p.db.Ping() }

// writeHoldCtxKey 是把写闸门持有权从本层传递给驱动层的 context 键。
//
// 为什么要跨层传递：闸门必须在**取连接之前**获取（否则排队中的写者会各自占住一条连接，
// 把读请求饿死），但释放必须挂在**驱动层事务真正结束**的那一刻——因为 database/sql 在
// 请求 context 被取消时会自行回滚并回收连接（(*Tx).awaitDone），这条路径上 gorm 侧的
// 包装器不会被调用。只挂 gorm 侧会导致闸门永久泄漏（写全部挂死，比原问题更糟），
// 只挂驱动侧则会导致读饿死。context 值能穿透 (*DB).BeginTx → driver.ConnBeginTx，两处都挂、
// 由 sync.Once 保证幂等，即得「不饿死读 + 不泄漏闸门」。
type writeHoldCtxKey struct{}

// writeHold 是一次写闸门的持有权。release 幂等：gorm 包装器与驱动事务会各调一次，
// 谁先到谁真正释放，后者为空操作。
type writeHold struct {
	release func()
	once    sync.Once
}

func (h *writeHold) Release() {
	if h == nil {
		return
	}
	h.once.Do(h.release)
}

// BeginTx 是所有 gorm 显式事务（db.Begin / db.Transaction）的入口。
//
// 只读事务（opts.ReadOnly）不取闸门：WAL 下读事务不阻塞写，没有排队必要。gorm 的写操作
// 默认事务不传 TxOptions（opts 为 nil），因此 nil 一律按写事务处理——保守方向，最坏只是
// 让某个实为只读的事务被串行化，不会漏掉真正的写。
func (p *gatedConnPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	// 闸门未启用（单连接形态）时完全走原路径，不引入任何额外层。
	if p.gate == nil {
		return p.db.BeginTx(ctx, opts)
	}
	if opts != nil && opts.ReadOnly {
		return p.db.BeginTx(ctx, opts)
	}

	release, err := p.gate.acquire(ctx, p.guard)
	if err != nil {
		return nil, err
	}
	hold := &writeHold{release: release}
	// 把持有权交给驱动层事务，让它随事务结束（含 context 取消引发的自动回滚）释放。
	ctx = context.WithValue(ctx, writeHoldCtxKey{}, hold)

	tx, err := p.db.BeginTx(ctx, opts)
	if err != nil {
		hold.Release()
		return nil, err
	}
	return &gatedTx{Tx: tx, hold: hold}, nil
}

// gatedTx 在事务结束时交还写闸门（驱动层未释放时的兜底）。
type gatedTx struct {
	*sql.Tx
	hold *writeHold
}

func (t *gatedTx) Commit() error {
	defer t.hold.Release()
	return t.Tx.Commit()
}

func (t *gatedTx) Rollback() error {
	defer t.hold.Release()
	return t.Tx.Rollback()
}

// ---------------------------------------------------------------------------
// 连接池构建
// ---------------------------------------------------------------------------

// newGatedConnPool 用既成连接池组装 gorm 侧入口。
//
// 单连接形态下 gate 为 nil：逃生阀与降级路径必须回到**改动前的行为**，
// 连写闸门这一层额外的串行化都不应残留（单连接本就没有并发写可串行）。
func newGatedConnPool(db *sql.DB, guard *sqliteTxGuard) *gatedConnPool {
	p := &gatedConnPool{db: db, guard: guard}
	if guard != nil && guard.plan.MaxOpenConns > 1 {
		p.gate = newSQLiteWriteGate()
	}
	return p
}

// openSQLitePool 构建 SQLite 连接池：包装驱动连接、按需启用 WAL，并决定连接数上限。
func openSQLitePool(dsn string) (*sql.DB, *sqliteTxGuard, sqlitePoolPlan, error) {
	guard := &sqliteTxGuard{}
	plan := sqlitePoolPlan{}

	// 取出 glebarez/sqlite 注册的底层驱动，用自定义 connector 建立连接池：
	// 只有这样才能让每条物理连接都经过 guardConn（Validator 必须挂在 driver.Conn 上）。
	probe, err := sql.Open(sqlite.DriverName, dsn)
	if err != nil {
		return nil, nil, plan, fmt.Errorf("打开 SQLite 驱动失败: %w", err)
	}
	baseDriver := probe.Driver()
	// sql.Open 是惰性的，此时尚未建立任何连接，Close 只是释放占位池对象。
	_ = probe.Close()
	if baseDriver == nil {
		return nil, nil, plan, fmt.Errorf("SQLite 驱动 %q 未注册", sqlite.DriverName)
	}

	sqlDB := sql.OpenDB(&guardConnector{base: baseDriver, dsn: dsn, guard: guard})
	// WAL 切换需要独占访问：先收敛为单连接做完属性切换，再放开，避免与自己的连接抢锁。
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	plan.Memory = isMemorySQLiteDSN(dsn)
	override := sqliteMaxOpenConnsOverride()

	switch {
	case plan.Memory:
		// :memory: 下**每条连接是各自独立的库**，放开连接数会让不同请求看到不同的空库；
		// 共享缓存内存库（cache=shared）虽共享同一库，但内存库不支持 WAL（journal_mode 恒为
		// memory），多连接只会带来锁竞争而无读并发收益。两种情况都必须单连接。
		plan.MaxOpenConns = 1
		plan.Reason = "内存库（:memory: 每条连接是独立的库；内存库不支持 WAL）"
	case override == 1:
		// 逃生阀：完全退回改动前的行为（单连接、不切 WAL）。不切 WAL 是刻意的——逃生阀的
		// 意义是「回到已知形态」，任何残留变化都会让回退失去确定性。
		plan.MaxOpenConns = 1
		plan.Reason = envSQLiteMaxOpenConns + "=1 显式收敛为单连接"
	default:
		wal, reason := enableSQLiteWAL(sqlDB)
		plan.WAL = wal
		if !wal {
			// 自动降级：没有 WAL 时多连接会重新引入「读者挡 COMMIT」的毒化扳机，而读并发
			// 收益为零，放开连接数是净负收益。宁可退回单连接，也不能让生产陷入更糟状态。
			plan.MaxOpenConns = 1
			plan.Degraded = true
			plan.Reason = reason
		} else {
			plan.MaxOpenConns = resolveSQLiteMaxOpenConns(override)
		}
	}

	sqlDB.SetMaxOpenConns(plan.MaxOpenConns)
	// 空闲连接全部保活：SQLite 连接重建要重新打开文件并重放连接级 PRAGMA，不值得为省几条
	// 空闲连接反复付这个成本。
	sqlDB.SetMaxIdleConns(plan.MaxOpenConns)
	guard.plan = plan
	return sqlDB, guard, plan, nil
}

// enableSQLiteWAL 把日志模式切到 WAL 并校验确实生效。
//
// 为什么要 WAL：rollback journal 模式下读事务持 SHARED 锁、写者 COMMIT 需要 EXCLUSIVE，
// 读者因此能挡住写者提交——这正是毒化的扳机；同时写事务也会挡住所有读，长任务
// 一旦持锁，只读查询全部排队（本次「日志页慢」的直接成因）。WAL 下读不阻塞写、写不阻塞读，
// 是放开连接数的前提。
//
// 对既有数据文件的兼容性（**重要**）：journal_mode 是**持久化属性**，一旦切换，
// 数据库文件头即记录 wal 并生成 -wal / -shm 旁路文件。切换本身是原子且幂等的，重复调用
// 立即返回 wal；SQLite 打开该文件时会自动识别模式，因此**回滚二进制版本不需要改数据库**。
// 代价在运维面：-wal 中可能含有尚未 checkpoint 的数据，**直接复制 .db 文件会丢数据**，
// 备份必须连同 -wal（或先做 checkpoint）；WAL 依赖共享内存，不能放在 NFS/SMB 等网络
// 文件系统上。需要彻底退回 journal 模式时执行 PRAGMA journal_mode=DELETE。
//
// 返回值：是否成功启用 WAL，以及未启用时的原因（用于日志与降级决策）。
func enableSQLiteWAL(db *sql.DB) (bool, string) {
	var mode string
	if err := db.QueryRowContext(context.Background(), "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return false, fmt.Sprintf("WAL 切换失败: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		// 注意内存库与只读挂载等情形**不报错**，只是返回原模式（如 memory），
		// 所以必须校验返回值而不能只看 error。
		return false, fmt.Sprintf("WAL 未生效（journal_mode=%s）", mode)
	}
	return true, ""
}

// resolveSQLiteMaxOpenConns 推导连接数上限。
//
// 默认 4 的依据（不是随手取的数）：WAL 下**写仍然只有一个写者**（由写闸门显式串行化），
// 所以连接数的收益全部来自读并发。CP 的连接占用方只有三类：① 一个长任务（指标卷积等，
// 写事务，独占写闸门与一条连接）；② 写请求（等待写闸门，**不占连接**）；③ 读请求（列表页、
// 鉴权、统计，非事务裸查询）。于是 4 = 1（长任务）+ 3（读并发），正是本次目标「至少让只读
// 查询不被一个长写任务堵死」的最小可用集；再往上只增加 WAL 索引内存与调度开销。
//
// 同时不超过 GOMAXPROCS（超过可用并行度的连接只会在 SQLite 内部排队），但不低于 2——
// 取 1 等于放弃本次改造，那种诉求应该用逃生阀显式表达。
func resolveSQLiteMaxOpenConns(override int) int {
	if override > 0 {
		if override > maxSQLiteMaxOpenConns {
			return maxSQLiteMaxOpenConns
		}
		return override
	}
	n := defaultSQLiteMaxOpenConns
	if p := runtime.GOMAXPROCS(0); p > 0 && p < n {
		n = p
	}
	if n < minSQLiteMaxOpenConns {
		n = minSQLiteMaxOpenConns
	}
	return n
}

// sqliteMaxOpenConnsOverride 读取逃生阀环境变量；未设置或非法返回 0（表示按默认推导）。
func sqliteMaxOpenConnsOverride() int {
	raw, ok := os.LookupEnv(envSQLiteMaxOpenConns)
	if !ok {
		return 0
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		slog.Warn("忽略非法的 SQLite 连接数覆盖值（应为 ≥1 的整数）",
			"env", envSQLiteMaxOpenConns, "value", raw)
		return 0
	}
	return n
}

// isMemorySQLiteDSN 判定是否内存库（含 file::memory: 与 ?mode=memory 两种写法）。
// 这类库不能放开连接数：:memory: 下每条连接是独立的库。
func isMemorySQLiteDSN(dsn string) bool {
	if sqliteFilePath(dsn) == "" {
		return true
	}
	if i := strings.IndexByte(dsn, '?'); i >= 0 {
		if q, err := url.ParseQuery(dsn[i+1:]); err == nil {
			if strings.EqualFold(q.Get("mode"), "memory") {
				return true
			}
		}
	}
	return false
}

// 编译期约束：包装类型必须完整满足各自接口，否则下面这些断言直接编译失败。
var (
	_ driver.Conn               = (*guardConn)(nil)
	_ driver.Validator          = (*guardConn)(nil)
	_ driver.SessionResetter    = (*guardConn)(nil)
	_ driver.ConnBeginTx        = (*guardConn)(nil)
	_ driver.ConnPrepareContext = (*guardConn)(nil)
	_ driver.ExecerContext      = (*guardConn)(nil)
	_ driver.QueryerContext     = (*guardConn)(nil)
	_ driver.Pinger             = (*guardConn)(nil)
	_ driver.Tx                 = (*guardTx)(nil)
	_ driver.Connector          = (*guardConnector)(nil)

	_ gorm.ConnPool         = (*gatedConnPool)(nil)
	_ gorm.ConnPoolBeginner = (*gatedConnPool)(nil)
	_ gorm.GetDBConnector   = (*gatedConnPool)(nil)
	_ gorm.TxCommitter      = (*gatedTx)(nil)
)
