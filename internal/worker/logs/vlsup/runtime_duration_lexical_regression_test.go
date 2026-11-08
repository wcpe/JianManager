package vlsup

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/platform/logasset"
)

func TestRuntimeRegressionLeadingDotUnitRejectedBeforeExec(t *testing.T) {
	for _, raw := range []string{".5y", "1d.5h"} {
		cfg := baseCfg(NamespaceHot)
		cfg.RetentionPeriod = raw
		if _, err := BuildArgs(cfg); err == nil {
			t.Errorf("批准CLI不接受的单位词法必须启动前拒绝: %q", raw)
		}
	}
	for _, raw := range []string{".5", ".5M"} {
		cfg := baseCfg(NamespaceHot)
		cfg.RetentionPeriod = raw
		if _, err := BuildArgs(cfg); err != nil {
			t.Errorf("裸月/M是不同的合法解析路径，不得一刀切: %q %v", raw, err)
		}
	}
}

// 原始CLI参数刻意绕过BuildArgs，只为核验批准引擎的真实词法，不套用未核版本缓存结论。
func TestApprovedVLRuntimeLeadingDotUnitCLIContract(t *testing.T) {
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
	for _, raw := range []string{".5y", "1d.5h"} {
		t.Run(raw, func(t *testing.T) {
			ln, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			port := ln.Addr().(*net.TCPAddr).Port
			_ = ln.Close()
			cfg := baseCfg(NamespaceHot)
			cfg.StorageDataPath = t.TempDir()
			cfg.Port = port
			cfg.MemoryAllowedBytes = 128 << 20
			args, e := BuildArgs(cfg)
			if e != nil {
				t.Fatal(e)
			}
			for i, a := range args {
				if strings.HasPrefix(a, "-retentionPeriod=") {
					args[i] = "-retentionPeriod=" + raw
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			t.Cleanup(cancel)
			p, e := (RealFactory{DiagnosticSink: &RecordingSink{}, Env: []string{"GOMEMLIMIT=268435456B", "GOMAXPROCS=2"}}).Start(ctx, bin, args)
			if e != nil {
				t.Fatal(e)
			}
			h := p.(ExecHandle)
			t.Cleanup(func() { _ = h.Stop(time.Second) })
			deadline := time.Now().Add(2 * time.Second)
			for !h.Exited() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if !h.Exited() {
				t.Fatalf("批准CLI未拒绝该负控，不能用context到期强杀冒充词法拒绝: %q", raw)
			}
			v := awaitDiagnosticExit(t, h)
			if !v.ExitCodeKnown || v.ExitCode == 0 {
				t.Fatalf("批准CLI预期拒绝负控，却接受了: raw=%s snapshot=%+v", raw, v)
			}
			t.Logf("raw_retention=%s approved=%s CLI_exit=%d rejected=true cause=%s", raw, logasset.LinuxExecutableSHA256, v.ExitCode, strings.Join(v.Diagnostics, " | "))
		})
	}
}
