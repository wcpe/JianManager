package router

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/logcoord"
)

// TestFederationFilter_RejectsUnbalancedKeyword 守护级别/来源筛选不可被 keyword 绕过。
//
// 背景（2026-11-09 真机实测）：库里 2872 条全是 INFO，
// `pinged AND level:WARN` 正确得 0 条；而 `(InitialHandler) OR (pinged) AND level:WARN`
// 得 2872 条——括号被提前闭合，`AND level:WARN` 只约束了后一支，
// 用户点「只看 WARN」却拿到全量 INFO。
//
// 因此 keyword 括号/引号不配对时必须拒绝（返 400），而不是静默转义或重排：
// 后者会改变用户表达式的含义，且比报错更难排查。
func TestFederationFilter_RejectsUnbalancedKeyword(t *testing.T) {
	for _, tc := range []struct {
		name    string
		keyword string
	}{
		{"右括号提前闭合", "a) OR (b"},
		{"多余左括号", "(a OR b"},
		{"多余右括号", "a OR b)"},
		{"双引号未闭上", `_msg:"abc`},
		{"深度转负", "a)b("},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w1 := &fedWorker{
				items:   []logcoord.Event{fedEvent("e1", "src", "g", 1, "2026-09-20T12:00:00Z", "INFO", "hi")},
				targets: []logcoord.WorkerTargetResult{fedSuccess("inst:1", "w1", "src/g:1", "1")},
			}
			resolver := &fedResolver{targets: []logcoord.TargetInfo{
				{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline},
			}}
			coord := logcoord.New(resolver, &fedDialer{clients: map[string]logcoord.WorkerClient{"w1": w1}})
			r, _ := setupFederationRouter(t, coord)
			token := getAdminToken(t, r)

			// 必须带结构化约束：只有「要包进 ( )」时才存在括号被提前闭合的风险。
			w := makeRequest(r, "GET",
				"/api/v1/logs/federation/search?limit=100&level=warn&keyword="+url.QueryEscape(tc.keyword),
				nil, token)
			require.Equal(t, http.StatusBadRequest, w.Code, "body=%s", w.Body.String())
			assert.Contains(t, w.Body.String(), "INVALID_REQUEST")
			assert.Empty(t, w1.lastFilter, "不配对的 keyword 不得下发到 Worker")
		})
	}
}

// TestFederationFilter_AcceptsBalancedKeyword 确认收紧没有误伤合法表达式。
//
// 「只守括号平衡」是刻意选择的下限：宁可放过语义可疑的表达式，也不把用户的
// 合法布尔筛选判成非法（那会让关键词筛选直接不可用）。
func TestFederationFilter_AcceptsBalancedKeyword(t *testing.T) {
	for _, tc := range []struct {
		name    string
		keyword string
		want    string
	}{
		{"无括号", "needle", "(needle) AND level:WARN"},
		{"已配对括号", "(a OR b)", "((a OR b)) AND level:WARN"},
		{"多组配对", "(a OR b) AND (c OR d)", "((a OR b) AND (c OR d)) AND level:WARN"},
		// 引号内括号不计入深度，否则 `_msg:"a)"` 会被误判为不配对。
		{"引号内含右括号", `_msg:"a)"`, `(_msg:"a)") AND level:WARN`},
		{"转义引号", `_msg:"a\"b"`, `(_msg:"a\"b") AND level:WARN`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w1 := &fedWorker{
				items:   []logcoord.Event{fedEvent("e1", "src", "g", 1, "2026-09-20T12:00:00Z", "WARN", "hi")},
				targets: []logcoord.WorkerTargetResult{fedSuccess("inst:1", "w1", "src/g:1", "1")},
			}
			resolver := &fedResolver{targets: []logcoord.TargetInfo{
				{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline},
			}}
			coord := logcoord.New(resolver, &fedDialer{clients: map[string]logcoord.WorkerClient{"w1": w1}})
			r, _ := setupFederationRouter(t, coord)
			token := getAdminToken(t, r)

			w := makeRequest(r, "GET",
				"/api/v1/logs/federation/search?limit=100&level=warn&keyword="+url.QueryEscape(tc.keyword),
				nil, token)
			require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
			assert.Equal(t, tc.want, w1.lastFilter)
		})
	}
}

// TestFederationFilter_NoStructuredFilterKeepsKeywordAsIs 确认无结构化约束时行为不变。
//
// 此时不包括号，也就不存在括号被提前闭合的问题，原有自由布尔表达式必须继续可用。
func TestFederationFilter_NoStructuredFilterKeepsKeywordAsIs(t *testing.T) {
	w1 := &fedWorker{
		items:   []logcoord.Event{fedEvent("e1", "src", "g", 1, "2026-09-20T12:00:00Z", "INFO", "hi")},
		targets: []logcoord.WorkerTargetResult{fedSuccess("inst:1", "w1", "src/g:1", "1")},
	}
	resolver := &fedResolver{targets: []logcoord.TargetInfo{
		{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline},
	}}
	coord := logcoord.New(resolver, &fedDialer{clients: map[string]logcoord.WorkerClient{"w1": w1}})
	r, _ := setupFederationRouter(t, coord)
	token := getAdminToken(t, r)

	// 无结构化条件仍保留合法布尔表达式，不将字段查询转义成纯文本。
	// 不配对/管道表达式现在对两个入口统一拒绝，见 log_advanced_query_test.go。
	w := makeRequest(r, "GET",
		"/api/v1/logs/federation/search?limit=100&keyword="+url.QueryEscape("a OR (b)"),
		nil, token)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	assert.Equal(t, "a OR (b)", w1.lastFilter)
}

// TestFederationFacets_DimensionLimitClamped 守护每维上限不被负值/超限值绕过。
//
// 背景：FacetsQuery.DimensionLimit 是 uint32，CP 直接把用户输入转换：
// `?dimensionLimit=-1` → 4294967295，而下游判据是 `uint32(len(values)) > limit`，
// 于是裁剪恒不发生、`Truncated` 也不置位——高基数维度的全量值集会一次性进 CP 内存，
// 并被 aggCache 按条数（非字节）缓存留驻。
// field_values 不向 VL 传 limit，本地裁剪是该响应的唯一体积闸，所以这层钳制不可省。
func TestFederationFacets_DimensionLimitClamped(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  uint32
	}{
		{"未传取 0（下游回填默认）", "", 0},
		{"正常值原样传递", "&dimensionLimit=8", 8},
		{"负值钳成 0", "&dimensionLimit=-1", 0},
		{"超大负值钳成 0", "&dimensionLimit=-999999", 0},
		{"超上限钳到硬上限", "&dimensionLimit=999999", logcoord.MaxFacetDimensionLimit},
		{"恰好等于上限不变", "&dimensionLimit=" + "1000", logcoord.MaxFacetDimensionLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w1 := &fedWorker{
				items:   []logcoord.Event{fedEvent("e1", "src", "g", 1, "2026-09-20T12:00:00Z", "INFO", "hi")},
				targets: []logcoord.WorkerTargetResult{fedSuccess("inst:1", "w1", "src/g:1", "1")},
			}
			resolver := &fedResolver{targets: []logcoord.TargetInfo{
				{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline},
			}}
			coord := logcoord.New(resolver, &fedDialer{clients: map[string]logcoord.WorkerClient{"w1": w1}})
			r, _ := setupFederationRouter(t, coord)
			token := getAdminToken(t, r)

			w := makeRequest(r, "GET",
				"/api/v1/logs/federation/facets?dimensions=level"+tc.query, nil, token)
			require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
			assert.Equal(t, tc.want, w1.lastFacetLimit,
				"每维上限必须钳制后下发；直接 uint32(用户输入) 会让负值溢出成 4294967295")
		})
	}
}

func TestFederationFilter_AdvancedLogsQL_IsScopedAndBounded(t *testing.T) {
	tests := []struct {
		name       string
		logsQL     string
		query      string
		wantStatus int
		wantFilter string
	}{
		{
			name:       "advanced expression is combined with structured filters",
			logsQL:     `(error OR level:WARN)`,
			query:      "level=warn&source=instance",
			wantStatus: http.StatusOK,
			wantFilter: `((error OR level:WARN)) AND level:WARN AND log_source_id:"inst:"`,
		},
		{
			name:       "pipes are rejected because search endpoint expects event rows",
			logsQL:     `error | stats by (level) count()`,
			query:      "",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unbalanced advanced expression is rejected",
			logsQL:     `(error OR warn`,
			query:      "",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "oversized advanced expression is rejected",
			logsQL:     strings.Repeat("error ", 2048),
			query:      "",
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w1 := &fedWorker{
				items:   []logcoord.Event{fedEvent("e1", "inst:1/file", "g", 1, "2026-09-20T12:00:00Z", "WARN", "error")},
				targets: []logcoord.WorkerTargetResult{fedSuccess("inst:1", "w1", "inst:1/file", "1")},
			}
			resolver := &fedResolver{targets: []logcoord.TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline}}}
			coord := logcoord.New(resolver, &fedDialer{clients: map[string]logcoord.WorkerClient{"w1": w1}})
			r, _ := setupFederationRouter(t, coord)
			token := getAdminToken(t, r)
			path := "/api/v1/logs/federation/search?limit=100&advanced=true&logsQL=" + url.QueryEscape(tc.logsQL)
			if tc.query != "" {
				path += "&" + tc.query
			}
			w := makeRequest(r, "GET", path, nil, token)
			require.Equal(t, tc.wantStatus, w.Code, "body=%s", w.Body.String())
			if tc.wantStatus == http.StatusOK {
				assert.Equal(t, tc.wantFilter, w1.lastFilter)
				// The filter is separate from the authorized target selector; an expression
				// must never become a way to broaden the target set.
				assert.Equal(t, []string{"inst:1"}, w1.lastTargets)
			} else {
				assert.Contains(t, w.Body.String(), "INVALID_REQUEST")
				assert.Empty(t, w1.lastFilter)
			}
		})
	}
}
