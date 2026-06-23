package service

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newEnrollTokenSvc(t *testing.T) (*EnrollTokenService, *gorm.DB) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.NodeEnrollToken{}))
	return NewEnrollTokenService(db), db
}

func TestEnrollToken_IssueStoresHashOnlyAndReturnsPlaintextOnce(t *testing.T) {
	svc, db := newEnrollTokenSvc(t)

	tok, plaintext, err := svc.Issue("node-a", 30, 7)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(plaintext, "jmet_"), "明文应带 jmet_ 前缀")
	require.Equal(t, "node-a", tok.NodeName)
	require.Equal(t, uint(7), tok.CreatedBy)
	require.False(t, tok.Used)
	require.WithinDuration(t, time.Now().Add(30*time.Minute), tok.ExpiresAt, 5*time.Second)

	// 库内只存哈希，不存明文。
	var stored model.NodeEnrollToken
	require.NoError(t, db.First(&stored, tok.ID).Error)
	require.Equal(t, sha256Hex(plaintext), stored.TokenHash)
	require.NotContains(t, stored.TokenHash, plaintext)
	// 前缀仅供识别，不足以重建。
	require.True(t, strings.HasPrefix(plaintext, stored.TokenPrefix))
	require.LessOrEqual(t, len(stored.TokenPrefix), enrollTokenPrefixLen)
}

func TestEnrollToken_IssueDefaultsAndClampsTTL(t *testing.T) {
	svc, _ := newEnrollTokenSvc(t)

	// ttl<=0 取默认 30 分钟。
	tok, _, err := svc.Issue("", 0, 1)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(defaultEnrollTTLMinutes*time.Minute), tok.ExpiresAt, 5*time.Second)

	// 超上限取上限。
	tok2, _, err := svc.Issue("", maxEnrollTTLMinutes+10000, 1)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(maxEnrollTTLMinutes*time.Minute), tok2.ExpiresAt, 5*time.Second)
}

func TestEnrollToken_PeekValidAndConsumeOnce(t *testing.T) {
	svc, _ := newEnrollTokenSvc(t)
	tok, plaintext, err := svc.Issue("node-a", 30, 1)
	require.NoError(t, err)

	// 有效 token 可 peek。
	peeked, err := svc.PeekForNewNode(plaintext)
	require.NoError(t, err)
	require.Equal(t, tok.ID, peeked.ID)

	// 首次消费成功。
	require.NoError(t, svc.ConsumeForNewNode(plaintext, "uuid-1"))

	// 消费后再 peek 失败（已消费）。
	_, err = svc.PeekForNewNode(plaintext)
	require.ErrorIs(t, err, ErrEnrollTokenInvalid)

	// 二次消费失败（一次性）。
	require.ErrorIs(t, svc.ConsumeForNewNode(plaintext, "uuid-2"), ErrEnrollTokenInvalid)

	// 消费态落库正确：used + used_by_node 记首个消费者。
	got, err := svc.List()
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.True(t, got[0].Used)
	require.Equal(t, "uuid-1", got[0].UsedByNode)
	require.NotNil(t, got[0].UsedAt)
}

func TestEnrollToken_PeekRejectsExpired(t *testing.T) {
	svc, db := newEnrollTokenSvc(t)
	tok, plaintext, err := svc.Issue("", 30, 1)
	require.NoError(t, err)
	// 手动改过期时间到过去。
	require.NoError(t, db.Model(&tok).Update("expires_at", time.Now().Add(-time.Minute)).Error)

	_, err = svc.PeekForNewNode(plaintext)
	require.ErrorIs(t, err, ErrEnrollTokenInvalid)
	// 过期 token 也不能消费。
	require.ErrorIs(t, svc.ConsumeForNewNode(plaintext, "uuid-x"), ErrEnrollTokenInvalid)
}

func TestEnrollToken_PeekRejectsRevoked(t *testing.T) {
	svc, _ := newEnrollTokenSvc(t)
	tok, plaintext, err := svc.Issue("", 30, 1)
	require.NoError(t, err)
	require.NoError(t, svc.Revoke(tok.ID))

	_, err = svc.PeekForNewNode(plaintext)
	require.ErrorIs(t, err, ErrEnrollTokenInvalid)
	require.ErrorIs(t, svc.ConsumeForNewNode(plaintext, "uuid-x"), ErrEnrollTokenInvalid)
}

func TestEnrollToken_PeekRejectsUnknownAndEmpty(t *testing.T) {
	svc, _ := newEnrollTokenSvc(t)

	_, err := svc.PeekForNewNode("")
	require.ErrorIs(t, err, ErrEnrollTokenInvalid)

	_, err = svc.PeekForNewNode("jmet_does-not-exist")
	require.ErrorIs(t, err, ErrEnrollTokenInvalid)

	require.ErrorIs(t, svc.ConsumeForNewNode("", "uuid"), ErrEnrollTokenInvalid)
	require.ErrorIs(t, svc.ConsumeForNewNode("jmet_nope", "uuid"), ErrEnrollTokenInvalid)
}

// TestEnrollToken_ConsumeIsAtomicUnderConcurrency 验证一次性消费在并发下仅一个调用成功。
func TestEnrollToken_ConsumeIsAtomicUnderConcurrency(t *testing.T) {
	svc, _ := newEnrollTokenSvc(t)
	_, plaintext, err := svc.Issue("", 30, 1)
	require.NoError(t, err)

	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if err := svc.ConsumeForNewNode(plaintext, "uuid"); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, successes, "并发消费同一 token 应仅一个成功")
}

func TestEnrollToken_RevokeNotFound(t *testing.T) {
	svc, _ := newEnrollTokenSvc(t)
	require.ErrorIs(t, svc.Revoke(9999), ErrEnrollTokenNotFound)
}

func TestEnrollToken_ListOrdersByCreatedDesc(t *testing.T) {
	svc, _ := newEnrollTokenSvc(t)
	_, _, err := svc.Issue("first", 30, 1)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	_, _, err = svc.Issue("second", 30, 1)
	require.NoError(t, err)

	list, err := svc.List()
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "second", list[0].NodeName, "最新的排前面")
	require.Equal(t, "first", list[1].NodeName)
}
