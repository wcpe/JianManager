package router

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func openAuthSetupLockDatabase(t *testing.T) (*gorm.DB, func() *gorm.DB) {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "auth.db")) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	open := func() *gorm.DB {
		t.Helper()
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
		require.NoError(t, err)
		pool, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = pool.Close() })
		var journalMode string
		require.NoError(t, db.Raw("PRAGMA journal_mode").Scan(&journalMode).Error)
		require.Equal(t, "wal", journalMode)
		var busyTimeout int
		require.NoError(t, db.Raw("PRAGMA busy_timeout").Scan(&busyTimeout).Error)
		require.Equal(t, 5000, busyTimeout)
		return db
	}
	db := open()
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.AuthSetupLock{}))
	return db, open
}

func newAuthSetupLockService(db *gorm.DB) *service.AuthService {
	svc := service.NewAuthService(db, testJWTConfig())
	svc.SetPasswordCostForTest(bcrypt.MinCost)
	return svc
}

func assertAuthSetupLockState(t *testing.T, db *gorm.DB, admins, locks int64) {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&model.User{}).Where("role = ?", model.RolePlatformAdmin).Count(&count).Error)
	assert.Equal(t, admins, count, "平台管理员数量")
	require.NoError(t, db.Model(&model.AuthSetupLock{}).Count(&count).Error)
	assert.Equal(t, locks, count, "一次性初始化锁数量")
	if locks == 1 {
		var lock model.AuthSetupLock
		require.NoError(t, db.First(&lock).Error)
		assert.Equal(t, "initial-admin", lock.Key)
		assert.False(t, lock.CreatedAt.IsZero())
	}
}

func assertAuthSetupLockTokens(t *testing.T, db *gorm.DB, username, password string, pair *service.TokenPair) {
	t.Helper()
	require.NotNil(t, pair)
	var user model.User
	require.NoError(t, db.Where("username = ?", username).First(&user).Error)
	assert.NotZero(t, user.ID)
	assert.NotEmpty(t, user.UUID)
	assert.Equal(t, model.RolePlatformAdmin, user.Role)
	assert.Equal(t, model.UserStatusActive, user.Status)
	assert.Zero(t, user.AuthVersion)
	assert.NotEqual(t, password, user.Password)
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)))
	assert.Error(t, bcrypt.CompareHashAndPassword([]byte(user.Password), []byte("incorrect-password")))
	assert.Equal(t, int(testJWTConfig().AccessTTL.Seconds()), pair.ExpiresIn)
	for purpose, encoded := range map[string]string{service.TokenTypeAccess: pair.AccessToken, service.TokenTypeRefresh: pair.RefreshToken} {
		claims := &service.Claims{}
		token, err := jwt.ParseWithClaims(encoded, claims, func(*jwt.Token) (interface{}, error) {
			return []byte(testJWTConfig().Secret), nil
		}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
		require.NoError(t, err)
		assert.True(t, token.Valid)
		assert.Equal(t, user.ID, claims.UserID)
		assert.Equal(t, purpose, claims.TokenType)
		assert.Equal(t, user.AuthVersion, claims.AuthVersion)
		if purpose == service.TokenTypeAccess {
			assert.Equal(t, user.Username, claims.Username)
			assert.Equal(t, user.Role, claims.Role)
		}
	}
}

func TestAuth_SetupAdminConcurrentReadUpgradeKeepsSingleAdmin(t *testing.T) {
	left, reopen := openAuthSetupLockDatabase(t)
	right := reopen()
	leftPool, err := left.DB()
	require.NoError(t, err)
	rightPool, err := right.DB()
	require.NoError(t, err)
	require.NotSame(t, leftPool, rightPool, "必须使用同文件的独立连接池")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type participant struct {
		readReady       chan struct{}
		lockReady       chan struct{}
		done            chan struct{}
		writeAttempted  bool
		readBeforeWrite bool
		count           int64
	}
	peers := [2]*participant{}
	for i := range peers {
		peers[i] = &participant{readReady: make(chan struct{}), lockReady: make(chan struct{}), done: make(chan struct{}), count: -1}
	}
	wait := func(tx *gorm.DB, ready <-chan struct{}) {
		select {
		case <-ready:
		case <-ctx.Done():
			tx.AddError(fmt.Errorf("初始化并发屏障超时: %w", ctx.Err()))
		}
	}
	for i, db := range []*gorm.DB{left, right} {
		own, other := peers[i], peers[1-i]
		require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:auth_setup_read_snapshot", func(tx *gorm.DB) {
			count, ok := tx.Statement.Dest.(*int64)
			if tx.Error != nil || tx.Statement.Table != "users" || !ok {
				return
			}
			own.count = *count
			if own.writeAttempted {
				return
			}
			// 只拦截写锁之前的真实查询；写锁优先版本不会等待不可能出现的第二次计数。
			own.readBeforeWrite = true
			close(own.readReady)
			wait(tx, other.readReady)
			if i == 1 {
				// 两事务都读到空库后，保留右侧读快照，等左侧真实提交才尝试升级写锁。
				wait(tx, other.done)
			}
		}))
		require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:auth_setup_lock_attempt", func(tx *gorm.DB) {
			if _, ok := tx.Statement.Dest.(*model.AuthSetupLock); !ok {
				return
			}
			own.writeAttempted = true
			close(own.lockReady)
			if !own.readBeforeWrite {
				// 写锁优先时仍让两个独立连接同时抵达写入口，不靠单连接或串行调用规避竞争。
				wait(tx, other.lockReady)
			}
		}))
	}

	type result struct {
		index int
		pair  *service.TokenPair
		err   error
	}
	results := make(chan result, 2)
	for i, db := range []*gorm.DB{left, right} {
		go func() {
			pair, err := newAuthSetupLockService(db.WithContext(ctx)).SetupAdmin(fmt.Sprintf("admin_%d", i), "password123")
			close(peers[i].done)
			results <- result{index: i, pair: pair, err: err}
		}()
	}
	var got [2]result
	for range got {
		select {
		case item := <-results:
			got[item.index] = item
		case <-ctx.Done():
			t.Fatal("并发初始化未在期限内完成: ", ctx.Err())
		}
	}
	for i, item := range got {
		var coded interface{ Code() int }
		code := 0
		if errors.As(item.err, &coded) {
			code = coded.Code()
		}
		t.Logf("连接%d: 写前计数=%t, 管理员计数=%d, SQLite错误码=%d, 结果=%v", i, peers[i].readBeforeWrite, peers[i].count, code, item.err)
	}
	assert.True(t, (got[0].err == nil && got[1].err == service.ErrAdminAlreadyExists) ||
		(got[1].err == nil && got[0].err == service.ErrAdminAlreadyExists), "初始化只能有一个成功且另一个明确已存在: %v, %v", got[0].err, got[1].err)

	// 关闭两端再重开，验证真实提交、唯一锁和凭据，而不是连接内的未提交可见性。
	require.NoError(t, leftPool.Close())
	require.NoError(t, rightPool.Close())
	db := reopen()
	assertAuthSetupLockState(t, db, 1, 1)
	var users int64
	require.NoError(t, db.Model(&model.User{}).Count(&users).Error)
	assert.Equal(t, int64(1), users)
	for i, item := range got {
		if item.err == nil {
			assertAuthSetupLockTokens(t, db, fmt.Sprintf("admin_%d", i), "password123", item.pair)
		} else {
			assert.Nil(t, item.pair)
		}
	}
	pair, err := newAuthSetupLockService(db).SetupAdmin("after_reopen", "password123")
	assert.ErrorIs(t, err, service.ErrAdminAlreadyExists)
	assert.Nil(t, pair)
	assertAuthSetupLockState(t, db, 1, 1)
}

func TestAuth_SetupAdminLockTransactionBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		prepare     func(*testing.T, *gorm.DB)
		username    string
		password    string
		wantAlready bool
		wantError   string
		admins      int64
		locks       int64
		repair      func(*testing.T, *gorm.DB)
	}{
		{name: "已有管理员但没有锁行", prepare: func(t *testing.T, db *gorm.DB) {
			require.NoError(t, db.Create(&model.User{Username: "existing_admin", Password: "existing-hash", Role: model.RolePlatformAdmin}).Error)
		}, wantAlready: true, admins: 1},
		{name: "已有锁行仍然拒绝初始化", prepare: func(t *testing.T, db *gorm.DB) {
			require.NoError(t, db.Create(&model.AuthSetupLock{Key: "initial-admin"}).Error)
		}, wantAlready: true, locks: 1},
		{name: "用户名冲突回滚锁并可重试", prepare: func(t *testing.T, db *gorm.DB) {
			require.NoError(t, db.Create(&model.User{Username: "occupied", Password: "existing-hash", Role: model.RoleMember}).Error)
		}, username: "occupied", wantError: "创建管理员失败", repair: func(t *testing.T, db *gorm.DB) {
			var member model.User
			require.NoError(t, db.Where("username = ?", "occupied").First(&member).Error)
			assert.Equal(t, model.RoleMember, member.Role)
			assert.Equal(t, "existing-hash", member.Password)
		}},
		{name: "管理员查询错误不冒充已存在", prepare: func(t *testing.T, db *gorm.DB) {
			require.NoError(t, db.Migrator().DropTable(&model.User{}))
		}, wantError: "查询管理员数量失败", repair: func(t *testing.T, db *gorm.DB) {
			require.NoError(t, db.AutoMigrate(&model.User{}))
		}},
		{name: "锁写入错误不冒充已存在", prepare: func(t *testing.T, db *gorm.DB) {
			require.NoError(t, db.Migrator().DropTable(&model.AuthSetupLock{}))
		}, wantError: "获取初始化锁失败", repair: func(t *testing.T, db *gorm.DB) {
			require.NoError(t, db.AutoMigrate(&model.AuthSetupLock{}))
		}},
		{name: "密码加密失败不消费锁", password: strings.Repeat("x", 73), wantError: "加密密码失败", repair: func(*testing.T, *gorm.DB) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, reopen := openAuthSetupLockDatabase(t)
			if tt.prepare != nil {
				tt.prepare(t, db)
			}
			username, password := tt.username, tt.password
			if username == "" {
				username = "candidate"
			}
			if password == "" {
				password = "password123"
			}
			pair, err := newAuthSetupLockService(db).SetupAdmin(username, password)
			require.Error(t, err)
			assert.Nil(t, pair)
			if tt.wantAlready {
				assert.ErrorIs(t, err, service.ErrAdminAlreadyExists)
			} else {
				assert.NotErrorIs(t, err, service.ErrAdminAlreadyExists)
				assert.Contains(t, err.Error(), tt.wantError)
			}
			pool, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, pool.Close())
			db = reopen()
			if tt.repair != nil {
				tt.repair(t, db)
			}
			assertAuthSetupLockState(t, db, tt.admins, tt.locks)
			if !tt.wantAlready {
				pair, err = newAuthSetupLockService(db).SetupAdmin("recovered_admin", "password123")
				require.NoError(t, err)
				assertAuthSetupLockState(t, db, 1, 1)
				assertAuthSetupLockTokens(t, db, "recovered_admin", "password123", pair)
			}
		})
	}
}
