package crashreport

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestTail_Boundaries 尾部截取边界（FR-313 spec §5）：不足 N 行取全部、超出只留最后 N 行、
// 结尾换行不计行、字节上限先裁、空输入与零上限的退化行为。
func TestTail_Boundaries(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		maxLines int
		maxBytes int
		want     string
	}{
		{"空输入", "", 200, 1024, ""},
		{"不足 N 行取全部", "a\nb\nc", 200, 1024, "a\nb\nc"},
		{"恰好 N 行取全部", "a\nb\nc", 3, 1024, "a\nb\nc"},
		{"超出取最后 N 行", "a\nb\nc\nd\ne", 2, 1024, "d\ne"},
		{"结尾换行不计为一行", "a\nb\nc\n", 2, 1024, "b\nc\n"},
		{"单行无换行", "only-line", 1, 1024, "only-line"},
		{"maxLines<=0 不按行限制", "a\nb\nc", 0, 1024, "a\nb\nc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Tail([]byte(tt.data), tt.maxLines, tt.maxBytes))
		})
	}
}

// TestTail_ByteCap 字节上限先于行数截取：超长单行被裁到最后 maxBytes 字节。
func TestTail_ByteCap(t *testing.T) {
	data := strings.Repeat("x", 100) + "TAIL"
	got := Tail([]byte(data), 200, 8)
	assert.Equal(t, "xxxxTAIL", got, "应保留最后 8 字节")
}

// TestTail_ByteCapThenLines 字节裁剪后再按行截取：行数从裁剪后的窗口内数。
func TestTail_ByteCapThenLines(t *testing.T) {
	// 500 行，每行 "line-i\n"；字节上限只覆盖尾部一小段，再取最后 3 行。
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString("line\n")
	}
	got := Tail([]byte(b.String()), 3, 64)
	assert.Equal(t, "line\nline\nline\n", got)
}

// TestTail_DefaultLimits 默认上限常量与 spec 写死值一致（N=200 / 64KB）。
func TestTail_DefaultLimits(t *testing.T) {
	assert.Equal(t, 200, DefaultTailLines)
	assert.Equal(t, 64*1024, DefaultTailBytes)
}
