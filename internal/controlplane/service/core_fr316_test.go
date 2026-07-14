package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestJavaMajorForMCVersion 覆盖内置版本-Java 需求映射表的各版本段边界（FR-316）：
// ≤1.16→8、1.17→16、1.18~1.20.4→17、1.20.5+→21、26.1+（年号制）→25；
// 未知/解析不出的版本返回 0（不设需求，宽容不误拦）。
func TestJavaMajorForMCVersion(t *testing.T) {
	tests := []struct {
		version string
		want    int
	}{
		// ≤1.16 → 8
		{"1.8.8", 8},
		{"1.12.2", 8},
		{"1.16", 8},
		{"1.16.5", 8},
		// 1.17 段 → 16
		{"1.17", 16},
		{"1.17.1", 16},
		// 1.18 ~ 1.20.4 → 17
		{"1.18", 17},
		{"1.19.4", 17},
		{"1.20", 17},
		{"1.20.4", 17},
		// 1.20.5+ → 21
		{"1.20.5", 21},
		{"1.20.6", 21},
		{"1.21", 21},
		{"1.21.8", 21},
		// 年号制 26.1+ → 25（真机事故：MC 26.1 要求 Java 25）
		{"26.1", 25},
		{"26.2", 25},
		{"27.1", 25},
		// 预发布后缀取数字前缀
		{"1.20.5-pre1", 21},
		{"1.21.1-SNAPSHOT", 21},
		// 未知/解析不出 → 0（不误拦）
		{"latest", 0},
		{"", 0},
		{"abc", 0},
		{"0.9", 0},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			require.Equal(t, tt.want, javaMajorForMCVersion(tt.version))
		})
	}
}

// newPaperJavaStub 构造带 fill v3 版本详情端点的 Paper stub：
// 1.21.1 的版本详情携带 java.version.minimum=22（与内置表 21 刻意不同，证明上游优先）；
// 1.20.6 无版本详情端点（404），验证回退内置映射表。
func newPaperJavaStub(t *testing.T) *CoreService {
	t.Helper()
	mux := http.NewServeMux()
	buildsJSON := []byte(`[
		{"id":196,"downloads":{"server:default":{"name":"paper.jar","checksums":{"sha256":"bbb"},"url":"https://cdn.example/paper.jar"}}}
	]`)
	mux.HandleFunc("/paper/versions/1.21.1/builds", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buildsJSON)
	})
	mux.HandleFunc("/paper/versions/1.20.6/builds", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buildsJSON)
	})
	mux.HandleFunc("/paper/versions/1.21.1", func(w http.ResponseWriter, r *http.Request) {
		// fill v3 版本详情：java 需求在 version.java.version.minimum。
		_, _ = w.Write([]byte(`{"version":{"id":"1.21.1","java":{"version":{"minimum":22}}},"builds":[196]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &CoreService{client: srv.Client(), base: srv.URL, spongeBase: srv.URL, forgeBase: srv.URL}
}

// TestCoreResolveBuild_JavaMajorRequired 验证 ResolveBuild 响应携带 Java 需求（FR-316）：
// Paper 优先用 fill v3 上游元数据，上游不可得回退内置表；Sponge 用内置表；代理核心不设需求。
func TestCoreResolveBuild_JavaMajorRequired(t *testing.T) {
	s := newPaperJavaStub(t)

	// 上游版本详情可得 → 用上游值（22），不用内置表值（21）。
	upstream, err := s.ResolveBuild(context.Background(), "paper", "1.21.1", 0)
	require.NoError(t, err)
	require.Equal(t, 22, upstream.JavaMajorRequired)

	// 上游版本详情 404 → 回退内置映射表（1.20.6 → 21），解析本身不受影响。
	fallback, err := s.ResolveBuild(context.Background(), "paper", "1.20.6", 0)
	require.NoError(t, err)
	require.Equal(t, 21, fallback.JavaMajorRequired)

	// Sponge 家族无上游 java 元数据 → 内置映射表（1.21.1 → 21）。
	sponge := newSpongeStub(t)
	sv, err := sponge.ResolveBuild(context.Background(), "spongevanilla", "1.21.1", 0)
	require.NoError(t, err)
	require.Equal(t, 21, sv.JavaMajorRequired)

	// 代理核心（bungeecord latest）无 MC 版本语义 → 不设需求（0，JSON omitempty 不外泄）。
	bc, err := s.ResolveBuild(context.Background(), "bungeecord", "latest", 0)
	require.NoError(t, err)
	require.Equal(t, 0, bc.JavaMajorRequired)
}
