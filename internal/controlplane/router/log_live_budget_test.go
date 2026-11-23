package router

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/logcoord"
)

func TestLiveRequestBudgetDoesNotInheritHistoricalPageDefaults(t *testing.T) {
	for _, limit := range []int{0, 100, 1000} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/live?advanced=true", nil)
		budget, duration, err := liveRequestBudget(c, logcoord.QueryBudget{Limit: limit, MaxBytes: 32 << 20, TimeoutMS: 30000, MaxFanout: 64}, 45*time.Second)
		require.NoError(t, err)
		require.Equal(t, logcoord.LiveSessionRows, budget.Limit)
		require.Equal(t, logcoord.LiveSessionBytes, budget.MaxBytes)
		require.Equal(t, uint32(logcoord.LiveWorkerLimit), budget.MaxFanout)
		require.Equal(t, 30*time.Second, duration)
	}
}

func TestLiveRequestBudgetOnlyTightensExplicitAndExistingLimits(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/live?limit=1200&maxBytes=1024&timeoutMs=5000&maxFanout=2", nil)
	budget, duration, err := liveRequestBudget(c, logcoord.QueryBudget{Limit: 1000, MaxBytes: 512, TimeoutMS: 3000, MaxFanout: 1}, 45*time.Second)
	require.NoError(t, err)
	require.Equal(t, 1200, budget.Limit, "explicit live limit is independent of historical page ceiling")
	require.Equal(t, uint64(512), budget.MaxBytes)
	require.Equal(t, uint32(1), budget.MaxFanout)
	require.Equal(t, 3*time.Second, duration)
}

func TestLiveRequestBudgetRespectsSessionCeilingsAndTokenExpiry(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/live?limit=99999&maxBytes=999999999&timeoutMs=999999&maxFanout=999", nil)
	budget, duration, err := liveRequestBudget(c, logcoord.QueryBudget{}, 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, logcoord.LiveSessionRows, budget.Limit)
	require.Equal(t, logcoord.LiveSessionBytes, budget.MaxBytes)
	require.Equal(t, uint32(logcoord.LiveWorkerLimit), budget.MaxFanout)
	require.Equal(t, 2*time.Second, duration)
}

func TestLiveRequestBudgetRejectsMalformedExplicitLimits(t *testing.T) {
	for _, query := range []string{"limit=-1", "limit=0", "maxBytes=invalid", "timeoutMs=0", "maxFanout=-2"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/live?"+query, nil)
		_, _, err := liveRequestBudget(c, logcoord.QueryBudget{}, 45*time.Second)
		require.Error(t, err, query)
	}
}
