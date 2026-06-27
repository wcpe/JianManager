package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// writePID 在目录写一份 <uuid>.pid 记录，PID 取当前进程（必然存活）或自定义死 PID。
func writePID(t *testing.T, dir, uuid string, wrapperPID int) {
	t.Helper()
	pf := daemon.NewPIDFile(daemon.PIDFileName(dir, uuid))
	require.NoError(t, pf.WriteRecord(daemon.PIDRecord{
		WrapperPID:   wrapperPID,
		JavaPID:      wrapperPID,
		SocketAddr:   daemon.SocketAddr(dir, uuid),
		InstanceUUID: uuid,
		WorkDir:      filepath.Join(dir, "work-"+uuid),
	}))
}

// TestResolvePIDDir_FlagWins --pid-dir 显式指定优先级最高。
func TestResolvePIDDir_FlagWins(t *testing.T) {
	t.Setenv(dataroot.EnvVar, filepath.Join(t.TempDir(), "envroot"))
	flagDir := t.TempDir()
	got, err := resolvePIDDir(flagDir)
	require.NoError(t, err)
	assert.Equal(t, flagDir, got)
}

// TestResolvePIDDir_EnvDataRoot 次优先：JIANMANAGER_DATA_DIR 下 var/servers（与 Worker 实际写入路径对齐）。
func TestResolvePIDDir_EnvDataRoot(t *testing.T) {
	root := t.TempDir()
	// 造出 Worker 实际会写 PID 的目录 <root>/var/servers，并放一个 pid 文件，使其可被发现。
	serversDir := filepath.Join(root, "var", "servers")
	require.NoError(t, os.MkdirAll(serversDir, 0o755))
	t.Setenv(dataroot.EnvVar, root)

	got, err := resolvePIDDir("")
	require.NoError(t, err)
	assert.Equal(t, serversDir, got)
}

// TestResolvePIDDir_NotFound 无 flag、无 env、默认 ./data/var/servers 不存在时报错提示用 --pid-dir。
func TestResolvePIDDir_NotFound(t *testing.T) {
	// 把工作目录切到一个空临时目录，使默认 ./data/var/servers 不存在。
	dir := t.TempDir()
	chdir(t, dir)
	t.Setenv(dataroot.EnvVar, "")

	_, err := resolvePIDDir("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--pid-dir")
}

// TestResolvePIDDir_DefaultDataDir 无 flag、无 env，但默认 ./data/var/servers 存在时采用之。
func TestResolvePIDDir_DefaultDataDir(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	t.Setenv(dataroot.EnvVar, "")
	def := filepath.Join(dir, "data", "var", "servers")
	require.NoError(t, os.MkdirAll(def, 0o755))

	got, err := resolvePIDDir("")
	require.NoError(t, err)
	assert.Equal(t, def, got)
}

// TestScanInstances_AliveOnly 扫描 pidDir：存活实例返回、死实例标 alive=false。
func TestScanInstances_AliveOnly(t *testing.T) {
	dir := t.TempDir()
	writePID(t, dir, "aaaa1111-2222-3333-4444-555566667777", os.Getpid()) // 存活
	writePID(t, dir, "bbbb1111-2222-3333-4444-555566667777", 99999999)    // 不存在 PID

	insts, err := scanInstances(dir)
	require.NoError(t, err)
	require.Len(t, insts, 2)

	byUUID := map[string]instanceInfo{}
	for _, in := range insts {
		byUUID[in.UUID] = in
	}
	assert.True(t, byUUID["aaaa1111-2222-3333-4444-555566667777"].Alive)
	assert.False(t, byUUID["bbbb1111-2222-3333-4444-555566667777"].Alive)
	assert.Equal(t, os.Getpid(), byUUID["aaaa1111-2222-3333-4444-555566667777"].WrapperPID)
}

// TestScanInstances_IgnoresNonPID 非 .pid 文件不计入。
func TestScanInstances_IgnoresNonPID(t *testing.T) {
	dir := t.TempDir()
	writePID(t, dir, "aaaa1111-2222-3333-4444-555566667777", os.Getpid())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "noise.sock"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("x"), 0o644))

	insts, err := scanInstances(dir)
	require.NoError(t, err)
	require.Len(t, insts, 1)
	assert.Equal(t, "aaaa1111-2222-3333-4444-555566667777", insts[0].UUID)
}

// TestResolvePrefix_UniqueMatch 唯一前缀命中返回该实例。
func TestResolvePrefix_UniqueMatch(t *testing.T) {
	insts := []instanceInfo{
		{UUID: "aaaa1111-2222", Alive: true},
		{UUID: "bbbb2222-3333", Alive: true},
	}
	got, err := resolvePrefix(insts, "aa")
	require.NoError(t, err)
	assert.Equal(t, "aaaa1111-2222", got.UUID)
}

// TestResolvePrefix_FullUUID 完整 UUID 也命中（前缀==自身）。
func TestResolvePrefix_FullUUID(t *testing.T) {
	insts := []instanceInfo{{UUID: "aaaa1111-2222", Alive: true}}
	got, err := resolvePrefix(insts, "aaaa1111-2222")
	require.NoError(t, err)
	assert.Equal(t, "aaaa1111-2222", got.UUID)
}

// TestResolvePrefix_Ambiguous 多个匹配报错并列出候选。
func TestResolvePrefix_Ambiguous(t *testing.T) {
	insts := []instanceInfo{
		{UUID: "aaaa1111", Alive: true},
		{UUID: "aaaa2222", Alive: true},
	}
	_, err := resolvePrefix(insts, "aaaa")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aaaa1111")
	assert.Contains(t, err.Error(), "aaaa2222")
}

// TestResolvePrefix_NoMatch 无匹配报错。
func TestResolvePrefix_NoMatch(t *testing.T) {
	insts := []instanceInfo{{UUID: "aaaa1111", Alive: true}}
	_, err := resolvePrefix(insts, "zzzz")
	require.Error(t, err)
}

// TestResolvePrefix_Empty 空前缀报错（避免误选）。
func TestResolvePrefix_Empty(t *testing.T) {
	insts := []instanceInfo{{UUID: "aaaa1111", Alive: true}}
	_, err := resolvePrefix(insts, "")
	require.Error(t, err)
}

// chdir 切换工作目录并在测试结束后还原。
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(old) })
}
