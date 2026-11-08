package tailer

// 本文件解决 fingerprint 的**小文件增长**问题（Filebeat `growing fingerprint`）。
//
// # 问题（实测踩到，2026-11-06）
//
// 指纹只哈希文件前 1024 字节，所以：
//
//	文件 ≥ 1024 字节：追加 ⇒ 前 1024 字节不变 ⇒ 指纹不变 ✓
//	文件 <  1024 字节：追加 ⇒ 参与哈希的字节变多 ⇒ **指纹会变** ✗
//
// 后果：刚新建的 latest.log（几十字节）采一轮 → 重启 → 指纹变了 ⇒ registry 命中不了
// ⇒ 整文件从头重读 ⇒ 已投递的行全部重复上报。而“刚启动的服务”恰好全是这个形态。
//
// # 为什么不直接做 Filebeat 的身份迁移
//
// 它的 growing 机制会在文件长到 1024 字节时把 registry 条目迁移到正式指纹上。
// 那需要跨条目的原子迁移，而且迁移失败的后果是「要么重读、要么漏读」，两条都不好。
//
// 这里用一个更弱但**可验证**的判据代替：
//
//	记录的指纹是对前 N 字节算的（N = min(1024, 记录时的 Size)）
//	⇒ 现在再对**同样前 N 字节**算一遍，相等就说明是同一个文件的延长
//
// 逐字节相等是**可证的**（sha256 碰撞可忽略），不像“路径相同”那种猜测。
// 而 offset 只在文件确实变长时复用（新内容总是在末尾），所以即使误中也不会漏行，
// 最坏情况是重复——而重复比漏行安全（用户裁定：重复可接受，静默丢不可接受）。

// FingerprintPrefixLen 返回一条位点记录当时参与哈希的字节数。
//
// 由记录时的文件大小反推：`min(FingerprintLength, Size)`。
// 旧记录同样成立（它没有额外的字段可读），故不需要 schema 变更。
func FingerprintPrefixLen(st FileState) int {
	if st.Size <= 0 {
		return 0
	}
	if st.Size < FingerprintLength {
		return int(st.Size)
	}
	return FingerprintLength
}

// fingerprintOfPrefix 对文件**前 n 字节**算指纹（与 fingerprintOf 同一算法）。
//
// 与 fingerprintOf 的区别仅在于长度是显式给的：判断「同一文件的延长」时
// 必须用**记录当时**的长度重算，而不是当前长度。
func fingerprintOfPrefix(path string, n int) (string, bool) {
	if n <= 0 {
		return "", false
	}
	fp, err := fingerprintOfFixed(path, n)
	if err != nil {
		return "", false
	}
	return fp, true
}
