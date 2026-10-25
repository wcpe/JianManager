package eventstore

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 最后一个段含有中间坏行时，不能把其后的完整数据当成不存在。
func TestReview03LastSegmentMiddleCorruptionMustFailLoud(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	require.NoError(t, err)
	key := "inst:1/g1"
	require.NoError(t, s.Append(key, []logtypes.Event{ev(1), ev(2), ev(3)}))
	require.NoError(t, s.Close())
	path := filepath.Join(root, sectionDirName(key), s.Segments(key)[0].Name)
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := bytes.Split(body, []byte{'\n'})
	lines[1] = []byte(`{"message":故意损坏}`)
	require.NoError(t, os.WriteFile(path, bytes.Join(lines, []byte{'\n'}), 0o600))
	reopened, err := Open(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	got := 0
	err = reopened.Iterate(key, func(logtypes.Event) error { got++; return nil })
	t.Logf("三条完整行中第二条损坏: Iterate返回%d条 err=%v", got, err)
	require.ErrorIs(t, err, ErrCorrupt, "完整坏行不是尾部半行，必须响亮失败而非静默截掉后续事件")
}

func TestReview03MissingManifestMustNotHideDurableSegments(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	require.NoError(t, err)
	key := "inst:1/g1"
	require.NoError(t, s.Append(key, []logtypes.Event{ev(1)}))
	require.NoError(t, s.Close())
	require.NoError(t, os.Remove(filepath.Join(root, sectionDirName(key), manifestName)))
	reopened, err := Open(root)
	if reopened != nil {
		t.Cleanup(func() { _ = reopened.Close() })
	}
	if err == nil {
		n, countErr := reopened.Count(key)
		t.Logf("段仍存在且完整，但manifest缺失: Open err=nil Count=%d CountErr=%v", n, countErr)
	}
	require.Error(t, err, "存在段但manifest缺失不得当作空源")
}

func TestReview03SourceDirectoryNamesMustNotCollide(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	require.NoError(t, err)
	a, b := "node:a-b/g1", "node-a:b/g1"
	require.NoError(t, s.Append(a, []logtypes.Event{ev(1)}))
	require.NoError(t, s.Append(b, []logtypes.Event{ev(2)}))
	require.NoError(t, s.Close())
	reopened, err := Open(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	nA, eA := reopened.Count(a)
	nB, eB := reopened.Count(b)
	t.Logf("不同源key:%q / %q 同目录:%q 重启后CountA=%d(%v) CountB=%d(%v)", a, b, sectionDirName(a), nA, eA, nB, eB)
	require.Equal(t, 1, nA, "不同源键的manifest不可互相覆盖；实际注册可达性由独立集成测试验证")
	require.Equal(t, 1, nB)
}

// 写侧与读侧只通过正式接口并发，不注入任何变异。
func TestReview03ConcurrentIterateAppendSnapshot(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.SetMaxSegmentBytes(1024)
	key := "inst:1/g1"
	require.NoError(t, s.Append(key, []logtypes.Event{ev(0)}))
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 1; i <= 120; i++ {
			if err := s.Append(key, []logtypes.Event{ev(i)}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 120; i++ {
			if err := s.Iterate(key, func(logtypes.Event) error { return nil }); err != nil {
				t.Error(fmt.Errorf("读写并发: %w", err))
				return
			}
		}
	}()
	close(start)
	wg.Wait()
}
