package daemon

import (
	"os"
	"path/filepath"
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
