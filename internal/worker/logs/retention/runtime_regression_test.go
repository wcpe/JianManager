package retention

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 批准引擎仅消费filter，独立end参数不能保护新记录。
func TestRuntimeRegressionDeleteCutoffIsInsideFilter(t *testing.T) {
	var filter, end string
	d, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		filter, end = r.URL.Query().Get("filter"), r.URL.Query().Get("end")
		_, _ = w.Write([]byte(`{"task_id":"constructed-task"}`))
	})
	cutoff := time.Date(2026, 10, 21, 10, 0, 0, 123456789, time.UTC)
	if _, err := d.RunDeleteTask(context.Background(), `level:="DEBUG" AND log_source_id:="inst:147"`, cutoff); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filter, "_time:") || !strings.Contains(filter, cutoff.Add(-time.Nanosecond).Format(time.RFC3339Nano)) {
		t.Fatalf("截止必须以纳秒半开边界进入filter，不是仅放入end: filter=%q end=%q", filter, end)
	}
	if end != "" {
		t.Fatal("不得保留批准引擎不消费的end参数造成误解")
	}
}

func TestRuntimeRegressionDeleteRejectsFreeTextAndUnderflow(t *testing.T) {
	calls := 0
	d, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(`{"task_id":"x"}`)) })
	for _, tc := range []struct {
		filter string
		before time.Time
	}{
		{"*", time.Now()}, {"level:DEBUG OR *", time.Now()}, {`level:="DEBUG"`, time.Unix(0, -1<<63)},
		{"level:DEBUG", time.Now()}, {`level:"DEBUG" AND log_source_id:"inst:147"`, time.Now()},
		{`level:="DEBUG" AND log_source_id:"inst:147"`, time.Now()}, {`level:DEBUG AND log_source_id:="inst:147"`, time.Now()},
		{`level:="DEBUG" OR *`, time.Now()}, {`level:="DEBUG" AND log_source_id:="inst:147" OR *`, time.Now()},
	} {
		if _, err := d.RunDeleteTask(context.Background(), tc.filter, tc.before); err == nil {
			t.Fatalf("必须在下发前拒绝危险filter或cutoff-1ns下溢: %+v", tc)
		}
	}
	if calls != 0 {
		t.Fatal("非法删除必须零请求")
	}
}
