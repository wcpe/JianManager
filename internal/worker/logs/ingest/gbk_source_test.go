package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 缺陷 B 的端到端回归：真实文件 → FileTailer → 归一化 → 投递。
//
// 单元级判据见 normalize/charset_test.go；本文件证明**采集接线**同样成立
// （SourceConfig.Charset 生效，且投递到 VL 的内容是正确 UTF-8）。
//
// 转红：改动前投递内容里是 U+FFFD 乱码（现场表现：`[Lodestone] �Ѱַ�ѽ� BC �ʵ�`）→ 必红。
func TestGBKSourceIsDecodedBeforeDelivery(t *testing.T) {
	client, fixture := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	lines := "[23:54:02] [Server thread/INFO]: [Lodestone] 已解析 BC 实例\n" +
		"[23:54:03] [Server thread/INFO]: 结束\n"
	encoded, _, err := transform.String(simplifiedchinese.GBK.NewEncoder(), lines)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(logPath, []byte(encoded), 0o644))

	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:gbk/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:gbk", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	m.pollOnce()

	key := source.LogSourceID + "/" + source.SourceGeneration
	events := durableEvents(t, m, key)
	require.NotEmpty(t, events)
	require.Equal(t, "[23:54:02] [Server thread/INFO]: [Lodestone] 已解析 BC 实例", events[0].Message,
		"GBK 字节必须被解码成正确 UTF-8，而不是替换字符")
	require.Equal(t, "gb18030", events[0].Fields[normalize.FieldSourceCharset])
	require.NotContains(t, events[0].Fields, normalize.FieldEncodingSanitized)

	fixture.inner.mu.Lock()
	payload := string(fixture.inner.writes[0])
	fixture.inner.mu.Unlock()
	require.Contains(t, payload, "已解析 BC 实例", "投递到 VL 的正文必须是正确 UTF-8")
	require.NotContains(t, payload, "\ufffd", "投递内容不得含替换字符")
}

// TestGBKSourceAutoDetectedWithoutExplicitConfig 覆盖「未显式配置也能按源检测」：
// 现场暴露时并没有 charset 配置，运维期望的是平台自己识别中文 locale 的 JVM 输出。
func TestGBKSourceAutoDetectedWithoutExplicitConfig(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	encoded, _, err := transform.String(simplifiedchinese.GBK.NewEncoder(),
		"[23:54:02] [Server thread/INFO]: 自动检测中文\n[23:54:03] [Server thread/INFO]: 结束\n")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(logPath, []byte(encoded), 0o644))

	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:gbk-auto/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:gbk-auto", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	m.pollOnce()

	events := durableEvents(t, m, source.LogSourceID+"/"+source.SourceGeneration)
	require.NotEmpty(t, events)
	require.Equal(t, "[23:54:02] [Server thread/INFO]: 自动检测中文", events[0].Message)
	require.Equal(t, "gb18030", events[0].Fields[normalize.FieldSourceCharset])
}

// TestUTF8SourceUnaffectedByCharsetDetection 守住对照面：同机 Beacon 的 UTF-8 日志必须逐字节不变。
func TestUTF8SourceUnaffectedByCharsetDetection(t *testing.T) {
	client, fixture := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[23:54:02] [Server thread/INFO]: 正常 UTF-8 日志 🚀\n[23:54:03] [Server thread/INFO]: 结束\n"), 0o644))

	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:utf8/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:utf8", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	m.pollOnce()

	events := durableEvents(t, m, source.LogSourceID+"/"+source.SourceGeneration)
	require.NotEmpty(t, events)
	require.Equal(t, "[23:54:02] [Server thread/INFO]: 正常 UTF-8 日志 🚀", events[0].Message)
	require.NotContains(t, events[0].Fields, normalize.FieldSourceCharset, "UTF-8 源不得被标记为已转码")
	require.NotContains(t, events[0].Fields, normalize.FieldEncodingSanitized)

	fixture.inner.mu.Lock()
	payload := string(fixture.inner.writes[0])
	fixture.inner.mu.Unlock()
	require.Contains(t, payload, "正常 UTF-8 日志 🚀")
}
