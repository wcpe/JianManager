package service

import (
	"github.com/wcpe/JianManager/internal/controlplane/crashdiag"
)

// 崩溃根因枚举（FR-470 spec §2.1）：service 层对外暴露的别名，实现位于 crashdiag 包。
//
// 为何不把规则直接写在 service：service 依赖 grpc（ClientPool），而崩溃快照的写入路径
// 在 grpc 层，若规则只在 service 会形成 import cycle；故纯函数规则下沉到中立的 crashdiag 包，
// 由 service 与 grpc 两侧共同引用（service 保留本门面文件作为「服务层入口」）。
const (
	CrashRootCauseOOM           = crashdiag.CrashRootCauseOOM
	CrashRootCausePortInUse     = crashdiag.CrashRootCausePortInUse
	CrashRootCauseClassNotFound = crashdiag.CrashRootCauseClassNotFound
	CrashRootCauseJVMArgs       = crashdiag.CrashRootCauseJVMArgs
	CrashRootCausePermission    = crashdiag.CrashRootCausePermission
	CrashRootCauseSegfault      = crashdiag.CrashRootCauseSegfault
	CrashRootCauseCorruptData   = crashdiag.CrashRootCauseCorruptData
	CrashRootCauseUnknown       = crashdiag.CrashRootCauseUnknown
)

// CrashClassification 崩溃分类结果（FR-470）：service 层别名。
type CrashClassification = crashdiag.CrashClassification

// ClassifyCrashSnapshot 对崩溃现场做根因归类（FR-470，纯函数，见 crashdiag 包）。
func ClassifyCrashSnapshot(exitCode int, signal, tailOutput string) CrashClassification {
	return crashdiag.ClassifyCrashSnapshot(exitCode, signal, tailOutput)
}

// EncodeCrashEvidence 把证据行序列化为 JSON 数组文本（落库用）。
func EncodeCrashEvidence(lines []string) string { return crashdiag.EncodeCrashEvidence(lines) }

// DecodeCrashEvidence 解析证据 JSON 文本；空串/非法 JSON 返回 nil。
func DecodeCrashEvidence(raw string) []string { return crashdiag.DecodeCrashEvidence(raw) }
