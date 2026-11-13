package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPIDFile_WriteReadRecord 验证 PID 记录 JSON 往返。
func TestPIDFile_WriteReadRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inst.pid")
	pf := NewPIDFile(path)

	rec := PIDRecord{
		WrapperPID:   12345,
		JavaPID:      67890,
		SocketAddr:   "/tmp/inst.sock",
		InstanceUUID: "uuid-abc",
		WorkDir:      "/srv/inst",
		ProbePort:    29940,
	}
	require.NoError(t, pf.WriteRecord(rec))

	got, err := pf.ReadRecord()
	require.NoError(t, err)
	assert.Equal(t, rec, *got)
}

// TestPIDFile_ReadLegacyPID 兼容旧版裸 PID 数字格式。
func TestPIDFile_ReadLegacyPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.pid")
	require.NoError(t, os.WriteFile(path, []byte("9999"), 0644))

	pf := NewPIDFile(path)
	got, err := pf.ReadRecord()
	require.NoError(t, err)
	assert.Equal(t, 9999, got.WrapperPID)
	assert.Equal(t, 0, got.JavaPID)
}

// TestPIDFile_IsProcessAlive_Nonexistent 不存在的 PID 应判为非存活。
func TestPIDFile_IsProcessAlive_Nonexistent(t *testing.T) {
	// 99999999 几乎必然不存在
	assert.False(t, IsPIDAlive(99999999))
}

// TestPIDFile_IsProcessAlive_Current 当前进程必然存活。
func TestPIDFile_IsProcessAlive_Current(t *testing.T) {
	assert.True(t, IsPIDAlive(os.Getpid()))
}

// TestPIDFile_Remove 删除后读取失败。
func TestPIDFile_Remove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inst.pid")
	pf := NewPIDFile(path)
	require.NoError(t, pf.WriteRecord(PIDRecord{WrapperPID: 1}))

	require.NoError(t, pf.Remove())
	_, err := pf.ReadRecord()
	assert.Error(t, err)

	// 重复删除不报错
	assert.NoError(t, pf.Remove())
}

// TestPIDFile_WriteRecord_Atomic_ReaderNeverSeesPartial 锁定「PID 文件必须原子写」。
//
// 真实故障形态：wrapper 会写两次 PID 文件（监听就绪写 wrapper_pid+socket，Java 拉起后补写
// java_pid，见 wrapper.go）。旧实现用 os.WriteFile（截断→写入），中间存在空文件/半截 JSON 的窗口。
// 并发读取方（Worker 重启时的 RecoverDaemonInstances）恰好撞上这个窗口就会 ReadRecord 失败，
// 而它对解析失败的处理是 **删除 PID 文件并跳过**——一个正在服务的实例被静默移出账本。
//
// 本用例用「持续写 + 持续读」把该窗口放大：修复前（非原子写）必能读到失败；
// 修复后（临时文件 + rename）读者只会看到完整的旧记录或新记录。
// 若日后有人把 writeFileAtomic改回 os.WriteFile，本用例会立刻变红。
func TestPIDFile_WriteRecord_Atomic_ReaderNeverSeesPartial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inst.pid")
	pf := NewPIDFile(path)

	// 先写一份完整记录，确保一直有一份可读的旧内容。
	require.NoError(t, pf.WriteRecord(PIDRecord{WrapperPID: 1000, InstanceUUID: "uuid-atomic"}))

	const rounds = 300
	stop := make(chan struct{})
	var wg sync.WaitGroup

	// 写侧：模拟 wrapper 反复补写（java_pid 变化）。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			_ = pf.WriteRecord(PIDRecord{
				WrapperPID:   1000,
				JavaPID:      2000 + i,
				InstanceUUID: "uuid-atomic",
			})
		}
		close(stop)
	}()

	// 读侧：持续读取，任何一次解析失败都说明存在非原子窗口。
	var readErrs []error
	var mu sync.Mutex
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if rec, err := pf.ReadRecord(); err != nil {
				mu.Lock()
				readErrs = append(readErrs, err)
				mu.Unlock()
			} else if rec.InstanceUUID != "uuid-atomic" {
				mu.Lock()
				readErrs = append(readErrs, fmt.Errorf("读到不完整记录: %+v", rec))
				mu.Unlock()
			}
		}
	}()
	wg.Wait()

	assert.Empty(t, readErrs, "原子写下并发读取不应出现任何解析失败或半截记录")

	// 也不应在 PID 目录里留下临时文件垃圾（该目录会被反复扫描）。
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp", "原子写的临时文件必须已清理")
	}
}
