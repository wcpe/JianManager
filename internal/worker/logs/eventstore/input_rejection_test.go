package eventstore

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func sectionTreeFingerprint(t *testing.T, root string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		out[entry.Name()+"/"] = "目录"
		files, err := os.ReadDir(filepath.Join(root, entry.Name()))
		require.NoError(t, err)
		for _, file := range files {
			body, err := os.ReadFile(filepath.Join(root, entry.Name(), file.Name()))
			require.NoError(t, err)
			out[filepath.Join(entry.Name(), file.Name())] = fmt.Sprintf("%d:%x", len(body), sha256.Sum256(body))
		}
	}
	return out
}

func TestInputRejectionPrecedesAnySegmentMutationAndSurvivesThreeReopens(t *testing.T) {
	for _, stage := range []string{"first_write", "sealed_rollover", "active_tail"} {
		for _, position := range []string{"first_event", "middle_event"} {
			for _, encoding := range []string{"raw_oversize", "escaped_oversize"} {
				t.Run(stage+"/"+position+"/"+encoding, func(t *testing.T) {
					root := t.TempDir()
					key := "inst:1/file/g1"
					store, err := Open(root)
					require.NoError(t, err)
					t.Cleanup(func() { _ = store.Close() })
					var wantIDs []string
					if stage == "sealed_rollover" {
						store.SetMaxSegmentBytes(1)
					}
					if stage != "first_write" {
						require.NoError(t, store.Append(key, []logtypes.Event{ev(0)}))
						wantIDs = append(wantIDs, ev(0).EventID)
					}
					beforeTree := sectionTreeFingerprint(t, root)
					beforeMeta := store.Segments(key)
					invalid := ev(777)
					invalid.Message = strings.Repeat("x", maxEventLineBytes)
					if encoding == "escaped_oversize" {
						invalid.Message = strings.Repeat("\x00", maxEventLineBytes/6)
					}
					batch := []logtypes.Event{invalid}
					if position == "middle_event" {
						prefix := ev(700)
						// 让原缺陷确实可能越过写缓冲，不能只验证没有刷到磁盘的小前缀。
						prefix.Message = strings.Repeat("p", 300<<10)
						batch = []logtypes.Event{prefix, invalid, ev(701)}
					}
					err = store.Append(key, batch)
					require.Error(t, err)
					t.Logf("阶段=%s 拒绝位置=%s 编码=%s 错误=%v", stage, position, encoding, err)
					assert.Equal(t, beforeTree, sectionTreeFingerprint(t, root), "纯输入拒绝不得创建、截断或改写任何段与清单")
					assert.Equal(t, beforeMeta, store.Segments(key))
					if sec := store.lookup(key); sec != nil {
						assert.Nil(t, sec.writeErr, "纯输入拒绝不是未决IO，不应焊死源写入")
					}
					if stage == "first_write" {
						assert.Nil(t, store.lookup(key), "未通过全批预检不得登记新源目录")
					}
					assert.NoError(t, store.Append(key, []logtypes.Event{ev(2)}), "同一存储应仍能追加后续合法事件")
					wantIDs = append(wantIDs, ev(2).EventID)
					require.NoError(t, store.Close())
					for round := 1; round <= 3; round++ {
						reopened, err := Open(root)
						if !assert.NoError(t, err, "拒绝输入后第%d次重开必须正常", round) {
							break
						}
						var gotIDs []string
						require.NoError(t, reopened.Iterate(key, func(event logtypes.Event) error {
							gotIDs = append(gotIDs, event.EventID)
							return nil
						}))
						assert.Equal(t, wantIDs, gotIDs, "拒绝批次内的合法前缀也不能部分进入权威集合")
						require.NoError(t, reopened.Close())
					}
				})
			}
		}
	}
}

func TestAppendAcceptsExactEncodedLineLimit(t *testing.T) {
	store, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	event := ev(1)
	event.Message = ""
	base, err := json.Marshal(event)
	require.NoError(t, err)
	event.Message = strings.Repeat("x", maxEventLineBytes-len(base)-1)
	encoded, err := json.Marshal(event)
	require.NoError(t, err)
	require.Equal(t, maxEventLineBytes, len(encoded)+1)
	require.NoError(t, store.Append("inst:1/file/g1", []logtypes.Event{event}))
	got := collect(t, store, "inst:1/file/g1")
	require.Len(t, got, 1)
	require.Equal(t, event.Message, got[0].Message)
}

// 此基准仅比较每批一千条事件的编码与预检成本，不含文件写入、耐久同步或节点级吞吐。
func BenchmarkAppendValidation(b *testing.B) {
	events := make([]logtypes.Event, 1000)
	var encodedBytes int64
	for i := range events {
		events[i] = ev(i)
		events[i].Message = strings.Repeat("x", 512)
		events[i].Fields = map[string]string{"kind": "synthetic", "suite": "preflight"}
		data, err := json.Marshal(events[i])
		if err != nil {
			b.Fatal(err)
		}
		encodedBytes += int64(len(data) + 1)
	}
	marshalAll := func() {
		for _, event := range events {
			data, err := json.Marshal(event)
			if err != nil || len(data)+1 > maxEventLineBytes {
				b.Fatal("编码样本失败", err)
			}
		}
	}
	b.Run("marshal_once", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(encodedBytes)
		for i := 0; i < b.N; i++ {
			marshalAll()
		}
	})
	b.Run("preflight_only", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(encodedBytes)
		for i := 0; i < b.N; i++ {
			if err := validateAppendEvents(events); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("preflight_and_marshal", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(encodedBytes)
		for i := 0; i < b.N; i++ {
			if err := validateAppendEvents(events); err != nil {
				b.Fatal(err)
			}
			marshalAll()
		}
	})
	invalid := ev(1)
	invalid.Message = strings.Repeat("x", maxEventLineBytes)
	b.Run("raw_guard_reject", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := validateAppendEvents([]logtypes.Event{invalid}); err == nil {
				b.Fatal("超限样本未被拒绝")
			}
		}
	})
}
