package grpc

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// newImportTestServer 构造带数据根的 Server（导入 RPC 测试通用）。
func newImportTestServer(t *testing.T) (*Server, *dataroot.Root) {
	t.Helper()
	root, err := dataroot.Init(t.TempDir())
	require.NoError(t, err)
	return NewServer(process.NewManager(t.TempDir()), "import-node", nil, nil, root), root
}

// writeJar 生成一个真 zip 结构的 jar；mainClass 非空时写入 MANIFEST Main-Class。
func writeJar(t *testing.T, path, mainClass string, padding int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path)
	require.NoError(t, err)
	zw := zip.NewWriter(f)
	if mainClass != "" {
		w, err := zw.Create("META-INF/MANIFEST.MF")
		require.NoError(t, err)
		_, err = w.Write([]byte("Manifest-Version: 1.0\r\nMain-Class: " + mainClass + "\r\n\r\n"))
		require.NoError(t, err)
	}
	w, err := zw.Create("pad.bin")
	require.NoError(t, err)
	_, err = w.Write(make([]byte, padding))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
}

// makePaperLayout 布一个典型 paper 服目录：核心 jar + 插件 jar + 配置。
func makePaperLayout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeJar(t, filepath.Join(dir, "paper-1.20.4-497.jar"), "io.papermc.paperclip.Main", 16)
	writeJar(t, filepath.Join(dir, "zzz-tool.jar"), "", 8)
	writeJar(t, filepath.Join(dir, "plugins", "Essentials.jar"), "", 8)
	// 深度 3：不应出现在候选中。
	writeJar(t, filepath.Join(dir, "plugins", "sub", "deep.jar"), "", 8)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "server.properties"),
		[]byte("#comment\nmotd=hello\nserver-port=25577\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "eula.txt"),
		[]byte("#By agreeing...\neula=TRUE\n"), 0o644))
	return dir
}

// ── InspectServerDir 守卫 ─────────────────────────────────────────────

func TestInspectServerDir_GuardRejectsBadPaths(t *testing.T) {
	srv, root := newImportTestServer(t)

	// 已有实例目录（托管区内）——防重复收编。
	managed := filepath.Join(root.ServersDir(), "lobby-abc12345")
	require.NoError(t, os.MkdirAll(managed, 0o755))
	// 托管区祖先（数据根）——防搬迁自吞。
	ancestor := root.Base()

	cases := []struct {
		name, path, wantErr string
	}{
		{"相对路径", "servers/foo", "绝对路径"},
		{"不存在", filepath.Join(t.TempDir(), "nope"), "无法访问"},
		{"托管区内", managed, "托管区"},
		{"托管区祖先", ancestor, "托管区"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := srv.InspectServerDir(context.Background(), &workerpb.InspectServerDirRequest{Path: tc.path})
			require.NoError(t, err)
			require.False(t, resp.Success)
			assert.Contains(t, resp.Error, tc.wantErr)
		})
	}
}

func TestInspectServerDir_GuardRejectsFile(t *testing.T) {
	srv, _ := newImportTestServer(t)
	file := filepath.Join(t.TempDir(), "a.txt")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	resp, err := srv.InspectServerDir(context.Background(), &workerpb.InspectServerDirRequest{Path: file})
	require.NoError(t, err)
	require.False(t, resp.Success)
	assert.Contains(t, resp.Error, "目录")
}

// ── InspectServerDir 探测 ─────────────────────────────────────────────

// paper 服布局：已知核心名排前、MANIFEST 嗅探标 hint、深度≤2、端口与 eula 解析。
func TestInspectServerDir_PaperLayout(t *testing.T) {
	srv, _ := newImportTestServer(t)
	dir := makePaperLayout(t)

	resp, err := srv.InspectServerDir(context.Background(), &workerpb.InspectServerDirRequest{Path: dir})
	require.NoError(t, err)
	require.True(t, resp.Success, "探测应成功: %s", resp.Error)

	require.Len(t, resp.Jars, 3, "深度≤2 应命中 3 个 jar（deep.jar 除外）")
	assert.Equal(t, "paper-1.20.4-497.jar", resp.Jars[0].Path, "已知核心名必须排最前")
	assert.Equal(t, "io.papermc.paperclip.Main", resp.Jars[0].MainClassHint)
	assert.Positive(t, resp.Jars[0].Size)
	// 根目录 jar 先于子目录 jar。
	assert.Equal(t, "zzz-tool.jar", resp.Jars[1].Path)
	assert.Equal(t, "plugins/Essentials.jar", resp.Jars[2].Path)

	assert.Equal(t, int32(25577), resp.ServerPort)
	assert.True(t, resp.EulaAccepted, "eula=TRUE 应视为已接受")
	assert.True(t, resp.PropsFound)
}

// 无 server.properties：propsFound=false、端口 0、eula=false，不报错。
func TestInspectServerDir_NoProps(t *testing.T) {
	srv, _ := newImportTestServer(t)
	dir := t.TempDir()
	writeJar(t, filepath.Join(dir, "random.jar"), "", 8)

	resp, err := srv.InspectServerDir(context.Background(), &workerpb.InspectServerDirRequest{Path: dir})
	require.NoError(t, err)
	require.True(t, resp.Success)
	assert.False(t, resp.PropsFound)
	assert.Zero(t, resp.ServerPort)
	assert.False(t, resp.EulaAccepted)
}

// 内嵌 JDK：jre*/jdk*/runtime/java 子目录经探测钩子确认后进入候选（嵌套 jre 布局）。
func TestInspectServerDir_JDKCandidates(t *testing.T) {
	srv, _ := newImportTestServer(t)
	dir := t.TempDir()
	for _, d := range []string{"jre-17", "jdk8u402", "runtime", "java", "world"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, d), 0o755))
	}
	// 探测钩子替身：只认 jre-17 与 runtime 是真 JDK（detectAt 需真 java 可执行文件，单测不可得）。
	srv.importJDKProbe = func(home string) (*workerpb.ImportJdkCandidate, bool) {
		switch filepath.Base(home) {
		case "jre-17":
			return &workerpb.ImportJdkCandidate{Path: home, Vendor: "temurin", Version: "17.0.10", MajorVersion: 17, Arch: "x64"}, true
		case "runtime":
			return &workerpb.ImportJdkCandidate{Path: home, Vendor: "zulu", Version: "8.0.402", MajorVersion: 8, Arch: "x64"}, true
		}
		return nil, false
	}

	resp, err := srv.InspectServerDir(context.Background(), &workerpb.InspectServerDirRequest{Path: dir})
	require.NoError(t, err)
	require.True(t, resp.Success)
	require.Len(t, resp.Jdks, 2)
	assert.Equal(t, int32(17), resp.Jdks[0].MajorVersion)
	assert.Equal(t, "temurin", resp.Jdks[0].Vendor)
}

// scanJDKCandidateDirs 纯扫描：只认 jre*/jdk*/runtime/java 命名的一级子目录。
func TestScanJDKCandidateDirs(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"jre", "JDK-21", "runtime", "java", "plugins", "world"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, d), 0o755))
	}
	got := scanJDKCandidateDirs(dir)
	require.Len(t, got, 4)
	for _, g := range got {
		assert.NotContains(t, []string{"plugins", "world"}, filepath.Base(g))
	}
}

// ── ImportServerDir ──────────────────────────────────────────────────

// in_place：no-op 回原路径，目录内容原样。
func TestImportServerDir_InPlace(t *testing.T) {
	srv, _ := newImportTestServer(t)
	dir := makePaperLayout(t)

	resp, err := srv.ImportServerDir(context.Background(), &workerpb.ImportServerDirRequest{Path: dir, Mode: "in_place"})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)
	assert.Equal(t, filepath.Clean(dir), resp.WorkDir)
	assert.False(t, resp.Moved)
	assert.FileExists(t, filepath.Join(dir, "server.properties"), "就地模式不得动原目录")
}

// migrate 同盘：os.Rename 进托管区，源位置清空。
func TestImportServerDir_MigrateRename(t *testing.T) {
	srv, root := newImportTestServer(t)
	src := makePaperLayout(t)

	resp, err := srv.ImportServerDir(context.Background(), &workerpb.ImportServerDirRequest{
		Path: src, Mode: "migrate", TargetSlug: "web-ab12cd34",
	})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)
	assert.True(t, resp.Moved)

	target := filepath.Join(root.ServersDir(), "web-ab12cd34")
	assert.Equal(t, target, resp.WorkDir)
	assert.FileExists(t, filepath.Join(target, "paper-1.20.4-497.jar"))
	assert.FileExists(t, filepath.Join(target, "plugins", "Essentials.jar"))
	assert.NoDirExists(t, src, "同盘搬迁后源位置应清空")
}

// migrate 守卫：非法 slug / 目标已存在 / 未知模式。
func TestImportServerDir_Guards(t *testing.T) {
	srv, root := newImportTestServer(t)
	src := makePaperLayout(t)

	for _, slug := range []string{"", "../escape", "a/b", "a\\b", "UPPER"} {
		resp, err := srv.ImportServerDir(context.Background(), &workerpb.ImportServerDirRequest{
			Path: src, Mode: "migrate", TargetSlug: slug,
		})
		require.NoError(t, err)
		assert.False(t, resp.Success, "非法 slug %q 必须被拒", slug)
	}

	require.NoError(t, os.MkdirAll(filepath.Join(root.ServersDir(), "taken-12345678"), 0o755))
	resp, err := srv.ImportServerDir(context.Background(), &workerpb.ImportServerDirRequest{
		Path: src, Mode: "migrate", TargetSlug: "taken-12345678",
	})
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "已存在")

	resp, err = srv.ImportServerDir(context.Background(), &workerpb.ImportServerDirRequest{Path: src, Mode: "yolo"})
	require.NoError(t, err)
	assert.False(t, resp.Success)

	assert.DirExists(t, src, "守卫拒绝时源目录不得被动过")
}

// 跨盘回退：rename 失败 → 递归拷贝 + 数量/字节校验 + 清源。
func TestMoveDirCopyFallback(t *testing.T) {
	src := makePaperLayout(t)
	dst := filepath.Join(t.TempDir(), "moved")

	renamed, err := moveDirWithFallback(src, dst, func(string, string) error {
		return errors.New("simulated cross-device link")
	})
	require.NoError(t, err)
	assert.False(t, renamed, "回退路径不应报告 rename")
	assert.FileExists(t, filepath.Join(dst, "paper-1.20.4-497.jar"))
	assert.FileExists(t, filepath.Join(dst, "plugins", "sub", "deep.jar"))
	assert.FileExists(t, filepath.Join(dst, "eula.txt"))
	assert.NoDirExists(t, src, "校验通过后必须清源")
}

// 拷贝中途失败：目标半成品清理、源目录保留原样。
func TestMoveDirCopyFailureKeepsSource(t *testing.T) {
	src := makePaperLayout(t)
	// 目标放进一个只存在于 rename 失败后拷贝路径会撞的文件：预置 dst 为已存在文件迫使 MkdirAll 失败。
	dstParent := t.TempDir()
	dst := filepath.Join(dstParent, "occupied")
	require.NoError(t, os.WriteFile(dst, []byte("x"), 0o644))

	_, err := moveDirWithFallback(src, dst, func(string, string) error {
		return errors.New("simulated cross-device link")
	})
	require.Error(t, err)
	assert.DirExists(t, src, "拷贝失败时源目录必须保留")
	assert.FileExists(t, filepath.Join(src, "server.properties"))
}

// ── 就地实例删除保护（FR-302 关键守则，双保险之一）────────────────────

// CP 显式传 skip_work_dir：目录一个字节都不动，仅移除注册。
func TestRemoveInstance_SkipWorkDirKeepsInPlaceDir(t *testing.T) {
	srv, _ := newImportTestServer(t)
	inPlace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(inPlace, "server.jar"), []byte("jar"), 0o644))
	const uuid = "77777777-7777-7777-7777-777777777777"
	require.NoError(t, srv.manager.Create(uuid, "imported", "noop", "", inPlace, nil, false, process.ProcessTypeDirect, "", "", 0, 0))

	resp, err := srv.RemoveInstance(context.Background(), &workerpb.RemoveInstanceRequest{
		InstanceUuid: uuid, WorkDir: inPlace, SkipWorkDir: true,
	})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)
	assert.True(t, resp.WorkDirSkipped)
	assert.Contains(t, resp.SkipReason, "就地")
	assert.FileExists(t, filepath.Join(inPlace, "server.jar"), "就地实例删除绝不删原目录")
	_, ok := srv.manager.GetInstance(uuid)
	assert.False(t, ok, "注册表条目仍应移除")
}

// 双保险之二：即便 skip 标记丢失，托管区守卫仍保住就地目录（锁死既有行为）。
func TestRemoveInstance_ManagedAreaGuardStillProtectsInPlaceDir(t *testing.T) {
	srv, _ := newImportTestServer(t)
	inPlace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(inPlace, "server.jar"), []byte("jar"), 0o644))
	const uuid = "88888888-8888-8888-8888-888888888888"
	require.NoError(t, srv.manager.Create(uuid, "imported", "noop", "", inPlace, nil, false, process.ProcessTypeDirect, "", "", 0, 0))

	resp, err := srv.RemoveInstance(context.Background(), &workerpb.RemoveInstanceRequest{
		InstanceUuid: uuid, WorkDir: inPlace, // 无 SkipWorkDir：模拟标记丢失
	})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)
	assert.True(t, resp.WorkDirSkipped, "托管区外目录必须被守卫拦下")
	assert.FileExists(t, filepath.Join(inPlace, "server.jar"))
}
