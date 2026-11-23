package vlsup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// 本组回归锁定合法运行时单位、短命进程以及默认故障落点，不能靠回退窗口掩盖错误。
func TestRuntimeRegressionQueryWindow(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"12M", "8952h"}, {"12", "8952h"}, {"30w", "5064h"}, {"2y", "17544h"}, {"1.1d", "50h24m0s"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got := (InstanceConfig{RetentionPeriod: tc.raw}).QueryTimeRangeLimit()
			gd, ge := time.ParseDuration(got)
			wd, we := time.ParseDuration(tc.want)
			if ge != nil || we != nil || gd != wd {
				t.Fatalf("合法保留期派生缩短: %q got=%q want=%q", tc.raw, got, tc.want)
			}
		})
	}
	cfg := InstanceConfig{Namespace: NamespaceHot, Port: DefaultPortHot, StorageDataPath: t.TempDir(), RetentionPeriod: "NaNd", AuthUsername: "jm", AuthPassword: "secret"}
	if _, err := BuildArgs(cfg); err == nil {
		t.Fatal("非法保留期不得静默产生短窗参数")
	}
}

func TestRuntimeRegressionShortLivedProcessNeverBecomesStableAfterDeath(t *testing.T) {
	factory := &fakeFactory{}
	s := newTestSupervisor(t, factory, &RecordingSink{}, false)
	if err := s.Start(context.Background(), NamespaceHot); err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock()
	clock.Advance(800 * time.Millisecond)
	factory.procs[0].exitImmediately()
	s.checkOnce(context.Background(), guardianTestOptions(clock))
	clock.Advance(31 * time.Second)
	s.checkOnce(context.Background(), guardianTestOptions(clock))
	if factory.startCount() != 1 {
		t.Fatal("只活800ms的进程死亡后经过31s，不得获得稳定存活资格")
	}
}

func TestRuntimeRegressionDefaultSinkPreservesRootCause(t *testing.T) {
	var out bytes.Buffer
	before := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, nil)))
	t.Cleanup(func() { slog.SetDefault(before) })
	IndependentLoggerSink{}.ReportFailure("hot", errors.New("constructed argument rejected"))
	if !bytes.Contains(out.Bytes(), []byte("argument rejected")) {
		t.Fatal("默认独立落点不得丢弃根因")
	}
}

func TestRuntimeRegressionOrphanPublicMetadataHasNoCommandLine(t *testing.T) {
	typeOf := OrphanMatch{}
	data, err := json.Marshal(typeOf)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bytes.ToLower(data), []byte("cmdline")) {
		t.Fatal("孤儿公开匹配结果不得携带完整命令行字段")
	}
}

func TestRuntimeRegressionDiskBudgetIsNotRSSBudget(t *testing.T) {
	verdict := EvaluateBudget(CacheBudget{TotalWorkerLogBudgetBytes: 100}, BudgetSample{ProcessRSSBytes: 101, DiskUsagePercent: 5}, DefaultBudgetThresholds())
	if verdict.State != BudgetOK {
		t.Fatalf("磁盘字节预算不能拿来比较RSS: %+v", verdict)
	}
}
