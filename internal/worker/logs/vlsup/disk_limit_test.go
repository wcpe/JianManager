package vlsup

// 磁盘占用上限（2026-10-29 用户要求「加磁盘上限」）的回归。
//
// 背景：此前受管 VL 只有 `-retentionPeriod`，磁盘用量 = 保留期 × 实际写入量，
// 没有任何上限。VL 实际提供**两种**磁盘上限且**互斥**（`-help` 原文：
// "mutually exclusive with"）：
//   - `-retention.maxDiskSpaceUsageBytes`（绝对字节数）
//   - `-retention.maxDiskUsagePercent`（磁盘使用百分比 1-100）
//
// 本文件钉四件事：
//   1. 配了就出现在命令行里；
//   2. 没配（0）就不出现（保持改动前行为）；
//   3. **两个都配要拒绝**（不静默选一个）；
//   4. 与 -retentionPeriod **同时**存在（语义互补，不是替代）。
//
// 转红方式：从 BuildArgs 里删掉对应 append ⇒ 用例 1 立红；
// 从 Validate 里删掉互斥检查 ⇒ 用例 3 立红。

import (
	"strings"
	"testing"
)

func diskLimitBase() InstanceConfig {
	return InstanceConfig{
		Namespace:       NamespaceHot,
		Port:            19461,
		StorageDataPath: "/tmp/vl",
		RetentionPeriod: "180d",
		AuthUsername:    "u",
		AuthPassword:    "p",
	}
}

func hasArg(args []string, prefix string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

// 配了字节上限 ⇒ 命令行必须带上，且值与配置逐字一致。
func TestBuildArgsIncludesMaxDiskSpaceBytes(t *testing.T) {
	cfg := diskLimitBase()
	cfg.MaxDiskSpaceUsageBytes = 20 << 30 // 20 GiB
	args, err := BuildArgs(cfg)
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	want := "-retention.maxDiskSpaceUsageBytes=21474836480"
	if !hasArg(args, want) {
		t.Fatalf("缺少 %q；实际参数：%v", want, args)
	}
}

// 配了百分比上限 ⇒ 命令行必须带上。
func TestBuildArgsIncludesMaxDiskUsagePercent(t *testing.T) {
	cfg := diskLimitBase()
	cfg.MaxDiskUsagePercent = 85
	args, err := BuildArgs(cfg)
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	if !hasArg(args, "-retention.maxDiskUsagePercent=85") {
		t.Fatalf("缺少百分比上限；实际参数：%v", args)
	}
}

// 都不配（零值）⇒ 两个参数都不出现（改动前行为逐字不变）。
func TestBuildArgsOmitsDiskLimitWhenUnset(t *testing.T) {
	args, err := BuildArgs(diskLimitBase())
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	if hasArg(args, "-retention.maxDiskSpaceUsageBytes") {
		t.Fatalf("未配置时不得出现字节上限：%v", args)
	}
	if hasArg(args, "-retention.maxDiskUsagePercent") {
		t.Fatalf("未配置时不得出现百分比上限：%v", args)
	}
}

// 两个都配 ⇒ 必须拒绝（VL 官方互斥）。
//
// 为什么不“选一个先用”：静默选一个会让磁盘上限的真值取决于我们而不是运维，
// 而这件事只在磁盘满的那一刻才暴露。
func TestValidateRejectsBothDiskLimits(t *testing.T) {
	cfg := diskLimitBase()
	cfg.MaxDiskSpaceUsageBytes = 20 << 30
	cfg.MaxDiskUsagePercent = 85
	err := cfg.Validate()
	if err == nil {
		t.Fatal("两者同时配置时必须拒绝启动")
	}
	if !strings.Contains(err.Error(), "互斥") {
		t.Fatalf("错误文案应说明互斥：%v", err)
	}
}

// 负值与越界百分比 ⇒ 必须拒绝。
func TestValidateRejectsBadDiskLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*InstanceConfig)
	}{
		{"负字节", func(c *InstanceConfig) { c.MaxDiskSpaceUsageBytes = -1 }},
		{"负百分比", func(c *InstanceConfig) { c.MaxDiskUsagePercent = -1 }},
		{"百分比超100", func(c *InstanceConfig) { c.MaxDiskUsagePercent = 101 }},
	} {
		cfg := diskLimitBase()
		tc.mut(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%s 必须被拒绝", tc.name)
		}
	}
}

// 磁盘上限与保留期是**互补**的两个维度，必须同时存在。
//
// 这一条防的是「后人以为磁盘上限可以替代保留期」——它不是：
// 保留期管“最老留多久”，磁盘上限管“总共能占多少”，谁先到谁触发。
func TestDiskLimitCoexistsWithRetentionPeriod(t *testing.T) {
	cfg := diskLimitBase()
	cfg.MaxDiskSpaceUsageBytes = 20 << 30
	args, err := BuildArgs(cfg)
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	if !hasArg(args, "-retentionPeriod=180d") {
		t.Fatalf("保留期必须同时存在：%v", args)
	}
	if !hasArg(args, "-retention.maxDiskSpaceUsageBytes=") {
		t.Fatalf("磁盘上限必须同时存在：%v", args)
	}
}
