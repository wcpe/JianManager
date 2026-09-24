package vlsup

import (
	"errors"
	"testing"
)

func TestIndependentSinkDoesNotWriteVL(t *testing.T) {
	var s IndependentLoggerSink
	if s.WritesIntoVL() {
		t.Fatal("independent logger sink must not write into VL")
	}
	// nil logger 不应 panic
	s.ReportFailure("hot", errors.New("x"))
}

func TestReportViaSinkAllowsIndependent(t *testing.T) {
	rec := &RecordingSink{IntoVL: false}
	err := errors.New("proc fail")
	blocked := reportViaSink(rec, false, "hot", err)
	if blocked {
		t.Fatal("independent sink must not be blocked")
	}
	if rec.Count() != 1 {
		t.Fatalf("expected 1 report, got %d", rec.Count())
	}
}

func TestGuardBlocksRecursiveVLSink(t *testing.T) {
	rec := &RecordingSink{IntoVL: true}
	err := errors.New("vl write fail")
	// 生产守卫标志 false：拒绝递归写入 VL
	blocked := reportViaSink(rec, false, "hot", err)
	if !blocked {
		t.Fatal("guard must block VL pipeline sink when AllowVLRecursiveSink=false")
	}
	if rec.Count() != 0 {
		t.Fatalf("blocked sink must not receive report, got %d", rec.Count())
	}
}

func TestGuardFlagAllowsWhenExplicitlyEnabled(t *testing.T) {
	// 仅测试：显式打开守卫标志时允许（生产禁止）
	rec := &RecordingSink{IntoVL: true}
	blocked := reportViaSink(rec, true, "cold", errors.New("x"))
	if blocked {
		t.Fatal("explicit AllowVLRecursiveSink=true should not block")
	}
	if rec.Count() != 1 {
		t.Fatalf("expected report delivered, got %d", rec.Count())
	}
}

func TestErrRecursiveVLSinkDefined(t *testing.T) {
	if ErrRecursiveVLSink == nil {
		t.Fatal("ErrRecursiveVLSink must be defined")
	}
}
