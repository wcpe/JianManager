package process

import (
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func mustGBK(t *testing.T, s string) []byte {
	b, _, err := transform.Bytes(simplifiedchinese.GBK.NewEncoder(), []byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGbkToUTF8_ValidUTF8Untouched(t *testing.T) {
	in := []byte("已解析 BC 实例 正常中文")
	if out := gbkToUTF8(in); string(out) != string(in) {
		t.Fatalf("合法 UTF-8 被改动: %q", out)
	}
}

func TestGbkToUTF8_GBKDecoded(t *testing.T) {
	got := string(gbkToUTF8(mustGBK(t, "[Lodestone] 已解析 BC 实例")))
	if got != "[Lodestone] 已解析 BC 实例" {
		t.Fatalf("GBK 未解码: %q", got)
	}
}

func TestGbkToUTF8_CorruptBytesPassthrough(t *testing.T) {
	in := []byte{0xff, 0xfe, 0x01, 0x02, 0x00}
	out := gbkToUTF8(in)
	if string(out) != string(in) {
		t.Fatalf("损坏字节被错误解码: %q", out)
	}
}
