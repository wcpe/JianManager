package main

import "testing"

func TestTruncatePreservesUTF8(t *testing.T) {
	got := truncate("北京节点测试", 4)
	if got != "北京节…" {
		t.Fatalf("截断结果错误: %q", got)
	}
}
