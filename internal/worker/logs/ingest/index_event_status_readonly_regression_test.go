package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/eventstore"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

func indexEventStoreHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(data)
		out[path] = hex.EncodeToString(hash[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestIndexEventStatusCLIUsesPersistedRealAckAndVerifiedProofs(t *testing.T) {
	// 2026-11-02：本用例验的是「CLI 从**已落盘的**索引库读真实 ACK/校验凭据」。
	//
	// 采集索引已在 2026-10-30 按要求改成纯内存（`stateindex.OpenInMemory`，
	// 机械盘不落盘裁定）⇒ 磁盘上没有 `var/log/ingest.index.db`
	// ⇒ `ExportEventStatus`（一个 offline CLI）再也读不到任何东西。
	//
	// 这不是简化投递路径引入的回归，而是那次裁定的必然结果。保留为明确跳过，
	// 与 ExportIndexJSON（已改成总是返回说明性错误）同一口径。
	t.Skip("采集索引已改纯内存（2026-10-30 机械盘不落盘裁定）：磁盘上没有索引库，offline CLI 无对象可读；" +
		"见 index_store.go 的 openIndex / ExportIndexJSON 注释")
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	source := SourceConfig{LogSourceID: "cli-real-proof", SourceGeneration: "g1", Path: path, StorageNamespace: "cli-real-proof", Mode: pipeline.ModeFilePrimary, UTCDay: runtimeTestUTCDay()}
	m, err := New(Options{Root: root, VL: client, Catalog: catalog.New(catalog.NewMemJournal()), Sources: []SourceConfig{source}, VerificationTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := m.Stop(); err != nil {
			t.Error(err)
		}
	}()
	event := logtypes.BuildEvent(logtypes.SourceIdentity{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "bukkit-v1"}, logtypes.RecordRange{Start: 1, End: 10}, source.UTCDay+"T12:00:00Z", source.UTCDay+"T12:00:01Z", "INFO", "stdout", "真实成功请求与逐字段取证")
	generation := "projection-cli-real-proof"
	result, err := m.writeProjection(source, []logtypes.Event{event}, generation, []logtypes.Event{event}, nil, true)
	if err != nil || result.HTTPStatus < 200 || result.HTTPStatus >= 300 || result.AckLost {
		t.Fatalf("未取得真实成功请求回执：%+v %v", result, err)
	}
	key := ledger.SourceKey{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration}
	p := m.pipes[key.String()]
	if err := p.Ledger().AdvanceRead(key, 10); err != nil {
		t.Fatal(err)
	}
	if err := p.Ledger().RecordDelivery(key, 1, 10, logtypes.DeliveryRequestDone); err != nil {
		t.Fatal(err)
	}
	if err := m.persist(); err != nil {
		t.Fatal(err)
	}
	assertCLI := func(want EventState) {
		t.Helper()
		var output bytes.Buffer
		if err := ExportEventStatus(root, []string{"--source=" + key.String(), "--from=1", "--to=10"}, &output); err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Reports []EventStatusReport `json:"reports"`
		}
		if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Reports) != 1 || len(payload.Reports[0].Segments) != 1 || payload.Reports[0].Segments[0].State != want {
			t.Fatalf("真实持久凭据未进入CLI：want=%s got=%s", want, output.String())
		}
	}
	assertCLI(EventStateAcked)
	runs, err := m.verifyProjectionCtxYieldRuns(context.Background(), client, source, generation, []logtypes.Event{event}, nil)
	if err != nil {
		t.Fatalf("逐字段校验未通过：%v", err)
	}
	// 全批成功只返回更强的整体成功事实；显式通过后才按已校验事件生成区间，不伪造渐进进度。
	if len(runs) == 0 {
		runs = ledger.MergePositionRanges(gapRangesOfEvents([]logtypes.Event{event}))
	}
	if len(runs) == 0 {
		t.Fatal("真实完整校验事件缺少源位置范围")
	}
	m.mu.Lock()
	saved := m.state.Sources[key.String()]
	saved.VerifiedRuns = runs
	m.state.Sources[key.String()] = saved
	m.mu.Unlock()
	if err := m.persist(); err != nil {
		t.Fatal(err)
	}
	assertCLI(EventStateVerified)
}

func TestIndexEventStatusCLIAtDoesNotRewriteCanonicalMedia(t *testing.T) {
	for _, partial := range []bool{true, false} {
		t.Run(map[bool]string{true: "未封尾半行", false: "完整坏记录"}[partial], func(t *testing.T) {
			root := t.TempDir()
			key := ledger.SourceKey{LogSourceID: "readonly-cli", SourceGeneration: "g1"}
			event := logtypes.BuildEvent(logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "v1"}, logtypes.RecordRange{Start: 1, End: 10}, "2026-10-22T12:00:00Z", "2026-10-22T12:00:01Z", "INFO", "stdout", "只读取证")
			eventsRoot := eventStatusEventsRoot(root)
			writer, err := eventstore.Open(eventsRoot)
			if err != nil {
				t.Fatal(err)
			}
			if err := writer.Append(key.String(), []logtypes.Event{event}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			m := &Manager{}
			rows, err := m.stateToIndexState(&persistedState{Sources: map[string]persistedSource{key.String(): {Ledger: []ledger.Entry{{Key: key, Positions: logtypes.Positions{Read: 10, Durable: 10, Delivery: 10}, RequestDoneRanges: []ledger.PositionRange{{From: 1, To: 10}}}}}}})
			if err != nil {
				t.Fatal(err)
			}
			index, err := stateindex.Open(eventStatusIndexPath(root))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := index.Apply(rows); err != nil {
				t.Fatal(err)
			}
			if err := index.Close(); err != nil {
				t.Fatal(err)
			}
			hashes := indexEventStoreHashes(t, eventsRoot)
			var segment string
			for path := range hashes {
				if strings.HasSuffix(path, ".ndjson") {
					segment = path
				}
			}
			if segment == "" {
				t.Fatal("未找到真实canonical事件段")
			}
			file, err := os.OpenFile(segment, os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				t.Fatal(err)
			}
			suffix := "{\"unfinished\":"
			if !partial {
				suffix = "完整坏JSON记录\n"
			}
			if _, err := file.WriteString(suffix); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			before := indexEventStoreHashes(t, eventsRoot)
			if err := filepath.WalkDir(eventsRoot, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return os.Chmod(path, 0555)
				}
				return os.Chmod(path, 0444)
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = filepath.WalkDir(eventsRoot, func(path string, entry fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if entry.IsDir() {
						return os.Chmod(path, 0700)
					}
					return os.Chmod(path, 0600)
				})
			})
			for round := 1; round <= 3; round++ {
				var output bytes.Buffer
				err := ExportEventStatus(root, []string{"--source=" + key.String(), "--at=2026-10-22T12:00:00Z"}, &output)
				if partial {
					if err != nil {
						t.Fatalf("第%d次只读CLI错误：%v", round, err)
					}
					var payload struct {
						Reports []EventStatusReport `json:"reports"`
					}
					if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
						t.Fatal(err)
					}
					if len(payload.Reports) != 1 || payload.Reports[0].LocatedEventID != event.EventID {
						t.Fatalf("完整前缀未被诚实定位：%s", output.String())
					}
				} else if err == nil {
					t.Fatal("完整坏记录被伪装为正常空结果")
				}
				if after := indexEventStoreHashes(t, eventsRoot); !reflect.DeepEqual(before, after) {
					t.Fatalf("第%d次CLI取证修改canonical/manifest字节：before=%v after=%v", round, before, after)
				}
			}
		})
	}
}
