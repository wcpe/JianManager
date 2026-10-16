package acquire

import (
	"compress/gzip"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// 归档形态的判定与形态参数（纯文本轮转归档导入）。
//
// 现场缺口：BungeeCord 的轮转产物 `proxy.log.N` 是**纯文本**且**没有扩展名**
// （java.util.logging.FileHandler 把 proxy.log.0 轮转成同目录的数字后缀文件），
// 而此前的归档导入器只有 gzip 一种实现 ⇒ 任何被发现的轮转文件都以 ARCHIVE_GZIP_CORRUPT
// 失败，而该原因不在自动消解白名单（ledger/gap_bounds.go）⇒ 源一旦被暂停就永久停采。
// 本文件给出形态判据（按内容判定；名字只用于「文件自己声明是压缩归档」这一既有契约），
// 并把两种形态的差异（登记 kind + 解码方式）收敛成一个参数，使导入主流程
// （archive.go 的 importArchive）对两者**逐字共用**。

// gzipMagic 是 gzip 流的魔数（RFC 1952 §2.3.1）。它是「内容是不是 gzip」的唯一可靠判据。
var gzipMagic = [2]byte{0x1f, 0x8b}

// archiveFormat 描述一种归档形态在导入流程里的**全部差异**。
//
// 为什么收敛成参数而不是复制一份导入实现：导入流程里除「形态」以外的一切——归属闸、
// 断点续读的跳字节、有界分批、行/多行归一、record 范围与 canonical hash 口径、
// 缺口原因码——都必须对两种形态**完全一致**。任何一处被复制，就会有两份会各自漂移的口径。
type archiveFormat struct {
	// kind 是登记到账本分段的形态（ledger.SegmentKind 的归档类取值）。
	kind ledger.SegmentKind
	// decode 把「已打开的归档字节流」转成「按行可读的文本流」。返回错误表示该形态在解码
	// 阶段即不可解（gzip 头损坏等）。
	decode func(io.Reader) (io.ReadCloser, error)
	// decodeFailureReason 是 decode 失败时登记的缺口原因码（空串 = 该形态没有解码阶段，
	// 故不可能在此失败）。
	decodeFailureReason string
	// decodeFailurePrefix 是 decode 失败时返回给调用方的 Reason 前缀（保持既有文案）。
	decodeFailurePrefix string
}

var (
	// formatGzip：压缩归档（Bukkit / Waterfall / Velocity 的 `logs/*.gz`）。
	formatGzip = archiveFormat{
		kind:                ledger.SegmentGzip,
		decode:              func(r io.Reader) (io.ReadCloser, error) { return gzip.NewReader(r) },
		decodeFailureReason: ledger.GapReasonArchiveGzipCorrupt,
		decodeFailurePrefix: "corrupt gzip: ",
	}
	// formatRotatedText：纯文本轮转归档（BungeeCord 的 `proxy.log.N`）。
	//
	// 它**没有**解码阶段：字节即内容，没有容器头可校验，故不存在「形态损坏」这种失败
	// （读取期错误仍按既有 ARCHIVE_READ_ERROR 登记）。这也意味着**不需要引入任何新的缺口
	// 原因码**——新原因若不在自动消解白名单里，就会成为「源暂停后永久停采」的新入口。
	formatRotatedText = archiveFormat{
		kind:   ledger.SegmentRotatedText,
		decode: func(r io.Reader) (io.ReadCloser, error) { return io.NopCloser(r), nil },
	}
)

// DetectArchiveFormat 判定归档文件的物理形态。
//
// 判据顺序（名字只用于「文件自己声明是压缩归档」这一既有契约；其余一律看内容）：
//  1. 名字以 `.gz` 结尾 ⇒ gzip。这是**既有契约**：归档发现给 bukkit/waterfall/velocity 匹配的
//     就是 `logs/*.gz`，其内容必须是 gzip 流；内容不是时，gzip 路径给出的
//     ARCHIVE_GZIP_CORRUPT 正是正确诊断（写坏/截断的归档必须可见、可隔离，
//     不得被当成纯文本静默导入一堆垃圾事件——既有回归
//     ingest.TestManagerQuarantinesUnchangedCorruptGzipWithoutRepeatingGap 正是守这一条）。
//  2. 其余读文件头：命中 gzip 魔数 `1f 8b` ⇒ gzip（内容自证：被压过的 `proxy.log.N` 也走这里）；
//  3. 其余（含短文件、空文件、无扩展名的纯文本）⇒ 纯文本轮转。
//
// 为什么不能**只**按扩展名判定：BungeeCord 的轮转产物**没有扩展名**，而「没有 .gz 后缀」
// 推不出「不是 gzip」（运维可能自己压过），故第 2 条按内容兜住这一形态。
//
// 为什么非 .gz 的空文件必须走纯文本：`proxy.log.N.lck`（java.util.logging 为每个日志文件
// 创建的 0 字节锁文件）会被 bungee 的归档 glob 匹配到。若它落进 gzip 路径，就会为**每一个**
// 轮转文件留一条 ARCHIVE_GZIP_CORRUPT 缺口（不可自动消解 ⇒ 源暂停即永久停采）；
// 纯文本路径把它处置为「空归档」：0 事件、无缺口、直接记为已导入。
//
// 内容不可知（打不开/读失败）时返回 gzip：这是**既有**的唯一形态，由 gzip 路径按既有原因码
// （ARCHIVE_UNREADABLE / ARCHIVE_OPEN_FAILED）如实登记失败——判定阶段不产生新的失败语义，
// 也不吞掉任何错误，更不因为「判不出」而让归档悄悄不被处理。
func DetectArchiveFormat(path string) ledger.SegmentKind {
	if isGzipArchiveName(path) {
		return ledger.SegmentGzip
	}
	f, err := os.Open(path)
	if err != nil {
		return ledger.SegmentGzip
	}
	defer f.Close()
	var head [2]byte
	switch _, err := io.ReadFull(f, head[:]); {
	case err == nil:
		if head == gzipMagic {
			return ledger.SegmentGzip
		}
		return ledger.SegmentRotatedText
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		// 不足 2 字节：内容不足以构成 gzip 头 ⇒ 非压缩归档（空轮转文件/锁文件）。
		return ledger.SegmentRotatedText
	default:
		// 读失败（目录、设备错误等）：内容不可知 ⇒ 交回既有 gzip 路径。
		return ledger.SegmentGzip
	}
}

// isGzipArchiveName 报告文件名是否**声明**自己是压缩归档（仅用于内容不足以判定的短文件）。
func isGzipArchiveName(path string) bool {
	const suffix = ".gz"
	return len(path) > len(suffix) && strings.EqualFold(path[len(path)-len(suffix):], suffix)
}
