package eventstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func writeSyncedEvents(t *testing.T, path string, events ...logtypes.Event) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	for _, event := range events {
		data, err := json.Marshal(event)
		require.NoError(t, err)
		_, err = file.Write(append(data, '\n'))
		require.NoError(t, err)
	}
	require.NoError(t, file.Sync())
	require.NoError(t, file.Close())
}

func legacySectionName(key string) string {
	return strings.NewReplacer("/", "_", "\\", "_", ":", "-").Replace(key)
}

func TestManifestDamageAndUnlistedSegmentsFailLoud(t *testing.T) {
	for _, damage := range []string{"missing", "empty", "malformed", "temporary_only", "missing_key", "unlisted"} {
		t.Run(damage, func(t *testing.T) {
			root := t.TempDir()
			s, err := Open(root)
			require.NoError(t, err)
			key := "inst:1/file/g1"
			require.NoError(t, s.Append(key, []logtypes.Event{ev(1)}))
			require.NoError(t, s.Close())
			dir := filepath.Join(root, sectionDirName(key))
			path := filepath.Join(dir, manifestName)
			switch damage {
			case "missing":
				require.NoError(t, os.Remove(path))
			case "empty":
				require.NoError(t, os.WriteFile(path, nil, 0o600))
			case "malformed":
				require.NoError(t, os.WriteFile(path, []byte(`{"version":`), 0o600))
			case "temporary_only":
				require.NoError(t, os.Rename(path, path+".tmp"))
			case "missing_key":
				data, err := json.Marshal(Manifest{Version: manifestVer, Segments: s.Segments(key)})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, data, 0o600))
			case "unlisted":
				require.NoError(t, os.WriteFile(filepath.Join(dir, "seg-000002.ndjson"), []byte("{}\n"), 0o600))
			}
			_, err = Open(root)
			require.ErrorIs(t, err, ErrCorrupt)
			_, err = os.Stat(filepath.Join(dir, s.Segments(key)[0].Name))
			require.NoError(t, err, "错误不得删除或猜测修复现存段")
		})
	}
}

func TestEmptyDirectoriesDoNotInventSourceIdentity(t *testing.T) {
	root := t.TempDir()
	key := "inst:1/file/g1"
	for _, dirName := range []string{sectionDirName(key), legacySectionName(key)} {
		dir := filepath.Join(root, dirName)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, manifestName+".tmp"), []byte(`{"key":"not-authoritative"}`), 0o600))
	}
	s, err := Open(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.Empty(t, s.byKey, "空目录与临时清单均不能成为源身份")
	require.NoError(t, s.Append(key, []logtypes.Event{ev(1)}))
	require.Len(t, collect(t, s, key), 1)
}

func TestLegacyManifestDirectoryAndKeyValidation(t *testing.T) {
	root := t.TempDir()
	key := "inst:1/a:b/g1"
	s, err := Open(root)
	require.NoError(t, err)
	require.NoError(t, s.Append(key, []logtypes.Event{ev(1)}))
	require.NoError(t, s.Close())
	legacyDir := filepath.Join(root, legacySectionName(key))
	require.NoError(t, os.Rename(filepath.Join(root, sectionDirName(key)), legacyDir))
	newKey := "inst:1/a-b/g1"
	require.Equal(t, legacySectionName(key), legacySectionName(newKey))
	for round := 0; round < 3; round++ {
		reopened, err := Open(root)
		require.NoError(t, err)
		require.Len(t, collect(t, reopened, key), round+1)
		require.Len(t, collect(t, reopened, newKey), round)
		require.NoError(t, reopened.Append(key, []logtypes.Event{ev(round + 2)}))
		require.NoError(t, reopened.Append(newKey, []logtypes.Event{ev(round + 100)}))
		require.Len(t, collect(t, reopened, key), round+2)
		require.Len(t, collect(t, reopened, newKey), round+1)
		require.NoError(t, reopened.Close())
		_, err = os.Stat(filepath.Join(root, sectionDirName(key)))
		require.ErrorIs(t, err, os.ErrNotExist, "旧有效目录必须原位使用，不能批量重命名或制造覆盖副本")
	}
	data, err := os.ReadFile(filepath.Join(legacyDir, manifestName))
	require.NoError(t, err)
	var manifest Manifest
	require.NoError(t, json.Unmarshal(data, &manifest))
	require.Equal(t, key, manifest.Key)

	t.Run("new_digest_mismatch", func(t *testing.T) {
		otherRoot := t.TempDir()
		newDir := filepath.Join(otherRoot, sectionDirName("inst:2/file/g1"))
		require.NoError(t, os.MkdirAll(newDir, 0o700))
		body, err := json.Marshal(Manifest{Version: manifestVer, Key: "inst:3/file/g1"})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(newDir, manifestName), body, 0o600))
		_, err = Open(otherRoot)
		require.ErrorIs(t, err, ErrCorrupt)
	})
	t.Run("duplicate_key", func(t *testing.T) {
		otherRoot := t.TempDir()
		for _, name := range []string{"old-a", "old-b"} {
			dir := filepath.Join(otherRoot, name)
			require.NoError(t, os.MkdirAll(dir, 0o700))
			body, err := json.Marshal(Manifest{Version: manifestVer, Key: key})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, manifestName), body, 0o600))
		}
		_, err := Open(otherRoot)
		require.ErrorIs(t, err, ErrCorrupt)
	})
}

func TestCountIterateAndAppendShareStrictCorruptionRules(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		for _, damage := range []string{"complete_middle", "complete_last", "partial_tail", "oversize_complete", "oversize_partial"} {
			for _, first := range []string{"iterate", "count", "append"} {
				t.Run(fmt.Sprintf("sealed=%t/%s/%s-first", sealed, damage, first), func(t *testing.T) {
					root := t.TempDir()
					key := "inst:1/file/g1"
					s, err := Open(root)
					require.NoError(t, err)
					if sealed {
						s.SetMaxSegmentBytes(1)
					}
					require.NoError(t, s.Append(key, []logtypes.Event{ev(1), ev(2), ev(3)}))
					require.NoError(t, s.Close())
					path := filepath.Join(root, sectionDirName(key), s.Segments(key)[0].Name)
					body, err := os.ReadFile(path)
					require.NoError(t, err)
					lines := bytes.Split(body, []byte{'\n'})
					switch damage {
					case "complete_middle":
						lines[1] = []byte(`{"message":坏行}`)
						body = bytes.Join(lines, []byte{'\n'})
					case "complete_last":
						lines[2] = []byte(`{"message":坏行}`)
						body = bytes.Join(lines, []byte{'\n'})
					case "partial_tail":
						body = body[:len(body)-7]
					case "oversize_complete":
						body = append(body, []byte(strings.Repeat("x", maxEventLineBytes+1)+"\n")...)
					case "oversize_partial":
						body = append(body, []byte(strings.Repeat("x", maxEventLineBytes+1))...)
					}
					require.NoError(t, os.WriteFile(path, body, 0o600))
					reopened, err := Open(root)
					require.NoError(t, err)
					t.Cleanup(func() { _ = reopened.Close() })
					switch first {
					case "iterate":
						err = reopened.Iterate(key, func(logtypes.Event) error { return nil })
					case "count":
						_, err = reopened.Count(key)
					case "append":
						err = reopened.Append(key, []logtypes.Event{ev(4)})
					}
					if damage == "partial_tail" && !sealed {
						require.NoError(t, err)
						want := 2
						if first == "append" {
							want++
						}
						require.Len(t, collect(t, reopened, key), want)
						count, countErr := reopened.Count(key)
						require.NoError(t, countErr)
						require.Equal(t, want, count)
					} else {
						require.ErrorIs(t, err, ErrCorrupt)
						after, readErr := os.ReadFile(path)
						require.NoError(t, readErr)
						require.Equal(t, body, after, "完整坏行、封段或非合法尾部必须保留原物而非截断")
					}
				})
			}
		}
	}
}

func TestCanceledDayReadDoesNotStartCalibrationOrRewriteTail(t *testing.T) {
	root := t.TempDir()
	key := "inst:1/file/g1"
	s, err := Open(root)
	require.NoError(t, err)
	require.NoError(t, s.Append(key, []logtypes.Event{ev(1)}))
	require.NoError(t, s.Close())
	dir := filepath.Join(root, sectionDirName(key))
	path := filepath.Join(dir, s.Segments(key)[0].Name)
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	body = append(body, []byte(`{"message":"未完成`)...)
	require.NoError(t, os.WriteFile(path, body, 0o600))
	reopened, err := Open(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = reopened.DayMaxEndCtx(ctx, key, func(logtypes.Event) (string, error) {
		t.Fatal("已取消请求不能开始事件回调")
		return "", nil
	})
	require.ErrorIs(t, err, context.Canceled)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, body, after, "首次读快照校准也必须遵从取消，不能后台继续扫盘改写")
}

type cancelingReader struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (reader *cancelingReader) Read(dst []byte) (int, error) {
	n, err := reader.reader.Read(dst)
	reader.cancel()
	return n, err
}

func TestCancellationDuringCalibrationScanIsNotCorruption(t *testing.T) {
	body, err := json.Marshal(ev(1))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelingReader{reader: bytes.NewReader(append(body, '\n')), cancel: cancel}
	_, err = scanSegmentContext(ctx, reader, "取消读取段", SegmentMeta{}, true, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, ErrCorrupt)
}

type readerWithFailure struct {
	reader io.Reader
	err    error
}

func (reader *readerWithFailure) Read(dst []byte) (int, error) {
	n, err := reader.reader.Read(dst)
	if err == io.EOF {
		return n, reader.err
	}
	return n, err
}

func TestNonEOFReadFailureNeverBecomesTailRecovery(t *testing.T) {
	failure := errors.New("注入读取故障")
	body, err := json.Marshal(ev(1))
	require.NoError(t, err)
	for _, suffix := range []string{"", "\n", "\n{\"message\":"} {
		for _, allowTail := range []bool{false, true} {
			reader := &readerWithFailure{reader: bytes.NewReader(append(append([]byte(nil), body...), suffix...)), err: failure}
			_, err := scanSegment(reader, "故障注入段", SegmentMeta{}, allowTail, nil)
			require.ErrorIs(t, err, ErrCorrupt)
			require.ErrorIs(t, err, failure)
		}
	}
}

func TestSyncedTailBeforeManifestCommitRecoversAcrossThreeReopens(t *testing.T) {
	root := t.TempDir()
	key := "inst:1/file/g1"
	s, err := Open(root)
	require.NoError(t, err)
	require.NoError(t, s.Append(key, []logtypes.Event{ev(1)}))
	require.NoError(t, s.Close())
	dir := filepath.Join(root, sectionDirName(key))
	path := filepath.Join(dir, s.Segments(key)[0].Name)
	writeSyncedEvents(t, path, ev(2))
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = file.WriteString(`{"message":"崩溃半行`)
	require.NoError(t, err)
	require.NoError(t, file.Sync())
	require.NoError(t, file.Close())
	require.NoError(t, os.WriteFile(filepath.Join(dir, manifestName+".tmp"), []byte(`{"version":999}`), 0o600))
	for round := 0; round < 3; round++ {
		reopened, err := Open(root)
		require.NoError(t, err)
		snapshot, err := reopened.Snapshot(key)
		require.NoError(t, err)
		require.Equal(t, 2, snapshot.Count())
		var got []string
		require.NoError(t, snapshot.Iterate(func(event logtypes.Event) error { got = append(got, event.EventID); return nil }))
		require.Equal(t, []string{ev(1).EventID, ev(2).EventID}, got)
		stat, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, snapshot.Segments()[0].Bytes, stat.Size())
		t.Logf("重开轮次=%d count=%d bytes=%d first=%s last=%s", round+1, snapshot.Count(), stat.Size(), snapshot.Segments()[0].FirstID, snapshot.Segments()[0].LastID)
		require.NoError(t, snapshot.Close())
		require.NoError(t, reopened.Close())
	}
}

func TestFailedManifestPublicationCannotLeakUncommittedMetadata(t *testing.T) {
	root := t.TempDir()
	key := "inst:1/file/g1"
	s, err := Open(root)
	require.NoError(t, err)
	require.NoError(t, s.Append(key, []logtypes.Event{ev(1)}))
	before := s.Segments(key)
	snapshot, err := s.Snapshot(key)
	require.NoError(t, err)
	t.Cleanup(func() { _ = snapshot.Close() })
	blockedTmp := filepath.Join(root, sectionDirName(key), manifestName+".tmp")
	require.NoError(t, os.Mkdir(blockedTmp, 0o700))
	err = s.Append(key, []logtypes.Event{ev(2)})
	require.Error(t, err, "真实追加必须在段写入耐久后遭遇清单发布故障")
	require.Equal(t, before, s.Segments(key))
	count, err := s.Count(key)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Len(t, collect(t, s, key), 1)
	require.Equal(t, 1, snapshot.Count())
	require.NoError(t, os.Remove(blockedTmp))
	require.Error(t, s.Append(key, []logtypes.Event{ev(3)}), "不能在未决批次后盲目追加制造重复")
	require.NoError(t, s.Close())
	for round := 0; round < 3; round++ {
		reopened, err := Open(root)
		require.NoError(t, err)
		got := collect(t, reopened, key)
		require.Len(t, got, 2)
		require.Equal(t, []string{ev(1).EventID, ev(2).EventID}, []string{got[0].EventID, got[1].EventID})
		require.NoError(t, reopened.Close())
	}
}

func TestFirstSyncedSegmentWithoutPublishedReferenceFailsLoudOnReopen(t *testing.T) {
	root := t.TempDir()
	key := "inst:1/file/g1"
	dir := filepath.Join(root, sectionDirName(key))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	body, err := json.Marshal(Manifest{Version: manifestVer, Key: key})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, manifestName), body, 0o600))
	s, err := Open(root)
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(dir, manifestName+".tmp"), 0o700))
	require.Error(t, s.Append(key, []logtypes.Event{ev(1)}))
	count, err := s.Count(key)
	require.NoError(t, err)
	require.Zero(t, count, "未发布首段不能被本进程读侧采纳")
	require.NoError(t, s.Close())
	segmentPath := filepath.Join(dir, "seg-000001.ndjson")
	segment, err := os.ReadFile(segmentPath)
	require.NoError(t, err)
	require.True(t, bytes.HasSuffix(segment, []byte{'\n'}), "真实追加已经写入并耐久完整行")
	for round := 0; round < 3; round++ {
		_, err := Open(root)
		require.ErrorIs(t, err, ErrCorrupt, "清单外首段需要显式取证，不能猜测成空或自动恢复")
		after, err := os.ReadFile(segmentPath)
		require.NoError(t, err)
		require.Equal(t, segment, after)
	}
}

func TestSnapshotFreezesBytesAndAllowsCallbackReentryAndRollover(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	key := "inst:1/file/g1"
	require.NoError(t, s.Append(key, []logtypes.Event{ev(1), ev(2)}))
	snapshot, err := s.Snapshot(key)
	require.NoError(t, err)
	t.Cleanup(func() { _ = snapshot.Close() })
	segs := snapshot.Segments()
	segs[0].Count = 999
	manifest := snapshot.Manifest()
	manifest.Segments[0].Bytes = 0
	require.Equal(t, 2, snapshot.Count())
	require.NotZero(t, snapshot.Segments()[0].Bytes)
	done := make(chan error, 1)
	go func() {
		seen := 0
		err := snapshot.Iterate(func(logtypes.Event) error {
			seen++
			if seen == 1 {
				s.SetMaxSegmentBytes(1)
				if err := s.Append(key, []logtypes.Event{ev(3)}); err != nil {
					return err
				}
				if err := s.Append(key, []logtypes.Event{ev(4)}); err != nil {
					return err
				}
				if err := s.Append("inst:2/file/g1", []logtypes.Event{ev(5)}); err != nil {
					return err
				}
				count, err := s.Count(key)
				if err != nil || count != 4 {
					return fmt.Errorf("回调内计数=%d: %w", count, err)
				}
				if err := s.Iterate(key, func(logtypes.Event) error { return nil }); err != nil {
					return err
				}
			}
			return nil
		})
		if err == nil && seen != 2 {
			err = fmt.Errorf("旧快照读入追加行: %d", seen)
		}
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("回调重入不得被存储或源写锁阻塞")
	}
	require.NoError(t, s.Close())
	count := 0
	require.NoError(t, snapshot.Iterate(func(logtypes.Event) error { count++; return nil }))
	require.Equal(t, 2, count, "存储关闭后仍由快照持有原文件引用")
}

func TestSnapshotCloseWaitsForActualReaderExitWithoutBlockingCallback(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.SetMaxSegmentBytes(1)
	require.NoError(t, s.Append("key", []logtypes.Event{ev(1)}))
	require.NoError(t, s.Append("key", []logtypes.Event{ev(2)}))
	snapshot, err := s.Snapshot("key")
	require.NoError(t, err)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		seen := 0
		err := snapshot.Iterate(func(logtypes.Event) error {
			seen++
			if seen == 1 {
				close(entered)
				<-release
				return snapshot.Close()
			}
			return nil
		})
		if err == nil && seen != 2 {
			err = fmt.Errorf("在途读被提前关闭: %d", seen)
		}
		done <- err
	}()
	<-entered
	require.NoError(t, snapshot.Close())
	require.ErrorIs(t, snapshot.Iterate(nil), os.ErrClosed)
	snapshot.mu.Lock()
	readers, released := snapshot.readers, snapshot.released
	snapshot.mu.Unlock()
	require.Equal(t, 1, readers)
	require.False(t, released, "回调仍在首段，逻辑引用不得提前释放")
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, snapshot.Close())
	require.Zero(t, snapshot.readers)
	require.True(t, snapshot.released, "包括尚未打开的第二段读完后才释放")
}

func TestCalibrationRetryCannotPublishOnlyInMemoryAfterManifestFailure(t *testing.T) {
	root := t.TempDir()
	key := "inst:1/file/g1"
	s, err := Open(root)
	require.NoError(t, err)
	require.NoError(t, s.Append(key, []logtypes.Event{ev(1)}))
	require.NoError(t, s.Close())
	dir := filepath.Join(root, sectionDirName(key))
	writeSyncedEvents(t, filepath.Join(dir, s.Segments(key)[0].Name), ev(2))
	blockedTmp := filepath.Join(dir, manifestName+".tmp")
	require.NoError(t, os.Mkdir(blockedTmp, 0o700))
	reopened, err := Open(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	for attempt := 0; attempt < 2; attempt++ {
		_, err := reopened.Count(key)
		require.Error(t, err, "校准失败后的重试仍必须成功持久发布，不能只在内存采纳候选")
		require.Equal(t, 1, reopened.Segments(key)[0].Count)
	}
	require.NoError(t, os.Remove(blockedTmp))
	count, err := reopened.Count(key)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	body, err := os.ReadFile(filepath.Join(dir, manifestName))
	require.NoError(t, err)
	var manifest Manifest
	require.NoError(t, json.Unmarshal(body, &manifest))
	require.Equal(t, 2, manifest.Segments[0].Count)
}

func TestSnapshotDoesNotWaitForPendingWriterOrExposeWorkingManifest(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	key := "inst:1/file/g1"
	require.NoError(t, s.Append(key, []logtypes.Event{ev(1)}))
	sec := s.lookup(key)
	sec.mu.Lock()
	addEventMeta(&sec.manifest.Segments[0], ev(2))
	sec.manifest.Segments[0].Bytes += 99
	done := make(chan error, 1)
	go func() {
		snapshot, err := s.Snapshot(key)
		if err == nil {
			if snapshot.Count() != 1 {
				err = fmt.Errorf("未提交清单泄漏: count=%d", snapshot.Count())
			} else {
				err = snapshot.Iterate(nil)
			}
			_ = snapshot.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		sec.mu.Unlock()
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		sec.mu.Unlock()
		t.Fatal("读取成功清单不能等待在途慢写锁")
	}
}
