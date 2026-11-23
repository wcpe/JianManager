package vlsup

import (
	"context"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/platform/logasset"
)

// 合法长期单位必须既能启动批准引擎，也能在派生总窗内查询；不把纯parser绿当CLI合同。
func TestApprovedVLRuntimeDurationCLIAndQueryWindow(t *testing.T) {
	bin := os.Getenv("JM_VL_BIN")
	if bin == "" {
		t.Skip("本地批准二进制未指定")
	}
	var err error
	bin, err = filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyAsset(bin, logasset.LinuxExecutableSHA256); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"12M", "12", "30w", "2y", "1.1d", ".5", ".5M"} {
		t.Run(raw, func(t *testing.T) {
			ln, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			port := ln.Addr().(*net.TCPAddr).Port
			_ = ln.Close()
			cfg := InstanceConfig{Namespace: NamespaceHot, Port: port, StorageDataPath: t.TempDir(), RetentionPeriod: raw, MemoryAllowedBytes: 128 << 20, AuthUsername: "constructed", AuthPassword: "constructed-cli-secret"}
			args, e := BuildArgs(cfg)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			t.Cleanup(cancel)
			p, e := (RealFactory{Env: []string{"GOMEMLIMIT=268435456B", "GOMAXPROCS=2"}, DiagnosticSink: &RecordingSink{}}).Start(ctx, bin, args)
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() {
				_ = p.Stop(5 * time.Second)
				v := p.(ExecHandle).Snapshot()
				if !v.Exited || !v.ExitCodeKnown || v.ExitCode != 0 {
					t.Errorf("批准引擎未干净退出: %+v", v)
				}
			})
			c, e := NewClient(ClientOptions{BaseURL: "http://127.0.0.1:" + strconv.Itoa(port), Username: cfg.AuthUsername, Password: cfg.AuthPassword})
			if e != nil {
				t.Fatal(e)
			}
			for {
				if e = c.Health(ctx); e == nil {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatalf("CLI参数被引擎拒绝: %v snapshot=%+v", e, p.(ExecHandle).Snapshot())
				case <-time.After(10 * time.Millisecond):
				}
			}
			limit, e := cfg.QueryTimeRangeLimitChecked()
			if e != nil {
				t.Fatal(e)
			}
			span, e := time.ParseDuration(limit)
			if e != nil {
				t.Fatal(e)
			}
			end := time.Now().UTC().Truncate(time.Second)
			start := end.Add(-span)
			values := url.Values{"query": {"*"}, "start": {start.Format(time.RFC3339Nano)}, "end": {end.Format(time.RFC3339Nano)}, "limit": {"1"}}
			if _, e = c.Get(ctx, "/select/logsql/query", values); e != nil {
				t.Fatalf("合法精确派生窗口不能被拒绝: raw=%s limit=%s err=%v", raw, limit, e)
			}
			values.Set("start", start.Add(-time.Millisecond).Format(time.RFC3339Nano))
			if _, e = c.Get(ctx, "/select/logsql/query", values); e == nil {
				t.Fatal("派生上限外窗口必须由批准引擎拒绝")
			}
			t.Logf("retention=%s derived=%s exact_window=accepted plus_1ms=rejected", raw, limit)
		})
	}
}
