package vlrange

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// TestRealVLUserFilterCannotEscapeRangeScope 锁定作用域越权读（B-2）：
// 用户过滤表达式绝不允许改变 range 的授权范围。
//
// 漏洞形态：把用户过滤与作用域条件拼进同一个 LogsQL 串时，LogsQL 的 AND 优先级
// 高于 OR，`scope AND (f) OR (*)` 被解析为 `(scope AND f) OR (*)`，`OR (*)` 分支
// 即脱离 scope 约束，读到同一 VL 上其它实例的日志。修复方式是把作用域留给 query
// 参数、用户过滤交给 VL 的 extra_filters 参数独立解析（非法语法由 VL 直接拒绝）。
//
// 夹具刻意走投影分支（Projection.SourceProjections 非空），因为该分支以完整
// log_source_id 精确匹配、且 scope 自身被括号包裹——这正是生产热路径的形态，
// 也是「用户过滤的右括号闭合掉 scope 括号」得以逃逸的前提。
//
// 隔离环境要求：只使用本进程启动的 VL 实例与 t.TempDir() 数据根，不触碰生产。
// 未设置 JM_VL_BIN 时自动跳过。
func TestRealVLUserFilterCannotEscapeRangeScope(t *testing.T) {
	bin := os.Getenv("JM_VL_BIN")
	if bin == "" {
		t.Skip("set JM_VL_BIN to run real VictoriaLogs integration")
	}
	sha := os.Getenv("JM_VL_SHA256")
	require.NotEmpty(t, sha, "the real binary must match an explicit approved fingerprint")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	sup, err := vlsup.New(vlsup.Options{BinaryPath: bin, AssetSHA256: sha, DataRoot: t.TempDir(),
		AuthUsername: "fixture", AuthPassword: "fixture-only",
		Ports:              map[vlsup.Namespace]int{vlsup.NamespaceHot: port},
		MemoryAllowedBytes: map[vlsup.Namespace]int64{vlsup.NamespaceHot: 64 << 20}})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	require.NoError(t, sup.Start(ctx, vlsup.NamespaceHot))
	t.Cleanup(func() { require.NoError(t, sup.Stop(context.Background(), vlsup.NamespaceHot)) })
	vl, err := sup.ClientFor(vlsup.NamespaceHot)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return vl.Health(ctx) == nil }, 10*time.Second, 100*time.Millisecond)

	// 同一 Worker 的同一个 VL 上并存两个实例的日志：inst:1 是本次授权的目标，
	// inst:2 是调用方无权读取的其它实例；二者仅靠 log_source_id 区分。
	// source_generation / projection_generation / record_end 需与下方投影一致，
	// 否则事件会被 scope 自身合法地排除，测试将失去意义（退化为空集）。
	const closedSeq = 10
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	payload := ""
	for i, target := range []struct{ source, msg string }{
		{"inst:1/stdout", "AUTHORIZED"},
		{"inst:2/stdout", "FORBIDDEN"},
	} {
		payload += fmt.Sprintf("{\"_time\":%q,\"_msg\":%q,\"event_id\":%q,\"log_source_id\":%q,"+
			"\"source_generation\":\"s1\",\"projection_generation\":\"p1\",\"record_end\":%d,\"level\":\"INFO\"}\n",
			day.Add(time.Duration(12+i)*time.Hour).Format(time.RFC3339Nano), target.msg,
			fmt.Sprintf("EV%d", i), target.source, closedSeq)
	}
	_, err = vl.InsertJSONLines(ctx, []byte(payload))
	require.NoError(t, err)

	client, err := New(vl)
	require.NoError(t, err)
	rng := query.AuthoritativeRange{
		Key:              catalog.PartitionKey{StorageNamespace: "inst:1", UTCDay: day.Format("2006-01-02")},
		ClosedVisibleSeq: closedSeq,
		Projection: &catalog.PublishedProjection{SourceProjections: []catalog.SourceProjection{{
			SourceGenerationRef:   catalog.SourceGenerationRef{LogSourceID: "inst:1/stdout", SourceGeneration: "s1"},
			ProjectionGenerations: []string{"p1"},
			ClosedVisibleSeq:      closedSeq,
		}}},
	}

	// 前置断言：夹具必须真的能读到授权数据。若此处失败，说明 scope 没生效、
	// 后续「无泄漏」全是空集假象——这正是本测试最容易自我欺骗的地方。
	require.Eventually(t, func() bool {
		res, err := client.Search(ctx, rng, query.RangeQuery{Budget: query.Budget{Limit: 50}})
		return err == nil && len(res.Items) == 1 && res.Items[0].Source.LogSourceID == "inst:1/stdout"
	}, 20*time.Second, 200*time.Millisecond, "夹具未生效：授权范围内应恰有 1 条事件")

	// 攻击载荷：前两项在原「字符串拼接」实现下被实证会真正带出 inst:2 的数据
	// （隔离环境实测泄漏 10 条，含其它实例与其它 namespace）。后三项含管道，
	// VL 会直接 400；保留它们是为了锁住「即使用户提交管道语法也不得放行越界
	// 数据」这一不变式。本用例不带 level，故 CP 侧不给 keyword 补括号——这正是
	// 生产攻击路径。
	escapes := []struct{ name, filter string }{
		{"or_escape", "ZZZNONEXISTENT) OR (*"},
		{"newline_escape", "ZZZNONEXISTENT\n) OR (*"},
		{"balanced_or_escape", "(ZZZNONEXISTENT) OR (*)"},
		{"pipe_union_escape", "ZZZNONEXISTENT) OR (*) | union (*)"},
		{"stream_context_escape", "ZZZNONEXISTENT) OR (*) | stream_context after 5"},
	}
	for _, tc := range escapes {
		t.Run(tc.name, func(t *testing.T) {
			res, err := client.Search(ctx, rng, query.RangeQuery{Filter: tc.filter, Budget: query.Budget{Limit: 50}})
			// VL 在 extra_filters 中拒绝非法语法时直接 400；被拒绝即安全。
			if err != nil {
				return
			}
			for _, ev := range res.Items {
				require.Equal(t, "inst:1/stdout", ev.Source.LogSourceID,
					"用户过滤 %q 越出 range 作用域，读到了未授权实例的事件", tc.filter)
			}
		})
	}

	// 正向对照：合法过滤必须仍能命中授权范围内的数据（修复不得损害正常检索）。
	t.Run("legal_filter_still_matches", func(t *testing.T) {
		require.Eventually(t, func() bool {
			res, err := client.Search(ctx, rng, query.RangeQuery{Filter: "AUTHORIZED", Budget: query.Budget{Limit: 10}})
			return err == nil && len(res.Items) == 1 && res.Items[0].Source.LogSourceID == "inst:1/stdout"
		}, 15*time.Second, 200*time.Millisecond)
	})

	// 负向对照：合法过滤不得命中未授权实例的数据。
	t.Run("legal_filter_excludes_unauthorized", func(t *testing.T) {
		res, err := client.Search(ctx, rng, query.RangeQuery{Filter: "FORBIDDEN", Budget: query.Budget{Limit: 10}})
		require.NoError(t, err)
		require.Empty(t, res.Items, "无权实例的事件不得因过滤词匹配而返回")
	})
}
