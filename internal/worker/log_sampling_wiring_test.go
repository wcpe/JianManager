package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/eventstore"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/sampling"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 使用生产 Manager 与文件 pipeline，仅用回环 HTTP 模拟 VL，不访问部署环境。
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
			var rows []string
			var walErrors []string
			checkWAL := true
			seenSources := make(chan string, 32)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/health":
					w.WriteHeader(http.StatusOK)
				case "/insert/jsonline":
					payload, readErr := io.ReadAll(r.Body)
					if readErr != nil {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					// 投递面收到的每个汇总/原文身份必须已经有 durable WAL 责任记录。
					idx, openErr := stateindex.OpenReadOnly(filepath.Join(root, "var", "log", "ingest.index.db"))
					var state stateindex.State
					if openErr == nil {
						state, openErr = idx.Load()
						_ = idx.Close()
					}
					mu.Lock()
					if checkWAL && openErr != nil {
						walErrors = append(walErrors, openErr.Error())
					}
					for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
						var row struct {
							ID     string `json:"event_id"`
							Source string `json:"log_source_id"`
							Level  string `json:"level"`
						}
						if json.Unmarshal([]byte(line), &row) != nil {
							continue
						}
						found := false
						for _, entry := range state.WAL {
							if entry.EventID == row.ID && entry.Appended && entry.Durable {
								found = true
								break
							}
						}
						if checkWAL && !found {
							walErrors = append(walErrors, "投递身份未先持久化WAL: "+row.ID)
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
					query := r.URL.Query().Get("query")
					limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
					mu.Lock()
					defer mu.Unlock()
					count := 0
					for _, line := range rows {
						var row struct {
							Generation string `json:"projection_generation"`
							Source     string `json:"log_source_id"`
						}
						if json.Unmarshal([]byte(line), &row) != nil || !strings.Contains(query, "projection_generation:="+strconv.Quote(row.Generation)) {
							continue
						}
						if strings.Contains(query, "log_source_id:=") && !strings.Contains(query, "log_source_id:="+strconv.Quote(row.Source)) {
							continue
						}
						if limit > 0 && count >= limit {
							break
						}
						_, _ = io.WriteString(w, line+"\n")
						count++
					}
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
			require.NoError(t, err)
			sources := []ingest.SourceConfig{}
			for _, id := range []string{"inst:first", "inst:later"} {
				path := filepath.Join(root, strings.ReplaceAll(id, ":", "-")+".log")
				require.NoError(t, os.WriteFile(path, []byte(body), 0600))
				sources = append(sources, ingest.SourceConfig{LogSourceID: id, SourceGeneration: "g1", Path: path, StorageNamespace: id, UTCDay: time.Now().UTC().Format("2006-01-02")})
			}
			m, err := ingest.New(ingest.Options{Root: root, VL: client, Catalog: catalog.New(catalog.NewMemJournal()), Sources: sources[:1], Sampling: cfg.SamplingPolicy(), VerificationTimeout: 200 * time.Millisecond, Reconcile: &ingest.ReconcileConfig{Enabled: false}, ReconcileLoop: &ingest.ReconcileLoopConfig{Enabled: false}, MultilineUnclosedTimeout: time.Millisecond})
			require.NoError(t, err)
			t.Cleanup(func() { _ = m.Stop() })
			require.NoError(t, m.WaitStartupRecovery(context.Background()))
			require.NoError(t, m.Register(sources[1]), "后登记源必须沿用策略但创建独立pipeline")
			ctx, cancel := context.WithCancel(context.Background())
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
			capturedErrors := append([]string(nil), walErrors...)
			mu.Unlock()
			require.Empty(t, capturedErrors, "汇总事件必须先WAL再投递")
			store, err := eventstore.Open(filepath.Join(root, "var", "log", "events"))
			require.NoError(t, err)
			defer store.Close()
			expectedEvents := make(map[string][]logtypes.Event)
			for _, src := range sources {
				var events []logtypes.Event
				require.NoError(t, store.Iterate(src.LogSourceID+"/g1", func(ev logtypes.Event) error { events = append(events, ev); return nil }))
				expectedEvents[src.LogSourceID] = events
				require.Len(t, events, tc.expected, "关闭恒等/开启折叠必须走真实管线")
				if tc.name == "每源独立预算" {
					require.Empty(t, events[0].Fields[sampling.FieldRule], "两个源的首条必须各有独立原文额度，不能共享Sampler预算")
				}
				var next uint64
				suppressed := 0
				for _, ev := range events {
					require.Equal(t, next, ev.Record.Start, "汇总不得造位置空洞")
					next = ev.Record.End
					require.Equal(t, logtypes.EventID(ev.Source, ev.Record), ev.EventID)
					if ev.Fields[sampling.FieldRule] != "" {
						suppressed++
					}
					found := false
					for _, line := range captured {
						var row struct {
							ID   string `json:"event_id"`
							Hash string `json:"canonical_content_hash"`
						}
						_ = json.Unmarshal([]byte(line), &row)
						if row.ID == ev.EventID && row.Hash == ev.CanonicalHash {
							found = true
							break
						}
					}
					require.True(t, found, "canonical身份必须实际投递")
				}
				require.EqualValues(t, len(body), next, "消费范围必须守恒")
				if enabled {
					require.Positive(t, suppressed)
				} else {
					require.Zero(t, suppressed)
				}
			}
			if tc.name == "开启汇总" {
				// 模拟投影丢失，重启后按更严策略恢复；canonical 汇总不能被重新采样。
				mu.Lock()
				rows = nil
				checkWAL = false // 已回收 WAL 的重放责任来自 canonical，不是新采集。
				mu.Unlock()
				cfg.LogIngest.Sampling.MinLevel = "ERROR"
				restarted, err := ingest.New(ingest.Options{Root: root, VL: client, Catalog: catalog.New(catalog.NewMemJournal()),
					Sampling: cfg.SamplingPolicy(), VerificationTimeout: 200 * time.Millisecond,
					MultilineUnclosedTimeout: time.Millisecond})
				require.NoError(t, err)
				t.Cleanup(func() { _ = restarted.Stop() })
				waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
				require.NoError(t, restarted.WaitStartupRecovery(waitCtx))
				waitCancel()
				require.NoError(t, restarted.Stop())
				mu.Lock()
				replayed := append([]string(nil), rows...)
				mu.Unlock()
				require.NotEmpty(t, replayed, "删除旧投影后必须确实触发重放")
				for _, events := range expectedEvents {
					for _, ev := range events {
						found := false
						for _, line := range replayed {
							var row struct {
								ID      string `json:"event_id"`
								Hash    string `json:"canonical_content_hash"`
								Message string `json:"_msg"`
							}
							_ = json.Unmarshal([]byte(line), &row)
							if row.ID == ev.EventID && row.Hash == ev.CanonicalHash && row.Message == ev.Message {
								found = true
								break
							}
						}
						require.True(t, found, "canonical 重投不得按新 ERROR 策略二次折叠或改变身份/正文")
					}
				}
			}
		})
	}
}
