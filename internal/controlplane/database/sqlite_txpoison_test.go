package database

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestSQLiteCommitBusyPoisonsConnection 固化 FR-318 根因机制（驱动级，确定性复现）：
//
// glebarez/go-sqlite 的 tx.Commit() 是裸 `exec("commit")`。SQLite 语义下 COMMIT 因
// SQLITE_BUSY 失败时事务**保持打开**（区别于其他错误的自动回滚）；而该驱动既不实现
// driver.Validator 也不实现 driver.SessionResetter，且 BUSY 错误非 driver.ErrBadConn，
// 于是 database/sql 把带着打开事务的连接原样放回连接池、且永不驱逐。此后每次抽到
// 该连接执行 BEGIN 都报 "SQL logic error: cannot start a transaction within a
// transaction (1)"——与线上「实例 stop 转换反复报错、删 bot INTERNAL_ERROR、重启 CP
// 即愈」完全一致（重启 = 清空连接池）。
//
// 触发前提是同库多连接间锁竞争（读事务 SHARED 锁挡住写事务 COMMIT 的 EXCLUSIVE），
// OOM/swap 风暴只是把持锁窗口放大到必然撞上。修复见 database.New：SQLite 收敛单连接。
func TestSQLiteCommitBusyPoisonsConnection(t *testing.T) {
	// busy_timeout 调小加速失败（生产默认 5000ms，慢 IO 下同样会耗尽）。
	dsn := filepath.Join(t.TempDir(), "poison.db") + "?_pragma=busy_timeout(50)"
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(2)

	ctx := context.Background()
	_, err = db.ExecContext(ctx, "CREATE TABLE t (id INTEGER PRIMARY KEY, v INTEGER)")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "INSERT INTO t (id, v) VALUES (1, 0)")
	require.NoError(t, err)

	// 钉住两条物理连接，确定性构造「读者挡提交」。
	reader, err := db.Conn(ctx)
	require.NoError(t, err)
	defer reader.Close()
	writer, err := db.Conn(ctx)
	require.NoError(t, err)
	defer writer.Close()

	// 读事务执行过 SELECT 后持 SHARED 锁直到事务结束（rollback journal 模式）。
	rtx, err := reader.BeginTx(ctx, nil)
	require.NoError(t, err)
	var v int
	require.NoError(t, rtx.QueryRowContext(ctx, "SELECT v FROM t WHERE id = 1").Scan(&v))

	// 写事务：UPDATE 拿 RESERVED 锁可与 SHARED 共存，COMMIT 需要 EXCLUSIVE 被读者挡住，
	// busy_timeout 耗尽后返回 SQLITE_BUSY——此时 SQLite 事务仍然打开。
	wtx, err := writer.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = wtx.ExecContext(ctx, "UPDATE t SET v = 1 WHERE id = 1")
	require.NoError(t, err)
	commitErr := wtx.Commit()
	if commitErr == nil {
		t.Skip("驱动 COMMIT 未再因读锁 BUSY 失败（驱动行为已变化），毒化前提不复存在，可重评估 FR-318 防线")
	}
	require.Contains(t, commitErr.Error(), "locked",
		"预期 COMMIT 因 SQLITE_BUSY 失败，实际: %v", commitErr)

	require.NoError(t, rtx.Rollback())

	// 写连接已被毒化：同一物理连接上再开事务即复现线上报错。
	_, err = writer.BeginTx(ctx, nil)
	require.Error(t, err, "毒化连接上 BEGIN 应失败")
	require.Contains(t, err.Error(), "cannot start a transaction within a transaction")
}

// TestNewSQLiteStressNoNestedTransactionError FR-318 压力回归门：并发状态转换写 +
// 事务内建/删 Bot + 故意回滚 + 慢读者（长持读事务，模拟 OOM/swap 慢 IO 把持锁窗口放大）
// 混跑，断言全程不出现 "cannot start a transaction within a transaction" 与
// "database is locked"。
//
// 修复前（连接池不设限）：慢读者的 SHARED 锁让并发写事务 COMMIT BUSY → 连接带打开
// 事务回池 → 后续随机请求抽到毒连接报 nested transaction 错，本压力可稳定复现；
// 修复后（database.New 对 SQLite 收敛单连接）：自身连接间锁竞争不复存在，应恒绿。
//
// 运行时长：约 5-10s（慢读者持锁 60ms×串行化；期间 GORM 的 SLOW SQL 日志是排队等待
// 计入耗时所致，属预期观测，非异常）。
func TestNewSQLiteStressNoNestedTransactionError(t *testing.T) {
	// busy_timeout 调小：修复前让 COMMIT BUSY 更快暴露；修复后单连接下无同库锁竞争，该值无关紧要。
	dsn := filepath.Join(t.TempDir(), "stress.db") + "?_pragma=busy_timeout(50)"
	db, err := New(config.DatabaseConfig{Driver: "sqlite", DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() {
		// Windows 下不关池 TempDir 清理会报文件占用。
		if sqlDB, derr := db.DB(); derr == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.Bot{}))

	inst := &model.Instance{
		NodeID:       1,
		Name:         "stress",
		Type:         model.InstanceTypeGeneric,
		ProcessType:  model.ProcessTypeDaemon,
		StartCommand: "x",
		Status:       model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(inst).Error)

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
		readerHold     = 60 * time.Millisecond
	)
	writersDone := make(chan struct{})
	var writerWG sync.WaitGroup

	// 慢读者：持读事务 60ms（> busy_timeout）再回滚，循环到写者收工。
	// 模拟慢 IO 下长时间占着 SHARED 锁的读路径（列表页大查询、swap 抖动放大等）。
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
				tx := db.Begin()
				if tx.Error != nil {
					record("reader begin", tx.Error)
					continue
				}
				var got model.Instance
				record("reader select", tx.First(&got, inst.ID).Error)
				time.Sleep(readerHold)
				record("reader rollback", tx.Rollback().Error)
			}
		}()
	}

	// 写者：模拟 transition 的状态回写（GORM 默认事务包 BEGIN/UPDATE/COMMIT）、
	// 事务内建/删 Bot（删 bot 链路）、以及故意回滚的事务（回滚路径不得留残留事务）。
	statuses := []model.InstanceStatus{
		model.InstanceStatusStopping, model.InstanceStatusStopped,
		model.InstanceStatusStarting, model.InstanceStatusRunning,
	}
	sentinel := fmt.Errorf("intentional rollback")
	for w := 0; w < writers; w++ {
		writerWG.Add(1)
		go func(w int) {
			defer writerWG.Done()
			for i := 0; i < itersPerWriter; i++ {
				// ① transition 式条件状态回写。
				record("transition update", db.Model(&model.Instance{}).
					Where("id = ?", inst.ID).
					Updates(map[string]any{
						"status":        statuses[(w+i)%len(statuses)],
						"status_reason": "",
					}).Error)

				// ② 事务内建 Bot → 删 Bot（对应删 bot 链路的显式事务）。
				record("bot create+delete tx", db.Transaction(func(tx *gorm.DB) error {
					bot := &model.Bot{InstanceID: inst.ID, Name: fmt.Sprintf("b-%d-%d", w, i)}
					if err := tx.Create(bot).Error; err != nil {
						return err
					}
					return tx.Unscoped().Delete(bot).Error
				}))

				// ③ 故意失败回滚的事务：回滚后连接必须干净可复用。
				if err := db.Transaction(func(tx *gorm.DB) error {
					bot := &model.Bot{InstanceID: inst.ID, Name: "rollback-me"}
					if err := tx.Create(bot).Error; err != nil {
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
	if len(failures) > 0 {
		max := len(failures)
		if max > 10 {
			max = 10
		}
		t.Fatalf("压力混跑出现 %d 个错误（前 %d 个）:\n%s",
			len(failures), max, joinLines(failures[:max]))
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}
