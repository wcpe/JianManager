package acquire

// 文件内容身份（内容前缀的 长度 + SHA256）。
//
// ## 为什么单独留这个小文件（2026-10-30）
//
// 内容**锚**（`ContentAnchor` / `ResolveContentAnchor` / `ArchiveImporter` 那套）
// 随 gz 归档采集一起删掉了——用户裁定「只读日志然后往 VL 塞，有就有没有就算了」。
//
// 但「内容前缀摘要」这个基础能力还有两个正当用途，不能一起删：
//   1. inode 复用防护（见 inode_identity.go）：inode 相同的两份文件还要前缀也相同
//      才算同一份；
//   2. FileTailer 记录活文件身份（PrepareRotation 里用同一口径的 min(长度,512)）。
//
// 两者必须用**同一个窗口长度**（512）：换一个值会让已落库的旧身份全部失配，
// 判据退化成“永远不匹配 ⇒ 永远跳过”。

import "os"

// contentIdentityBytes 是内容身份窗口长度上限。
//
// 512 这个值与 FileTailer 记录活文件身份时用的口径一致（见 PrepareRotation），
// 沿用同一口径才能与账本里已有的身份逐字匹配。
const contentIdentityBytes int64 = 512

// ContentIdentity 返回文件内容前缀的 (窗口长度, SHA256)。
//
// 空文件返回 (0, "", nil)：没有内容就没有身份，调用方按“未取得身份”处理。
func ContentIdentity(path string) (int64, string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, "", err
	}
	size := fi.Size()
	if size <= 0 {
		return 0, "", nil
	}
	window := size
	if window > contentIdentityBytes {
		window = contentIdentityBytes
	}
	sum, err := filePrefixSHA256(path, window)
	if err != nil {
		return 0, "", err
	}
	return window, sum, nil
}
