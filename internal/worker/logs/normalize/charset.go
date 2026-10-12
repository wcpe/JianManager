package normalize

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// 本文件实现按源字符集解码（缺陷 B 修复）。
//
// 现场（2026-10-01/02）：MC 的 Java 日志由中文 locale 的 JVM 以 **GBK** 写出，平台按 UTF-8 直读，
// UI/接口显示为 `[Lodestone] �Ѱַ�ѽ� BC �ʵ�`；同机 Beacon 的 UTF-8 日志正常（可作对照）。
//
// 解码位置选在归一化入口（Normalizer.FeedAt）而不是各采集器：tailer / gzip 归档 / 受管 Raw
// 三条输入路径最终都汇到这里，在这里收口可以保证「同一份字节在任何输入路径上得到同一个事件」。
//
// 判据（**auto 模式**下为什么不会把合法 UTF-8 误判成 GBK）：
//   - 合法 UTF-8 永远原样返回，绝不进入 GB 系解码分支；
//   - 非 UTF-8 的字节先按 GB18030 解码，**解码结果含 U+FFFD（替换字符）即判为不可信**并放弃解码
//     （实测：随机损坏字节 0xff 0xfe、孤立截断字节 0xC4 经 GB18030 解码都会产生 U+FFFD，
//     而真实 GBK 中文不会）；
//   - 判定结果按源粘滞（sticky），但遇到「合法 UTF-8 且含非 ASCII」的行会解除粘滞——
//     真 UTF-8 源里偶发的损坏行不会把该源永久锁进 GB 系解码。
//
// 配置可用 SourceConfig.Charset 按源覆盖（auto/utf-8/gbk/gb18030）；不一致或未知的取值在
// 登记阶段被拒（见 pipeline.New 与 ingest.Register 的校验），不会静默回退。
// ⚠️ 显式配置为 GB 系时**不做**「合法 UTF-8 原样返回」这道保护（配置即事实：配置错的后果
// 要暴露出来，而不是被静默改对，见 decodeWith 的说明 ✓）——混有 UTF-8 内容的源应配 auto。
const (
	// CharsetAuto 自动检测（默认）：合法 UTF-8 原样，非法 UTF-8 尝试 GB18030。
	CharsetAuto Charset = "auto"
	// CharsetUTF8 强制 UTF-8：不做任何解码（非法字节走既有净化路径并留审计标记）。
	CharsetUTF8 Charset = "utf-8"
	// CharsetGBK 强制 GBK：中文 locale 的 Java 日志（MC 常见）。
	CharsetGBK Charset = "gbk"
	// CharsetGB18030 强制 GB18030：GBK 的超集，覆盖更多汉字与四字节序列。
	CharsetGB18030 Charset = "gb18030"
)

// Charset 是日志源正文的字符集标识。
type Charset string

// ParseCharset 归一化配置取值（大小写不敏感，容忍 `GB2312`/`CP936` 这类常见别名）。
// 空串视为 auto；未知取值返回错误（调用方必须显式失败，不得静默按 UTF-8 处理）。
func ParseCharset(raw string) (Charset, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(CharsetAuto):
		return CharsetAuto, nil
	case "utf-8", "utf8":
		return CharsetUTF8, nil
	case "gbk", "gb2312", "cp936", "ms936":
		// GB2312 ⊂ GBK ⊂ GB18030，按 GBK 解码对前两者都是正确超集。
		return CharsetGBK, nil
	case "gb18030", "gb-18030":
		return CharsetGB18030, nil
	default:
		return CharsetAuto, fmt.Errorf("normalize: 未知日志字符集 %q（支持 auto/utf-8/gbk/gb18030）", raw)
	}
}

// IsValidCharset 报告配置取值是否被支持（供登记阶段校验，避免创建后才失败）。
func IsValidCharset(raw string) bool {
	_, err := ParseCharset(raw)
	return err == nil
}

// charsetDecoder 是单源会话内的解码状态机。
//
// 为什么按源持有状态：GBK 的双字节序列不跨行（第二字节范围不含 0x0A），因此逐行解码是正确的；
// 但「这一行是不是 GBK」的判定需要源级粘滞，否则纯 ASCII 行与中文行会得到不一致的判定。
type charsetDecoder struct {
	configured Charset
	detected   Charset
	// decodedLines 是会话内被真正解码（发生转码）的行数，仅供观测/诊断。
	decodedLines int64
}

// newCharsetDecoder 创建解码器；未知配置一律按 auto（登记阶段已校验，这里只做兜底）。
func newCharsetDecoder(configured string) *charsetDecoder {
	parsed, err := ParseCharset(configured)
	if err != nil {
		parsed = CharsetAuto
	}
	return &charsetDecoder{configured: parsed}
}

// effective 返回当前的生效字符集（auto 且尚未判定时按 utf-8 报告）。
func (d *charsetDecoder) effective() Charset {
	if d == nil {
		return CharsetUTF8
	}
	if d.configured != CharsetAuto {
		return d.configured
	}
	if d.detected != "" {
		return d.detected
	}
	return CharsetUTF8
}

// decode 返回本行解码后的文本与生效字符集（生效为 UTF-8 时返回原串、零拷贝）。
//
// 语义约定：本函数**只做编码转码**，不做净化——无法判定的输入原样返回，
// 由既有的 sanitizeUTF8 兜底并留下 encoding_sanitized 审计标记。
func (d *charsetDecoder) decode(raw string) (string, Charset) {
	if d == nil || raw == "" {
		return raw, CharsetUTF8
	}
	if d.configured != CharsetAuto {
		return d.decodeWith(d.configured, raw)
	}
	// 源级粘滞：已判定为 GB 系时，除「确凿的 UTF-8 非 ASCII 文本」外一律按同一编码处理。
	// 纯 ASCII 行也走这里：同一源的事件因此带同一个 source_charset 口径，历史数据重写
	// 可以按该字段整源筛选，而不会漏掉其中的 ASCII 行。
	if d.detected == CharsetGBK || d.detected == CharsetGB18030 {
		if utf8.ValidString(raw) && hasNonASCII(raw) {
			// 真 UTF-8 源里的偶发损坏行不该把整源永久锁进 GB 系解码。
			d.detected = ""
		} else {
			return d.decodeWith(d.detected, raw)
		}
	}
	if utf8.ValidString(raw) {
		return raw, CharsetUTF8
	}
	if d.detected == CharsetUTF8 {
		// 已判定为 UTF-8：损坏行原样交给净化路径（不因一行损坏就换编码）。
		return raw, CharsetUTF8
	}
	if decoded, ok := decodeGB(raw); ok {
		d.detected = CharsetGB18030
		if decoded != raw {
			d.decodedLines++
		}
		return decoded, CharsetGB18030
	}
	// 无法判定：保持原样，交给 sanitizeUTF8（并标记编码已净化）。
	return raw, CharsetUTF8
}

// decodeWith 按**显式配置**的字符集解码（配置即事实 ✗✓）。
//
// 语义（NORM-1 订正后的实际口径）：配置为 GB 系时，本行走 GB18030 解码；**不再**套用 auto 模式
// 那道「合法 UTF-8 原样返回」的保护。理由是配置错误必须暴露而不是被静默改对——否则把 GBK 源
// 误配成 utf-8（或反之）时，平台会悄悄给出正确文本，现场永远发现不了 ✗。因此配置为 gbk 的源里
// 夹杂的 UTF-8 非 ASCII 行**会**被转码（GB18030 是 Unicode 超集，解 UTF-8 字节通常不产生
// U+FFFD ⇒ 无 `encoding_sanitized` 标记的静默乱码）；混有 UTF-8 内容的源应配 **auto**
// （auto 的粘滞判据正是为此存在 ✓）。纯 ASCII 行经 decodeGB 的短路原样返回（零转码 ✓）。
//
// 下面这个 utf8.ValidString 分支不是"保护合法 UTF-8"——它只在 **GB 解码被判不可信**
// （结果含 U+FFFD）且原文却是合法 UTF-8 时兜底：此时保留原文，并按 utf-8 记生效字符集。
func (d *charsetDecoder) decodeWith(charset Charset, raw string) (string, Charset) {
	switch charset {
	case CharsetUTF8:
		return raw, CharsetUTF8
	case CharsetGBK, CharsetGB18030:
		if decoded, ok := decodeGB(raw); ok {
			if decoded != raw {
				d.decodedLines++
			}
			return decoded, charset
		}
		if utf8.ValidString(raw) {
			// GB 解码不可信（含替换字符）但原文是合法 UTF-8 ⇒ 保留原文，生效字符集记 utf-8。
			return raw, CharsetUTF8
		}
		return raw, charset
	default:
		return raw, CharsetUTF8
	}
}

// decodeGB 尝试按 GB18030 解码；结果含替换字符即判为不可信。
//
// 判据依据（.tmp 实测）：真实 GBK 中文解码后不含 U+FFFD；而随机损坏字节（0xff 0xfe）、
// 孤立截断字节（0xc4）解码后必然出现 U+FFFD——用「无替换字符」区分二者，避免把损坏数据
// 当成汉字文本展示。
func decodeGB(raw string) (string, bool) {
	if !hasNonASCII(raw) {
		return raw, true
	}
	decoded, _, err := transform.String(simplifiedchinese.GB18030.NewDecoder(), raw)
	if err != nil {
		return "", false
	}
	if strings.ContainsRune(decoded, utf8.RuneError) {
		return "", false
	}
	return decoded, true
}

func hasNonASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return true
		}
	}
	return false
}
