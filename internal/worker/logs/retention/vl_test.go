package retention

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 本文件补上执行器**真正删数据的那一层**的回归：ClientDeleter 把策略目标翻译成
// VL 的 HTTP 请求。上面 Sweeper 的用例用的是假实现（只验证「该不该下发」），
// 若没有这里的用例，「end 参数取错」「过滤器没带过去」「鉴权没加」这类错误
// 在假实现下全部不可见——变异实验（N11）正是这样暴露出本文件此前缺失。

// newTestClient 启动一个假 VL 并返回适配器。
func newTestClient(t *testing.T, handler http.HandlerFunc) (ClientDeleter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{
		BaseURL:  srv.URL,
		Username: "jm",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("构造 VL 客户端失败：%v", err)
	}
	return ClientDeleter{Client: client}, srv
}

// TestClientDeleterBuildsRequest 钉住请求形状：路径、过滤器、截止时间、鉴权。
//
// 其中 **end 参数取错** 是最危险的一类错误：它决定「删到哪一刻为止」。
// 若误用 time.Now()（而不是策略算出的 cutoff），删除会一路删到最新数据。
func TestClientDeleterBuildsRequest(t *testing.T) {
	var (
		gotPath  string
		gotQuery url.Values
		gotAuth  bool
	)
	d, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		u, p, ok := r.BasicAuth()
		gotAuth = ok && u == "jm" && p == "secret"
		_, _ = w.Write([]byte(`{"task_id":"1790891448781593333"}`))
	})

	cutoff := baseTime.Add(-DefaultTTLDebug)
	id, err := d.RunDeleteTask(context.Background(), `level:DEBUG AND log_source_id:"inst:147"`, cutoff)
	if err != nil {
		t.Fatalf("下发删除任务失败：%v", err)
	}
	if id != "1790891448781593333" {
		t.Errorf("任务 ID 解析错误：%q", id)
	}
	if gotPath != "/delete/run_task" {
		t.Errorf("路径应为 /delete/run_task，得到 %q", gotPath)
	}
	if got := gotQuery.Get("filter"); got != `level:DEBUG AND log_source_id:"inst:147"` {
		t.Errorf("过滤器必须原样送达，得到 %q", got)
	}
	// end 必须是策略算出的 cutoff，逐纳秒一致。
	if got := gotQuery.Get("end"); got != cutoff.UTC().Format(time.RFC3339Nano) {
		t.Errorf("end 必须是策略截止时间 %q，得到 %q（取错会一路删到最新数据）",
			cutoff.UTC().Format(time.RFC3339Nano), got)
	}
	// 不得设置 start：保留期的作用是「保留最近 N」，设下界反而会漏掉更早的历史。
	if _, ok := gotQuery["start"]; ok {
		t.Errorf("不应携带 start 参数，得到 %q", gotQuery.Get("start"))
	}
	if !gotAuth {
		t.Error("删除接口受 basic auth 保护，必须带凭据（真机实测缺凭据返回 401）")
	}
}

// TestClientDeleterRejectsMissingTaskID：响应缺 task_id 必须报错，而不是当成成功。
func TestClientDeleterRejectsMissingTaskID(t *testing.T) {
	d, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := d.RunDeleteTask(context.Background(), "level:DEBUG", baseTime); err == nil {
		t.Fatal("缺少 task_id 必须报错——否则失败的删除会被记成已下发")
	}
}

func TestClientDeleterRejectsMalformedBody(t *testing.T) {
	d, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	})
	if _, err := d.RunDeleteTask(context.Background(), "level:DEBUG", baseTime); err == nil {
		t.Fatal("响应不是 JSON 必须报错")
	}
}

// TestClientDeleterSurfacesHTTPError：VL 侧拒绝（例如未开 -delete.enable 返回 400）必须冒泡。
func TestClientDeleterSurfacesHTTPError(t *testing.T) {
	d, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("requests to /delete/* are disabled; pass -delete.enable command-line flag for enabling them"))
	})
	_, err := d.RunDeleteTask(context.Background(), "level:DEBUG", baseTime)
	if err == nil {
		t.Fatal("VL 拒绝时必须报错")
	}
	if !strings.Contains(err.Error(), "delete.enable") {
		t.Errorf("错误信息应保留 VL 的原话（运维据此知道要加开关），得到 %v", err)
	}
}

func TestClientDeleterWithoutClientFails(t *testing.T) {
	if _, err := (ClientDeleter{}).RunDeleteTask(context.Background(), "level:DEBUG", baseTime); err == nil {
		t.Fatal("客户端未就绪必须报错而不是静默成功")
	}
}

// TestSweeperToRealHTTPEndToEnd 把策略 → 计划 → 请求三段串起来跑一遍，
// 确认「策略里写的保留期」最终真的变成了「请求里的 end」。
func TestSweeperToRealHTTPEndToEnd(t *testing.T) {
	type req struct{ filter, end string }
	var reqs []req
	d, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, req{filter: r.URL.Query().Get("filter"), end: r.URL.Query().Get("end")})
		_, _ = w.Write([]byte(`{"task_id":"t"}`))
	})

	p := discardPolicy()
	// 全部 5 档都显式给出：漏配的档会被 Normalize 补成推荐值（见 N10 的用例），
	// 本用例要断言的是「策略值 → 请求 end」的端到端映射，不是补默认值。
	p.ByLevel = map[string]time.Duration{
		"TRACE": 6 * time.Hour,
		"DEBUG": 3 * 24 * time.Hour,
		"INFO":  7 * 24 * time.Hour,
		"WARN":  30 * 24 * time.Hour,
		"ERROR": 90 * 24 * time.Hour,
	}
	sw := NewSweeper(p, d)

	res, err := sw.Sweep(context.Background(), baseTime, []string{"inst:147"}, DefaultScanLevels())
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if res.Submitted == 0 || res.Submitted != len(reqs) {
		t.Fatalf("下发数与实际请求数应一致：submitted=%d requests=%d", res.Submitted, len(reqs))
	}
	want := map[string]string{}
	for lvl, ttl := range p.ByLevel {
		want["level:"+lvl] = baseTime.Add(-ttl).UTC().Format(time.RFC3339Nano)
	}
	for _, r := range reqs {
		expected, ok := want[r.filter]
		if !ok {
			t.Errorf("意外过滤器 %q", r.filter)
			continue
		}
		if r.end != expected {
			t.Errorf("%s 的 end 期望 %q 得到 %q", r.filter, expected, r.end)
		}
		delete(want, r.filter)
	}
	if len(want) != 0 {
		t.Errorf("有档位没有下发：%v", want)
	}
}
