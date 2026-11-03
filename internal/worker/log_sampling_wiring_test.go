package config

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
	"github.com/wcpe/JianManager/internal/worker/logs/sampling"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// TestLoadSamplingThroughRealManagerWALAndDelivery 验证「采样策略经真实 Manager 生效」：
// 配置文件 → cfg.SamplingPolicy() → ingest.Options → 每个源独立的 Sampler → 投递面。
//
// ## 2026-11-03 重写说明（必读）
//
// 本用例原先把三条判据绑在一起：① 采样命中条数；② 事件段落盘内容；③ 发布凭据
// （PublishedProjection/ClosedVisibleSeq）与 canonical 重放。
//
// 事件段（canonical 事件体副本）与重建/重放链**已整体删除**（2026-11-03 裁定：
// 只保留 WAL → 投递 VL），因此 ②③ 的对象已不存在。本用例保留 ① —— 那才是它要考的
// 「配置是否真的接线到投递面」，并改用**投递面收到的 payload**作为唯一事实源：
// 收得对，就说明策略生效；这与原断言口径等价，且不再依赖任何已删机制。
func TestLoadSamplingThroughRealManagerWALAndDelivery(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enabled  bool
		rules    string
		expected int
	}{
		{"关闭恒等", false, "    min_level: WARN\n", 4},
		{"开启汇总", true, "    min_level: WARN\n", 2},
		{"每源独立预算", true, "    budget_max_events_per_window: 1\n    budget_window: 24h\n    budget_keep_every: 100\n", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enabled := tc.enabled
			cfg, err := loadLogPolicyFixture(t, fmt.Sprintf("log_ingest:\n  sampling:\n    enabled: %t\n%s", enabled, tc.rules))
			require.NoError(t, err)
			root := t.TempDir()
			body := "[12:00:00] [Server thread/DEBUG]: first\n[12:00:01] [Server thread/DEBUG]: second\n[12:00:02] [Server thread/DEBUG]: third\n[12:00:03] [Server thread/WARN]: kept\n"
			if tc.name == "每源独立预算" {
				body = strings.Repeat("[12:00:00] [Server thread/DEBUG]: same\n", 3) + "[12:00:03] [Server thread/WARN]: kept\n"
			}

			var mu sync.Mutex
			// rows 是**投递面收到**的全部行（唯一事实源）。
			var rows []string
			seenSources := make(chan string, 32)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/health":
					w.WriteHeader(http.StatusOK)
				case "/insert/jsonline":
					payload, readErr := vlInsertBodyForTest(r)
					if readErr != nil {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					mu.Lock()
					for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
						var row struct {
							Source string `json:"log_source_id"`
							Level  string `json:"level"`
						}
						if json.Unmarshal([]byte(line), &row) != nil {
							continue
						}
						rows = append(rows, line)
						if row.Level == "WARN" {
							select {
							case seenSources <- row.Source:
							default:
							}
						}
					}
					mu.Unlock()
					w.WriteHeader(http.StatusOK)
				case "/select/logsql/query":
					// 投递主路径不再逐字段回查（FR-508）：查询面只回空，确保不会因为
					// 「夹具没实现校验」而误判投递失败。
					_, _ = io.WriteString(w, "")
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
			require.NoError(t, err)
			// 两个源都先建**空文件**并登记，再写入正文。
			//
			// FILE_PRIMARY 源登记一律 `StartAtEOF: true`（裁定 D：新源不回头吃历史）。
			// 若在登记前就写好内容，这几行会被 EOF 起点整体跳过 ⇒ 一轮采集零事件 ⇒ 用例假红。
			sources := []ingest.SourceConfig{}
			paths := map[string]string{}
			for _, id := range []string{"inst:first", "inst:later"} {
				path := filepath.Join(root, strings.ReplaceAll(id, ":", "-")+".log")
				require.NoError(t, os.WriteFile(path, nil, 0600))
				paths[id] = path
				sources = append(sources, ingest.SourceConfig{LogSourceID: id, SourceGeneration: "g1", Path: path, StorageNamespace: id, UTCDay: time.Now().UTC().Format("2006-01-02")})
			}
			cat := catalog.New(catalog.NewMemJournal())
			m, err := ingest.New(ingest.Options{
				Root: root, VL: client, Catalog: cat, Sources: sources[:1],
				Sampling: cfg.SamplingPolicy(), VerificationTimeout: 200 * time.Millisecond,
				MultilineUnclosedTimeout: time.Millisecond,
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = m.Stop() })
			require.NoError(t, m.Register(sources[1]), "后登记源必须沿用策略但创建独立pipeline")
			// 登记完成（两个源都定位到 EOF=0）后再写入正式内容。
			for id, path := range paths {
				require.NoError(t, os.WriteFile(path, []byte(body), 0600), "写入源 %s 的正文", id)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { m.Start(ctx); close(done) }()

			seen := map[string]bool{}
			timer := time.NewTimer(5 * time.Second)
			for len(seen) < 2 {
				select {
				case source := <-seenSources:
					seen[source] = true
				case <-timer.C:
					cancel()
					<-done
					t.Fatal("真实Manager未投递全部源")
				}
			}
			timer.Stop()
			cancel()
			<-done
			require.NoError(t, m.Stop())

			mu.Lock()
			captured := append([]string(nil), rows...)
			mu.Unlock()

			// 按源汇总投递面实际收到的正文（采样后的最终形态）。
			perSource := map[string][]string{}
			for _, line := range captured {
				var row struct {
					Source  string `json:"log_source_id"`
					Message string `json:"_msg"`
					Fields  map[string]any
				}
				if json.Unmarshal([]byte(line), &row) != nil {
					continue
				}
				perSource[row.Source] = append(perSource[row.Source], row.Message)
			}
			require.Len(t, perSource, 2, "两个源都必须有过投递")
			for id, msgs := range perSource {
				require.Len(t, msgs, tc.expected, "源 %s 的投递条数必须与采样策略一致", id)
			}
			if enabled {
				// 汇总事件必须真的出现过（而不是把 DEBUG 全丢了却不报数）。
				joined := strings.Join(captured, "\n")
				require.Contains(t, joined, "suppressed", "开启汇总时必须有汇总事件")
			}
		})
	}
}

// vlInsertBodyForTest 返回一次 VL 写入请求的**解压后**正文。
//
// 为什么夹具必须走它：vlsup 客户端默认对 `/insert/jsonline` 请求体做 gzip 并声明
// `Content-Encoding: gzip`。直接 io.ReadAll 拿到的是压缩字节，按行解析必然 0 行
// ⇒ 断言静默变成空断言（不是变红）。
func vlInsertBodyForTest(r *http.Request) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
		return raw, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	return io.ReadAll(zr)
}

var _ = sampling.FieldRule
