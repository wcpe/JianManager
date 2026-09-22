package crashdiag

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClassifyCrashSnapshot_RootCauses 各根因正例（FR-470 spec §3 验收：每类正例）。
func TestClassifyCrashSnapshot_RootCauses(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		signal   string
		tail     string
		want     string
	}{
		{
			name:     "oom heap space",
			exitCode: 1,
			tail:     "java.lang.OutOfMemoryError: Java heap space\n\tat net.minecraft.server.MinecraftServer.run(MinecraftServer.java:1)",
			want:     CrashRootCauseOOM,
		},
		{
			name:     "oom cannot allocate",
			exitCode: 1,
			tail:     "Native memory allocation (mmap) failed to map 12288 bytes\nCannot allocate memory",
			want:     CrashRootCauseOOM,
		},
		{
			name:     "port in use",
			exitCode: 1,
			tail:     "java.net.BindException: Address already in use\n\tat sun.nio.ch.Net.bind0(Native Method)",
			want:     CrashRootCausePortInUse,
		},
		{
			name:     "class not found",
			exitCode: 1,
			tail:     "Error: Could not find or load main class com.example.Main\nCaused by: java.lang.ClassNotFoundException: com.example.Main",
			want:     CrashRootCauseClassNotFound,
		},
		{
			name:     "jvm args",
			exitCode: 1,
			tail:     "Unrecognized VM option 'UseG1GC1'\nError: Could not create the Java Virtual Machine.",
			want:     CrashRootCauseJVMArgs,
		},
		{
			name:     "permission",
			exitCode: 1,
			tail:     "java.io.FileNotFoundException: /srv/data/world (Permission denied)",
			want:     CrashRootCausePermission,
		},
		{
			// m-4② 负例：FileNotFoundException 单独出现（无 Permission denied）时**不得**归 permission——
			// 该文案绝大多数是「文件不存在」（jar 被删/路径写错/world 缺失），硬归权限会把排查引偏。
			name:     "file not found without permission denied is not permission",
			exitCode: 1,
			tail:     "java.io.FileNotFoundException: server.jar (No such file or directory)",
			want:     CrashRootCauseCorruptData,
		},
		{
			// m-4①：JDK 升级后旧 class 跑在新 JVM 上（MC 升级 JDK 后最常见的崩溃之一）。
			name:     "unsupported class version maps to jvm_args",
			exitCode: 1,
			tail:     "java.lang.UnsupportedClassVersionError: net/minecraft/server/Main has been compiled by a more recent version of the Java Runtime (class file version 61.0)",
			want:     CrashRootCauseJVMArgs,
		},
		{
			name:     "segfault",
			exitCode: 139,
			signal:   "segmentation fault",
			tail:     "# A fatal error has been detected by the Java Runtime Environment:\n#  SIGSEGV (0xb) at pc=0x00007f1234567890",
			want:     CrashRootCauseSegfault,
		},
		{
			name:     "corrupt data",
			exitCode: 1,
			tail:     "java.util.zip.ZipException: invalid stream header: 00000000\nFailed to load level.dat",
			want:     CrashRootCauseCorruptData,
		},
		{
			name:     "unknown fallback",
			exitCode: 1,
			tail:     "Shutting down server\nSaving chunks",
			want:     CrashRootCauseUnknown,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyCrashSnapshot(tc.exitCode, tc.signal, tc.tail)
			assert.Equal(t, tc.want, got.RootCause)
			assert.NotEmpty(t, got.Signature, "指纹不得为空")
			if tc.want != CrashRootCauseUnknown {
				assert.NotEmpty(t, got.Evidence, "命中根因应有证据原文")
				assert.GreaterOrEqual(t, got.Confidence, 0.7)
			}
		})
	}
}

// TestClassifyCrashSnapshot_Negatives 负例：相似但不该误判的文本。
func TestClassifyCrashSnapshot_Negatives(t *testing.T) {
	tests := []struct {
		name string
		tail string
		not  string
	}{
		{
			name: "普通 INFO 日志含 memory 字样不算 OOM",
			tail: "INFO: server memory usage is fine\nDone loading",
			not:  CrashRootCauseOOM,
		},
		{
			name: "正常关服的 saving chunks 不算损坏",
			tail: "Stopping server\nSaving chunks for level 'world'",
			not:  CrashRootCauseCorruptData,
		},
		{
			name: "启动横幅不算 JVM 参数错误",
			tail: "Starting Minecraft server on *:25565\nUsing Java 21",
			not:  CrashRootCauseJVMArgs,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyCrashSnapshot(0, "", tc.tail)
			assert.NotEqual(t, tc.not, got.RootCause)
		})
	}
}

// TestClassifyCrashSnapshot_ExitCodeSignalPriors 退出码/信号先验（killed/137 → OOM，SIGSEGV/139 → segfault）。
func TestClassifyCrashSnapshot_ExitCodeSignalPriors(t *testing.T) {
	// 无文本证据时 killed 信号兜底判 OOM。
	got := ClassifyCrashSnapshot(137, "killed", "")
	assert.Equal(t, CrashRootCauseOOM, got.RootCause)
	assert.InDelta(t, 0.6, got.Confidence, 0.001)

	got = ClassifyCrashSnapshot(139, "segmentation fault", "")
	assert.Equal(t, CrashRootCauseSegfault, got.RootCause)

	// 信号先验不得覆盖明确的文本证据：killed 但日志明确端口占用 → 端口占用。
	got = ClassifyCrashSnapshot(137, "killed", "java.net.BindException: Address already in use")
	assert.Equal(t, CrashRootCausePortInUse, got.RootCause)

	// 普通退出码 + 无信号 + 无文本 → unknown。
	got = ClassifyCrashSnapshot(1, "terminated", "some random output")
	assert.Equal(t, CrashRootCauseUnknown, got.RootCause)
	assert.Zero(t, got.Confidence)
}

// TestClassifyCrashSnapshot_MultiLabel 多规则命中：取最高分为主根因，≥0.7 的其它根因进标签。
func TestClassifyCrashSnapshot_MultiLabel(t *testing.T) {
	tail := "java.lang.OutOfMemoryError: Java heap space\njava.net.BindException: Address already in use\nCaused by: java.lang.OutOfMemoryError"
	got := ClassifyCrashSnapshot(1, "", tail)
	assert.Equal(t, CrashRootCauseOOM, got.RootCause)
	assert.Contains(t, got.Labels, CrashRootCauseOOM)
	assert.Contains(t, got.Labels, CrashRootCausePortInUse)
	assert.Equal(t, CrashRootCauseOOM, got.Labels[0], "主根因应在标签首位")
}

// TestCrashSignature_NormalizedStable 指纹归一稳定：时间戳/地址/行号不同但同类崩溃归一为同一指纹。
func TestCrashSignature_NormalizedStable(t *testing.T) {
	a := "java.lang.OutOfMemoryError: Java heap space\n\tat a.A.b(A.java:123)\n\tat c.D.e(D.java:45)"
	b := "java.lang.OutOfMemoryError: Java heap space\n\tat a.A.b(A.java:999)\n\tat c.D.e(D.java:1)"
	ca := ClassifyCrashSnapshot(1, "", a)
	cb := ClassifyCrashSnapshot(1, "", b)
	assert.Equal(t, ca.Signature, cb.Signature, "同种异常不同行号应归一为同一指纹")

	// 不同根因 → 不同指纹。
	cc := ClassifyCrashSnapshot(1, "", "java.net.BindException: Address already in use")
	assert.NotEqual(t, ca.Signature, cc.Signature)
}

// TestCrashSignature_HexNormalized 十六进制地址归一。
func TestCrashSignature_HexNormalized(t *testing.T) {
	got := ClassifyCrashSnapshot(1, "", "hs_err_pid1234: fatal error at pc=0x00007fab12345678")
	assert.Equal(t, CrashRootCauseSegfault, got.RootCause)
	assert.NotContains(t, got.Signature, "0x00007fab12345678")
	assert.Contains(t, got.Signature, "HEX")
}

// TestEncodeDecodeCrashEvidence 证据 JSON 编解码往返。
func TestEncodeDecodeCrashEvidence(t *testing.T) {
	assert.Empty(t, EncodeCrashEvidence(nil))
	lines := []string{"a", "b c"}
	enc := EncodeCrashEvidence(lines)
	assert.JSONEq(t, `["a","b c"]`, enc)
	assert.Equal(t, lines, DecodeCrashEvidence(enc))
	assert.Nil(t, DecodeCrashEvidence(""))
	assert.Nil(t, DecodeCrashEvidence("not json"))
}

// TestClassifyCrashSnapshot_EvidenceCap 证据行数封顶且去重。
func TestClassifyCrashSnapshot_EvidenceCap(t *testing.T) {
	tail := ""
	for i := 0; i < 20; i++ {
		tail += "OutOfMemoryError: Java heap space\n"
	}
	got := ClassifyCrashSnapshot(1, "", tail)
	require.Equal(t, CrashRootCauseOOM, got.RootCause)
	assert.LessOrEqual(t, len(got.Evidence), maxCrashEvidenceLines)
}
