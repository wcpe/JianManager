package tailer

// 本文件实现 gzip 日志文件的读取（对齐 Filebeat filestream 的 `compression: auto`）。
//
// # 为何要支持 gz
//
// 生产实测有 **1374 个 `.gz` 归档从未进过平台**。用户明确要求「不采 gz 作为实时采集」
// （那是重复：live 文件已经采过了），但**历史归档仍然需要能补进来**。
// 两者不是同一件事：
//
//	实时采集一个还在被写的 latest.log  ⇒ 采
//	把已经轮转完的 2026-09-20-10.log.gz 补进来 ⇒ 采（但只能一次性读完）
//
// # 官方语义（Filebeat filestream GZIP，逐条对照）
//
//   - gzip 文件视为**不可变**：读到 EOF 后 harvester 关闭，不再等新数据；
//   - 指纹按**解压后**的数据算（即身份 = 解压内容，而不是压缩字节）；
//   - 只有「数据完全解压 + 读到 gzip footer + size 与 checksum 校验通过」才算 EOF；
//     校验失败 ⇒ 记 error 但当作已读完（不无限重试一份损坏的归档）；
//   - 每读一个 gz 约多占 **100KB** 内存 ⇒ 装配层要考虑并发上限。
//
// # 为何不把 gz 当普通文件
//
// 压缩字节里的 0x0A 是随机的，按行切分会直接得到垃圾。
// 必须先解压再切行，且切分必须在解压后的字节流上做。

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"strings"
)

// IsGzipPath 报告路径是否是一个 gzip 归档（按扩展名判断）。
//
// # 为何看扩展名而不是文件头
//
// 官方按文件头（魔数 `1f 8b`）判定，那在 `acquire.DetectArchiveFormat` 里已有实现。
// 但 scan 阶段要读每个候选文件的头会很贵（几万个文件）。
// 扩展名过滤是**扫描期**的廉价筛选，真正的健壮性由开文件后的 magic 校验冗余保障（见 OpenGzip）。
func IsGzipPath(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".gz")
}

// GzipReader 是一个已就绪的 gzip 行读取器。
//
// # 为何单独一个类型而不是复用 Harvester
//
// Harvester 维护「文件 offset」的概念，而 gzip **不能随机定位**
// （要解到第 N 字节必须从头解压）。所以 gz 的语义是「一次读完」，没有 offset 可用。
// 把它当成另一个输入形状，而不是给 Harvester 加特例。
type GzipReader struct {
	path   string
	file   *os.File
	zr     *gzip.Reader
	br     *bufio.Reader
	closed bool

	// lines 是已读出的行数（供观测）。
	lines int64
	// bytes 是已解压的字节数。
	bytes int64
}

// OpenGzip 打开一个 gzip 文件准备逐行读取。
//
// 先校魔数：扩展名是 .gz 但内容不是 gzip 的文件（例如运维手滑改的名）
// 必须得到一个清晰错误，而不是解压时的乱码。
func OpenGzip(path string) (*GzipReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("tailer: 打开 gz %s: %w", path, err)
	}
	br := bufio.NewReaderSize(f, 64*1024)
	magic, err := br.Peek(2)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("tailer: 读 gz 文件头 %s: %w", path, err)
	}
	if magic[0] != 0x1f || magic[1] != 0x8b {
		_ = f.Close()
		return nil, fmt.Errorf("tailer: %s 扩展名是 .gz 但内容不是 gzip（魔数 %02x %02x）", path, magic[0], magic[1])
	}
	zr, zerr := gzip.NewReader(br)
	if zerr != nil {
		_ = f.Close()
		return nil, fmt.Errorf("tailer: 初始化 gzip 解压 %s: %w", path, zerr)
	}
	return &GzipReader{path: path, file: f, zr: zr, br: bufio.NewReaderSize(zr, 64*1024)}, nil
}

// Path 返回文件路径。
func (g *GzipReader) Path() string { return g.path }

// Lines 返回已读出的行数。
func (g *GzipReader) Lines() int64 { return g.lines }

// Bytes 返回已解压的字节数。
func (g *GzipReader) Bytes() int64 { return g.bytes }

// ReadAll 一行一行读完整个文件，每行回调一次。
//
// # 为何没有“读一点停一下”
//
// gzip 归档是不可变的（官方明确），没有「等新数据」这件事。
// 一次读完也让「读完才能确认校验通过」这个语义变得简单：
// 只有 ReadAll 正常返回 nil，才能说这份归档被完整、正确地解开了。
//
// # 错误语义（这里必须与官方一致）
//
//   - 读行过程中出错（解压失败/checksum 不对）⇒ 返回 error，但**已回调的行不回退**；
//     调用方应把已投过的当作已采，并记录“这份归档不完整”；
//   - 不因为一份损坏的归档无限重试（那会卡住整个采集链）。
func (g *GzipReader) ReadAll(onLine func(line []byte) error) error {
	if g == nil || g.closed {
		return nil
	}
	defer func() { g.closed = true }()
	var pending []byte
	for {
		chunk, err := g.br.ReadBytes('\n')
		if len(chunk) > 0 {
			g.bytes += int64(len(chunk))
			pending = append(pending, chunk...)
			if n := len(pending); n > 0 && pending[n-1] == '\n' {
				line := pending[:n-1]
				line = trimCR(line)
				g.lines++
				if cbErr := onLine(line); cbErr != nil {
					return cbErr
				}
				pending = pending[:0]
			}
		}
		if err != nil {
			if err == io.EOF {
				// 末尾无换行的最后一行也要交出去（否则会静默丢一行）。
				if len(pending) > 0 {
					g.lines++
					if cbErr := onLine(trimCR(pending)); cbErr != nil {
						return cbErr
					}
				}
				return nil
			}
			return fmt.Errorf("tailer: 解压 %s 失败（行 %d，已解 %d 字节）: %w",
				g.path, g.lines, g.bytes, err)
		}
	}
}

// Close 释放句柄。
func (g *GzipReader) Close() error {
	if g == nil || g.file == nil {
		return nil
	}
	err := g.file.Close()
	g.file = nil
	if g.zr != nil {
		_ = g.zr.Close()
		g.zr = nil
	}
	return err
}

// trimCR 去掉行尾的 \r（Windows 换行）。
//
// # 为何两处都要去
//
// Java 经 log4j2 `%n` 在 Windows 上写 CRLF。旧链路全管线无 \r 处理（记忆里那次
// 字节级探针发现），结果是正文尾部带一个不可见字符，查询时肉眼难以察觉。
func trimCR(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\r' {
		return b[:n-1]
	}
	return b
}
