package vlsup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/platform/logasset"
)

// 参数拒绝必须来自真正批准引擎，而不是只用夹具打印同形错误。
func TestApprovedVLRuntimeRejectedFlagRetainsCauseAndExitCode(t *testing.T) {
	bin := os.Getenv("JM_VL_BIN")
	if bin == "" {
		t.Skip("本地批准二进制未指定")
	}
	bin, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyAsset(bin, logasset.LinuxExecutableSHA256); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	sink := &RecordingSink{}
	p, err := (RealFactory{DiagnosticSink: sink}).Start(ctx, bin, []string{"-constructed.invalid.flag=1", "-httpAuth.username=constructed", "-httpAuth.password=constructed-cli-reject-secret"})
	if err != nil {
		t.Fatal(err)
	}
	h := p.(ExecHandle)
	t.Cleanup(func() { _ = h.Stop(time.Second) })
	v := awaitDiagnosticExit(t, h)
	all := strings.Join(v.Diagnostics, " ")
	for _, r := range sink.Snapshot() {
		all += " " + r.Err.Error()
	}
	if !v.ExitCodeKnown || v.ExitCode != 2 || !strings.Contains(all, "flag provided but not defined") {
		t.Fatalf("批准引擎参数根因/退出码丢失: code=%d known=%v diagnostics=%s", v.ExitCode, v.ExitCodeKnown, all)
	}
	if strings.Contains(all, "constructed-cli-reject-secret") {
		t.Fatal("真实参数拒绝不能泄露CLI秘密")
	}
	t.Logf("approved=%s exit=%d cause=flag_provided_but_not_defined redacted=true", logasset.LinuxExecutableSHA256, v.ExitCode)
}
