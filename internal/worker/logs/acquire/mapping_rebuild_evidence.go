package acquire

// 物理映射的**证据驱动自愈**（2026-10-28 生产停采最后一环）。
//
// ## 现场
//
// `checkPhysicalMapping` 报：
//
//	PHYSICAL_MAPPING_UNKNOWN: path=".../latest.log" logical_start=1764264
//	refusing to assign new identities without a proven physical origin
//
// 查账本发现该源的 live 分段：`start=1764264 end=1784612`，而 `PhysicalStart` 与
// `PhysicalMappingKnown` **两个字段都没写过**（JSON 里是 null）。文件实际只有 2428 字节。
//
// 这是**旧账本遗留形态**：分段是更早的版本创建的，那时还没有这两个字段。
// 2026-10-28 上午删掉旧的 `rebuildMappingForReplacedFile` 时，我判断新账本每个分段
// 创建时都带这两个字段，不会再进这个分支——这个判断**错了**：重建后运行时恢复出来的
// 分段仍来自旧账本，字段依然是空的。
//
// ## 为什么不能无条件重建
//
// 逻辑 `start` 非零意味着“已经读过前面那么多字节”。如果文件其实**没换**，
// 把物理起点归 0 会让已投递过的旧前缀被当成新日志重读一遍（静默重复入库）。
//
// ## 什么才算证据
//
// 只有一种情况可以安全地归零：**能直接证明当前文件不是当初那份**。
// 判据不能是“分段零宽”或“文件变小”（两者都能伪装），必须是内容身份：
//
//  1. 账本记录过 `IdentityBytes > 0 && IdentitySHA256 != ""`（没记过就没有比较基准）；
//  2. 当前文件前缀重算出的 SHA256 与记录的**不相同**（真的换了）；
//  3. 当前文件至少还有 `IdentityBytes` 长（短于基准时无法比较，宁可不动）。
//
// 三条全成立才重建。任一不成立 ⇒ 保持原行为（拒绝 + 可见缺口），不猜。
//
// 重建后必须**同时消解已登记的旧缺口**：`ResumeAcquire` 看的是账本里的未解决 gap，
// 不是本对象的内存状态。只置标志而不消缺口 ⇒ 下一轮照样被拒 ⇒ 静默死锁。

import (
	"fmt"
	"log/slog"
	"os"
)

// mappingRebuildEvidence 把“当前文件不是当初那份”的证据化成纯函数，便于单测。
//
// 返回成立的证据描述（写日志用）与是否成立。
func mappingRebuildEvidence(t *FileTailer) (string, bool) {
	if t.physicalStart != 0 {
		return "", false // 非零物理起点：重锦过的残段，不在此处理范围
	}
	if t.identityBytes <= 0 || t.identitySHA256 == "" {
		return "", false // 无内容身份：没有比较基准
	}
	info, err := os.Stat(t.path)
	if err != nil {
		return "", false
	}
	if info.Size() < t.identityBytes {
		return "", false // 文件比基准前缀还短：无法可靠比较
	}
	current, hashErr := filePrefixSHA256(t.path, t.identityBytes)
	if hashErr != nil || current == "" {
		return "", false
	}
	if current == t.identitySHA256 {
		return "", false // 内容就是当初那份：重建会把已投递过的前缀重读
	}
	return fmt.Sprintf("recorded=%s current=%s bytes=%d fileSize=%d",
		shortHash(t.identitySHA256), shortHash(current), t.identityBytes, info.Size()), true
}

// shortHash 只取前 12 个十六进制字符（日志可读性；不参与任何比较）。
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// rebuildMappingOnReplacedFile 在能直接证明“当前文件不是当初那份”时重建物理映射。
//
// 成功返回 nil；不成立或失败返回错误，由调用方保持原有拒绝行为。
func (t *FileTailer) rebuildMappingOnReplacedFile() error {
	evidence, ok := mappingRebuildEvidence(t)
	if !ok {
		return errMappingNotRebuildable
	}
	// 内容身份不同 ⇒ 文件确实换了 ⇒ 逻辑起点对应新文件第 0 字节。
	t.physicalStart = 0
	t.physicalMappingKnown = true
	// 同时重置内容身份到**当前**文件，否则下一轮会再判成“变了”。
	if sum, sumErr := filePrefixSHA256(t.path, t.identityBytes); sumErr == nil {
		t.identitySHA256 = sum
	}
	if err := t.registerLiveSegment(); err != nil {
		return err
	}
	// **必须同时消解已登记的旧缺口**，否则 `ResumeAcquire` 仍被拒、而本轮又不再报错
	// ⇒ 静默死锁（与 2026-10-28 修过的 inst:321 同型）。
	if resolved, err := t.led.ResolveGapsMatching(t.key, PhysicalMappingUnknownReason,
		fmt.Sprintf("path=%q", t.path),
		"physical mapping rebuilt: live file was replaced (content identity differs)"); err == nil && resolved > 0 {
		slog.Info("物理映射重建后已撑销旧隔离缺口",
			"logSourceID", t.key.LogSourceID, "path", t.path, "resolvedGaps", resolved)
	}
	slog.Warn("活文件已被替换且内容身份不匹配，物理映射已重建（旧账本缺字段的自愈）",
		"logSourceID", t.key.LogSourceID, "path", t.path,
		"logicalStart", t.segmentStart, "evidence", evidence)
	return nil
}

// 哨兵：证据不足或重建不可行。调用方据此保持原有拒绝行为。
var errMappingNotRebuildable = fmt.Errorf("acquire: physical mapping not rebuildable")
