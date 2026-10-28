package ingest

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// FR-485：采集面可观测性快照的取值语义。
//
// 覆盖三条容易写错、且写错后**不会报错只会静默**的判据：
//  1. 没有登记任何采集源时 Available=false（「没有采集任务」不等于「采集积压为零」）；
//  2. 累计量按真实口径暴露（纳秒存储的慢查询应按毫秒暴露）；
//  3. 投递失败分类由累计计数取最多者，且空批不计入分母。
func TestLogMetricsSnapshot_Semantics(t *testing.T) {
	root := t.TempDir()
	m, err := newTestManager(t, Options{Root: root, Catalog: catalog.New(nil)})
	require.NoError(t, err)

	t.Run("无采集源时不可用", func(t *testing.T) {
		snap := m.LogMetricsSnapshot()
		require.False(t, snap.Available, "0 源时必须标记为不可用，而不是报一组 0 读数")
	})

	t.Run("累计计数器原样暴露", func(t *testing.T) {
		m.persistBytesTotal.Store(4096)
		m.verifyBackoffCount.Store(12)
		m.slowSQLCount.Store(3)
		m.slowSQLMaxNs.Store(int64(2500 * time.Millisecond))

		snap := m.LogMetricsSnapshot()
		require.Equal(t, int64(4096), snap.PersistBytesTotal)
		require.Equal(t, int64(12), snap.VerifyBackoffCount)
		require.Equal(t, int64(3), snap.SlowSQLCount)
		require.Equal(t, int64(2500), snap.SlowSQLMaxMs, "纳秒应按毫秒口径暴露")
	})

	t.Run("投递失败分类取最多者", func(t *testing.T) {
		// 直接走统一收口函数（deliver 内部即调用它），验证分类与计数。
		batch := []logtypes.Event{{EventID: "e1"}}
		m.noteDeliveryOutcome(batch, pipeline.DeliveryResult{}, &vlsup.StatusError{Code: 400})
		m.noteDeliveryOutcome(batch, pipeline.DeliveryResult{}, &vlsup.StatusError{Code: 422})
		m.noteDeliveryOutcome(batch, pipeline.DeliveryResult{}, errors.New("connection refused"))

		snap := m.LogMetricsSnapshot()
		require.Equal(t, int64(3), snap.DeliveryBatches)
		require.Equal(t, int64(3), snap.DeliveryFailures)
		require.Equal(t, "status_4xx", snap.DominantFailureClass, "出现最多的分类应胜出")
	})

	t.Run("空批不计入分母", func(t *testing.T) {
		before := m.LogMetricsSnapshot().DeliveryBatches
		m.noteDeliveryOutcome(nil, pipeline.DeliveryResult{}, nil)
		require.Equal(t, before, m.LogMetricsSnapshot().DeliveryBatches, "空批不是一次投递尝试")
	})

	t.Run("成功投递计入分母但不计失败", func(t *testing.T) {
		before := m.LogMetricsSnapshot()
		m.noteDeliveryOutcome([]logtypes.Event{{EventID: "ok"}}, pipeline.DeliveryResult{}, nil)
		after := m.LogMetricsSnapshot()
		require.Equal(t, before.DeliveryBatches+1, after.DeliveryBatches)
		require.Equal(t, before.DeliveryFailures, after.DeliveryFailures)
	})
}

// FR-485：投递失败分类的判据。
//
// 分类的价值在于区分「重试有用吗」——凭据错（auth）重试无用且要人介入，
// 5xx/传输错则可能自愈。判据与 vlsup.IsPermanent 同源，不另造一套。
func TestClassifyDeliveryFailure(t *testing.T) {
	require.Equal(t, "", classifyDeliveryFailure(nil))

	require.Equal(t, "auth", classifyDeliveryFailure(
		fmt.Errorf("wrap: %w", vlsup.ErrUnauthorized)), "包装后的认证错误仍应归为 auth")

	require.Equal(t, "status_4xx", classifyDeliveryFailure(&vlsup.StatusError{Code: 400}))
	require.Equal(t, "status_4xx", classifyDeliveryFailure(&vlsup.StatusError{Code: 403}))
	require.Equal(t, "status_5xx", classifyDeliveryFailure(&vlsup.StatusError{Code: 503}))
	require.Equal(t, "transport", classifyDeliveryFailure(errors.New("connection refused")))
}
