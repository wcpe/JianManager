package grpc

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/decompiler"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// zeroReader 是无限零字节源，供测试造可控大小的 zip 条目。
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// makeDecompileJar 造一个反编译测试 jar：
//   - blobSize>0 时写一个填充条目 blobName：blobMethod=zip.Store 令其磁盘体积≈blobSize（不压缩）；
//     blobMethod=zip.Deflate 且全零 → 磁盘体积极小但解压后=blobSize（zip bomb 造型）。
//   - texts 为若干普通 Deflate 文本条目。
func makeDecompileJar(t *testing.T, path, blobName string, blobMethod uint16, blobSize int, texts map[string]string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	zw := zip.NewWriter(f)
	if blobSize > 0 {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: blobName, Method: blobMethod})
		require.NoError(t, err)
		_, err = io.CopyN(w, zeroReader{}, int64(blobSize))
		require.NoError(t, err)
	}
	for name, content := range texts {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
}

// TestPrepareDecompileTarget_JarEntryUsesEntrySizeNotJarSize 复现 FR-075 缺陷：
// 宿主 jar 远超上限（如 server.jar 43MB），但其中单个 class 仅几字节，
// 单条目反编译不得被整 jar 体积拒绝——否则服务端核心 jar 内 class 永远无法单条目反编译。
func TestPrepareDecompileTarget_JarEntryUsesEntrySizeNotJarSize(t *testing.T) {
	srv := &Server{}
	work := t.TempDir()
	jar := filepath.Join(work, "server.jar")
	over := maxDecompileInputBytes + 1<<20 // 17MB 填充（Store 不压缩），令 jar 磁盘体积超整体上限
	makeDecompileJar(t, jar, "BigBlob.bin", zip.Store, over, map[string]string{
		"io/papermc/paperclip/Main.class": "FAKE-MAIN-CLASS",
	})
	info, err := os.Stat(jar)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(maxDecompileInputBytes), "前置：宿主 jar 须超整体上限，才测得到点")

	target, jarEntry, cleanup, perr := srv.prepareDecompileTarget(jar, "server.jar", "io/papermc/paperclip/Main.class")
	if cleanup != nil {
		defer cleanup()
	}
	require.NoError(t, perr, "大 jar 内小 class 单条目反编译不应被整 jar 体积拒绝")
	require.Empty(t, jarEntry)
	b, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "FAKE-MAIN-CLASS", string(b))
}

// TestPrepareDecompileTarget_OversizeEntryRejected 校验 zip bomb 防护：
// jar 磁盘体积很小（压缩后），但单条目解压后超上限 → 仍须拒绝，不得静默截断。
func TestPrepareDecompileTarget_OversizeEntryRejected(t *testing.T) {
	srv := &Server{}
	work := t.TempDir()
	jar := filepath.Join(work, "bomb.jar")
	makeDecompileJar(t, jar, "Huge.class", zip.Deflate, maxDecompileInputBytes+1<<20, nil) // 17MB 全零，deflate 后极小
	info, err := os.Stat(jar)
	require.NoError(t, err)
	require.Less(t, info.Size(), int64(maxDecompileInputBytes), "前置：jar 磁盘体积须小于上限，才能证明是条目守卫在拒绝")

	_, _, cleanup, perr := srv.prepareDecompileTarget(jar, "bomb.jar", "Huge.class")
	if cleanup != nil {
		defer cleanup()
	}
	require.Error(t, perr, "超大单 class 条目须被拒绝，不得静默截断")
	require.Contains(t, perr.Error(), "过大")
}

// TestPrepareDecompileTarget_WholeJarStillLimited 校验整 jar 反编译（entry 空）仍按整体大小判限。
func TestPrepareDecompileTarget_WholeJarStillLimited(t *testing.T) {
	srv := &Server{}
	work := t.TempDir()
	jar := filepath.Join(work, "big.jar")
	over := maxDecompileInputBytes + 1<<20
	makeDecompileJar(t, jar, "BigBlob.bin", zip.Store, over, map[string]string{"a.class": "x"})

	_, _, cleanup, perr := srv.prepareDecompileTarget(jar, "big.jar", "")
	if cleanup != nil {
		defer cleanup()
	}
	require.Error(t, perr, "整 jar 超整体上限须被拒绝")
	require.Contains(t, perr.Error(), "过大")
}

func TestCheckFileSize(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small.class")
	require.NoError(t, os.WriteFile(small, []byte("cafebabe"), 0o644))
	require.NoError(t, checkFileSize(small, maxDecompileInputBytes))

	// 超限拒绝。
	require.Error(t, checkFileSize(small, 4))
	// 目录拒绝。
	require.Error(t, checkFileSize(dir, maxDecompileInputBytes))
	// 不存在拒绝。
	require.Error(t, checkFileSize(filepath.Join(dir, "nope"), maxDecompileInputBytes))
}

func TestExtractClassFromJar(t *testing.T) {
	work := t.TempDir()
	jar := filepath.Join(work, "plugin.jar")
	makeTestZip(t, jar, map[string]string{
		"com/example/Foo.class": "FAKE-CLASS-BYTES",
		"plugin.yml":            "name: Foo\n",
	})

	tmp, err := extractClassFromJar(jar, "com/example/Foo.class")
	require.NoError(t, err)
	defer os.Remove(tmp)
	require.True(t, strings.HasSuffix(tmp, ".class"))
	b, err := os.ReadFile(tmp)
	require.NoError(t, err)
	require.Equal(t, "FAKE-CLASS-BYTES", string(b))

	// 不存在的条目报错。
	_, err = extractClassFromJar(jar, "no/such/Class.class")
	require.Error(t, err)
}

// newDecompileServer 起一个带已注册实例的 Server（不注入 decompiler）。
func newDecompileServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)
	const uuid = "22222222-2222-2222-2222-222222222222"
	workDir := filepath.Join(tmp, "inst")
	resp, err := srv.CreateInstance(context.Background(), &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid,
		Name:         "dec",
		StartCommand: "noop",
		WorkDir:      workDir,
		ProcessType:  "direct",
	})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)
	return srv, uuid, workDir
}

func TestDecompileClass_NoProvider(t *testing.T) {
	srv, uuid, _ := newDecompileServer(t)
	// 未注入 decompiler → 降级（success=false），不报 gRPC 错误。
	resp, err := srv.DecompileClass(context.Background(), &workerpb.DecompileClassRequest{
		InstanceUuid: uuid,
		Path:         "Foo.class",
	})
	require.NoError(t, err)
	require.False(t, resp.Success)
	require.Contains(t, resp.Error, "反编译")
}

func TestDecompileClass_InstanceNotFound(t *testing.T) {
	srv, _, _ := newDecompileServer(t)
	_, err := srv.DecompileClass(context.Background(), &workerpb.DecompileClassRequest{
		InstanceUuid: "nope",
		Path:         "Foo.class",
	})
	require.Error(t, err)
}

func TestDecompileClass_PathTraversalRejected(t *testing.T) {
	srv, uuid, _ := newDecompileServer(t)
	srv.SetDecompiler(decompiler.NewProvider(decompiler.Config{}))
	_, err := srv.DecompileClass(context.Background(), &workerpb.DecompileClassRequest{
		InstanceUuid: uuid,
		Path:         "../../etc/passwd.class",
	})
	require.Error(t, err) // 越界在 java 解析前被 validatePath 拦截
}

// findJavacForTest 找 JAVA_HOME 下的 javac（编译测试 class 用）；无则空串。
func findJavacForTest() (javaBin, javacBin string) {
	exeJava, exeJavac := "java", "javac"
	if runtime.GOOS == "windows" {
		exeJava, exeJavac = "java.exe", "javac.exe"
	}
	jh := os.Getenv("JAVA_HOME")
	if jh == "" {
		return "", ""
	}
	jb := filepath.Join(jh, "bin", exeJava)
	jc := filepath.Join(jh, "bin", exeJavac)
	if fileStat(jb) && fileStat(jc) {
		return jb, jc
	}
	return "", ""
}

func fileStat(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// TestDecompileClass_RealCFR 真机端到端：编译 class 进实例工作目录 → 经 RPC 反编译出源码。
// 需 JAVA_HOME 含 javac + 联网拉 CFR；缺任一即跳过（标「待真机验」）。
func TestDecompileClass_RealCFR(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过真机反编译")
	}
	_, javac := findJavacForTest()
	if javac == "" {
		t.Skip("无 JAVA_HOME/javac，跳过真机反编译")
	}

	srv, uuid, workDir := newDecompileServer(t)
	require.NoError(t, os.MkdirAll(workDir, 0o755))

	// CFR Provider（按需下载到临时缓存，sha256 pin 校验真实 jar）。
	cache := filepath.Join(t.TempDir(), "cache", "tools")
	prov := decompiler.NewProvider(decompiler.Config{CacheDir: cache, AllowDownload: true})
	if _, err := prov.Resolve(); err != nil {
		t.Skipf("CFR 不可用（可能无网络）: %v", err)
	}
	srv.SetDecompiler(prov)

	// 在实例工作目录里编译一个最小类。
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "Hi.java"),
		[]byte("public class Hi {\n  public String greet(){ return \"hello\"; }\n}\n"), 0o644))
	out, err := exec.Command(javac, filepath.Join(workDir, "Hi.java")).CombinedOutput()
	require.NoError(t, err, "javac 失败: %s", out)

	t.Run("反编译工作目录内 class", func(t *testing.T) {
		resp, err := srv.DecompileClass(context.Background(), &workerpb.DecompileClassRequest{
			InstanceUuid: uuid,
			Path:         "Hi.class",
		})
		require.NoError(t, err)
		require.True(t, resp.Success, resp.Error)
		require.Contains(t, resp.Source, "public class Hi")
		require.Contains(t, resp.Source, "greet")
		require.Contains(t, resp.Decompiler, "CFR")
	})

	t.Run("反编译 jar 内 class", func(t *testing.T) {
		// 把 Hi.class 打进一个 jar。
		jarPath := filepath.Join(workDir, "lib.jar")
		classBytes, rerr := os.ReadFile(filepath.Join(workDir, "Hi.class"))
		require.NoError(t, rerr)
		f, cerr := os.Create(jarPath)
		require.NoError(t, cerr)
		zw := zip.NewWriter(f)
		w, werr := zw.Create("Hi.class")
		require.NoError(t, werr)
		_, _ = w.Write(classBytes)
		require.NoError(t, zw.Close())
		require.NoError(t, f.Close())

		resp, err := srv.DecompileClass(context.Background(), &workerpb.DecompileClassRequest{
			InstanceUuid: uuid,
			Path:         "lib.jar",
			Entry:        "Hi.class",
		})
		require.NoError(t, err)
		require.True(t, resp.Success, resp.Error)
		require.Contains(t, resp.Source, "class Hi")
	})
}
