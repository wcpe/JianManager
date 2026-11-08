package logtail

import "strings"

// namespaceOf 从完整源 id（`inst:318/file`）取分区命名空间（`inst:318`）。
//
// 与 Catalog 分区键的 `StorageNamespace` 同一口径：旧链路在登记时把
// `StorageNamespace` 设为 `TargetID`，而 `LogSourceID = TargetID + "/" + stream`。
// 两边必须算出同一个值，否则「行写进去了，分区记录却建在另一个 namespace 下」——
// 表现就是日志页永远空。
//
// 不含 `/` 的 id（如 `node:1`）原样返回：它本身就是命名空间。
func namespaceOf(logSourceID string) string {
	if i := strings.LastIndex(logSourceID, "/"); i > 0 {
		return logSourceID[:i]
	}
	return logSourceID
}
