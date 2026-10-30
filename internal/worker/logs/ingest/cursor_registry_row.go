package ingest

// 游标表（ingest.cursor）的单行格式：解析与序列化。
//
// ## 列布局（v2）
//
//	0  key              源键 = log_source_id/source_generation
//	1  path             采集文件路径
//	2  dev              文件所在设备号（拿不到为 0）
//	3  ino              inode（拿不到为 0）
//	4  cursor           文件内字节偏移
//	5  logicalpos       对应的逻辑源位置（跨轮转累计）
//	6  mode             FILE_PRIMARY / STDIO_PRIMARY
//	7  source_category  instance / worker / node
//	8  storage_namespace
//	9  rotate_to        轮转目标路径
//	10 utc_day          登记日分区
//	11 charset          **显式**字符集（空 = 跟随节点默认）
//	12 time_zone        **显式**时区（空 = 跟随节点默认）
//	13 stream           stdout / stderr / 空
//	14 archive_glob     归档发现 glob
//
// ## 为什么列集比第一轮多，且后四列不在需求给定的 11 列里
//
// 前 11 列是本次需求直接指定的。后 4 列的取舍各有一条硬理由，缺任何一条都会让
// 「重启后源能正确重建」变成「重启后源重建了但不是原来那个源」：
//
//   - charset / time_zone：源的两个**显式**归一化口径。它们必须跨重启存活，否则
//     运维给某源显式配的 `charset: utf-8` 会在重启后退回节点默认（defaults_restore_test.go
//     第三/四轮钉的正是「显式值不得被节点默认覆盖」）。注意存的是**显式值**，空串
//     就表示「跟随节点默认」——存生效值会把继承固化，与同一用例的第一/二轮相悖。
//   - stream：STDIO_PRIMARY 实例源的 LogSourceID 是 `<target>/stdout|stderr`，重建时
//     要把它原样交给 pipeline（它决定 raw 文件布局），丢了就只能猜。
//   - archive_glob：归档发现范围。实例源可按当前规则重算（recalcInstanceSourceConfig），
//     但 worker.yml 的显式源
//     重算不出来——丢了就再也发现不了那个源的历史归档。
//
// ## 向后兼容（v1）
//
// v1 只有 6 列。读方遇到 6 列行一律按「只有游标、没有源配置」处理：游标照常生效，
// 但**不**据此重建源（重建一个不知道 mode/namespace 的源，比不重建更危险）。
// 反过来，写方在源配置为 nil 时**也只写 6 列**——旧版本的读方（``len(fields) != 6`` 即跳过）
// 依旧能读它，而不是把整表当损坏丢弃。
//
// ## 单行损坏
//
// 任何一行解析不了一律 ``continue``（整表仍可用）。游标与源配置都是「可丢失的优化」：
// 丢一行最多重读一段／重采一个文件，绝不能因为一行坏而拒绝启动采集。

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// cursorRegistryHeaderV2 是当前写出的版本头。
//
// 版本头目前只用于人读与排障（读方按**列数**判定 v1/v2，不靠它）：一个文件可能由旧版本
// 写过、也可能是被手工编辑过的，靠列数判断比靠头字串宽容且不会自相矛盾。
const cursorRegistryHeaderV2 = "# jianmanager ingest cursor registry v2"

// cursorRegistrySourceColumns 是「带源配置的行」的列数（= 第 0..14 列）。
const cursorRegistrySourceColumns = 15

// cursorRegistryLegacyColumns 是 v1 行的列数（只有游标）。
const cursorRegistryLegacyColumns = 6

// parseCursorRegistryRow 解析一行（已按 TAB 切开）。
//
// 返回 ok=false 表示这一行整体不可用（调用方跳过它）。
// 返回的 entry.Source 为 nil 表示「只有游标」——既包括 v1 行，也包括「源配置列存在但
// 内容不可用」（例如 FILE_PRIMARY 却没有 path）：那种情况下宁可不重建源，也不要
// 拿半个配置去建一个会读错文件的源。
func parseCursorRegistryRow(fields []string) (string, cursorRegistryEntry, bool) {
	if len(fields) < cursorRegistryLegacyColumns {
		return "", cursorRegistryEntry{}, false
	}
	key := unescapeCursorField(fields[0])
	if key == "" {
		return "", cursorRegistryEntry{}, false
	}
	dev, err1 := strconv.ParseUint(fields[2], 10, 64)
	ino, err2 := strconv.ParseUint(fields[3], 10, 64)
	cursor, err3 := strconv.ParseInt(fields[4], 10, 64)
	offset, err4 := strconv.ParseUint(fields[5], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return "", cursorRegistryEntry{}, false
	}
	entry := cursorRegistryEntry{SourceCursor: acquire.SourceCursor{
		Path: unescapeCursorField(fields[1]), DeviceID: dev, Inode: ino,
		Cursor: cursor, LogicalPos: offset,
	}}
	// v1 行：只有 6 列 ⇒ 只恢复游标，不重建源（向后兼容，见文件头）。
	if len(fields) == cursorRegistryLegacyColumns {
		return key, entry, true
	}
	// 7..10 列是半个源配置（写方从不会产生它）⇒ 当作损坏行跳过，而不是丢一半信息建源。
	if len(fields) < 11 {
		return "", cursorRegistryEntry{}, false
	}
	source, ok := parseCursorRegistrySource(key, fields)
	if !ok {
		// 源配置列不可用：降级为「只有游标」——游标仍然是对的，没理由连它一起丢。
		return key, entry, true
	}
	entry.Source = source
	return key, entry, true
}

// parseCursorRegistrySource 从 6 列之后的字段还原源配置。
//
// ok=false 表示这些列不可用（调用方降级为「只有游标」）。
func parseCursorRegistrySource(key string, fields []string) (*SourceConfig, bool) {
	mode := pipeline.AcquireMode(unescapeCursorField(fields[6]))
	if !mode.Valid() {
		return nil, false
	}
	path := unescapeCursorField(fields[1])
	// FILE_PRIMARY 没有 path 就是一个必然在 Register 里失败（或读错地方）的源。
	if mode == pipeline.ModeFilePrimary && path == "" {
		return nil, false
	}
	logSourceID, generation := splitSourceKey(key)
	if logSourceID == "" || generation == "" {
		return nil, false
	}
	category, ok := parseSourceCategory(unescapeCursorField(fields[7]))
	if !ok {
		return nil, false
	}
	source := &SourceConfig{
		LogSourceID:      logSourceID,
		SourceGeneration: generation,
		Path:             path,
		Mode:             mode,
		SourceCategory:   category,
		StorageNamespace: unescapeCursorField(fields[8]),
		RotateTo:         unescapeCursorField(fields[9]),
		UTCDay:           unescapeCursorField(fields[10]),
	}
	// 11..14 是可选追加列：只写了 11 列的写方不会产生它们，缺了就留空。
	if len(fields) > 11 {
		source.Charset = unescapeCursorField(fields[11])
	}
	if len(fields) > 12 {
		source.TimeZone = unescapeCursorField(fields[12])
	}
	if len(fields) > 13 {
		source.Stream = unescapeCursorField(fields[13])
	}
	if len(fields) > 14 {
		source.ArchiveGlob = unescapeCursorField(fields[14])
	}
	return source, true
}

// parseSourceCategory 把盘上的类别名转回 logtypes.Source。
//
// 空串合法（= 未指定，Register 会补 instance）；**未知的非空值一律判为不可用**：
// 类别会决定 SuppressRecursiveVL（worker/node 不回采自己的日志），猜错比不重建更糟。
func parseSourceCategory(raw string) (logtypes.Source, bool) {
	switch raw {
	case "":
		return "", true
	case string(logtypes.SourceInstance):
		return logtypes.SourceInstance, true
	case string(logtypes.SourceWorker):
		return logtypes.SourceWorker, true
	case string(logtypes.SourceNode):
		return logtypes.SourceNode, true
	default:
		return "", false
	}
}

// writeCursorRegistryRow 写一行。
//
// 源配置为 nil 时只写 6 列（= v1 形态，旧读方也能读，见文件头）。
func writeCursorRegistryRow(w *bufio.Writer, key string, entry cursorRegistryEntry) error {
	c := entry.SourceCursor
	if entry.Source == nil {
		_, err := fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\n",
			escapeCursorField(key), escapeCursorField(c.Path), c.DeviceID, c.Inode, c.Cursor, c.LogicalPos)
		return err
	}
	s := entry.Source
	_, err := fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		escapeCursorField(key), escapeCursorField(c.Path), c.DeviceID, c.Inode, c.Cursor, c.LogicalPos,
		escapeCursorField(string(s.Mode)), escapeCursorField(string(s.SourceCategory)),
		escapeCursorField(s.StorageNamespace), escapeCursorField(s.RotateTo), escapeCursorField(s.UTCDay),
		escapeCursorField(s.Charset), escapeCursorField(s.TimeZone),
		escapeCursorField(s.Stream), escapeCursorField(s.ArchiveGlob))
	return err
}

// cursorFieldEscaper 转义可能出现在字段里的分隔符。
//
// 与 TAB 冲突的只有三个字符（TAB / 换行 / 反斜杠本身），转义后列数判定就永远准确：
// 路径里真出现 TAB（Windows 路径允许）也不会把一行切成更多列而让整行被当成损坏。
var cursorFieldEscaper = strings.NewReplacer(
	"\\", "\\\\",
	"\t", "\\t",
	"\n", "\\n",
	"\r", "\\r",
)

func escapeCursorField(text string) string {
	if !strings.ContainsAny(text, "\\\t\n\r") {
		return text
	}
	return cursorFieldEscaper.Replace(text)
}

func unescapeCursorField(text string) string {
	if !strings.Contains(text, "\\") {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); i++ {
		if text[i] != '\\' || i+1 >= len(text) {
			b.WriteByte(text[i])
			continue
		}
		i++
		switch text[i] {
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		default:
			b.WriteByte(text[i])
		}
	}
	return b.String()
}
