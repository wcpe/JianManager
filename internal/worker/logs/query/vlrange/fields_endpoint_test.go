package vlrange

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 回归（2026-11-05 架构审计 D3）：Fields 必须问 VL「这个窗口有哪些字段」，
// 而不是从返回的一页日志里猜。
//
// # 旧实现的缺陷
//
// 旧代码调 Search 拿一页行，遍历每行的 Fields map 去重。
// 于是返回的是「本页那几十条日志恰好带的字段」——不是时间窗的字段全集。
// 实测：旧实现返回 [level, stream] 两项，而 VL 里同一窗口有 15+ 个字段。
//
// # 本条用例怎么钉住
//
// 用假 VL：`/select/logsql/query` 故意返回**不带任何额外字段**的行，
// `/select/logsql/field_names` 返回一份字段全集。
// 若实现退回「从行里猜」，结果会是空（因为行没有字段），用例立刻转红。
func TestFieldsUsesFieldNamesEndpointNotReturnedRows(t *testing.T) {
	var fieldNamesCalls atomic.Int64
	var queryCalls atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		switch {
		case strings.HasSuffix(r.URL.Path, "/select/logsql/field_names"):
			fieldNamesCalls.Add(1)
			// 该窗口的字段全集（含行里根本没有的那些）。
			_, _ = w.Write([]byte(`{"values":[
			  {"value":"_msg","hits":100},
			  {"value":"event_id","hits":100},
			  {"value":"canonical_content_hash","hits":100},
			  {"value":"record_start","hits":100},
			  {"value":"level","hits":100}]}`))
		case strings.HasSuffix(r.URL.Path, "/select/logsql/query"):
			queryCalls.Add(1)
			// 刻意返回**不带额外字段**的行：旧实现从这些行里猜，只会得到空集。
			_, _ = w.Write([]byte(`{"_time":"2026-11-04T01:00:00Z","event_id":"e1",` +
				`"log_source_id":"inst:318/file","source_generation":"g1",` +
				`"record_start":"1","record_end":"2","_msg":"hello"}` + "\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)

	rng := query.AuthoritativeRange{
		Key:   catalog.PartitionKey{StorageNamespace: "inst:318", UTCDay: "2026-11-04"},
		Owner: catalog.OwnerHot,
	}
	res, err := client.Fields(context.Background(), rng, query.RangeQuery{
		TimeRange: query.TimeRange{
			FromUTC: "2026-11-04T00:00:00Z",
			ToUTC:   "2026-11-05T00:00:00Z",
		},
		Budget: query.Budget{Limit: 10},
	})
	require.NoError(t, err)

	require.EqualValues(t, 1, fieldNamesCalls.Load(),
		"必须问 field_names 端点，而不是从返回行里猜")
	require.Equal(t, 0, int(queryCalls.Load()),
		"不应为拿字段列表而拉一页日志（那正是旧实现的缺陷）")

	// 行里没有 canonical_content_hash / record_start，但它们属于该窗口 ⇒ 必须出现。
	require.Contains(t, res.Fields, "canonical_content_hash")
	require.Contains(t, res.Fields, "record_start")
	require.Contains(t, res.Fields, "level")
	require.Len(t, res.Fields, 5, "应返回 field_names 给的全集，实际: %v", res.Fields)
}

// 字段名查询必须受 range 的权威谓词约束，不能跨源泄漏。
//
// 否则用户会在实例 A 的筛选面板里看到「只有实例 B 才有的字段」，
// 点了必然查不到——比字段列表不完整更难排查。
func TestFieldsScopesFieldNamesToRangeSelector(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		if strings.HasSuffix(r.URL.Path, "/select/logsql/field_names") {
			gotQuery = r.Form.Get("query")
			_, _ = w.Write([]byte(`{"values":[{"value":"level","hits":1}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)

	rng := query.AuthoritativeRange{
		Key:   catalog.PartitionKey{StorageNamespace: "inst:318", UTCDay: "2026-11-04"},
		Owner: catalog.OwnerHot,
	}
	_, err = client.Fields(context.Background(), rng, query.RangeQuery{
		TimeRange: query.TimeRange{FromUTC: "2026-11-04T00:00:00Z", ToUTC: "2026-11-05T00:00:00Z"},
		Budget:    query.Budget{Limit: 10},
	})
	require.NoError(t, err)

	require.Contains(t, gotQuery, "inst:318/",
		"字段名查询必须带 range 的作用域谓词（命名空间前缀），实际: %s", gotQuery)
}

// 字段名查询失败不得伪装成「没有字段」：前端会把筛选面板整个清空。
func TestFieldsPropagatesFieldNamesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)

	rng := query.AuthoritativeRange{
		Key:   catalog.PartitionKey{StorageNamespace: "inst:318", UTCDay: "2026-11-04"},
		Owner: catalog.OwnerHot,
	}
	_, err = client.Fields(context.Background(), rng, query.RangeQuery{
		TimeRange: query.TimeRange{FromUTC: "2026-11-04T00:00:00Z", ToUTC: "2026-11-05T00:00:00Z"},
		Budget:    query.Budget{Limit: 10},
	})
	require.Error(t, err, "字段名查询失败必须报错，不能返回空列表冒充成功")
	_ = time.Now
}
