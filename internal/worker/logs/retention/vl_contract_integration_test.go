package retention

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/platform/logasset"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 本测试仅对全新构造目录开启delete；必须先核准本地批准二进制，不复用生产端口/数据。
func TestApprovedVLDeleteContractExactNanosecondSet(t *testing.T) {
	bin := os.Getenv("JM_VL_BIN")
	if bin == "" {
		t.Skip("本地批准二进制未指定")
	}
	bin, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err = vlsup.VerifyAsset(bin, logasset.LinuxExecutableSHA256); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	root := t.TempDir()
	cfg := vlsup.InstanceConfig{Namespace: vlsup.NamespaceHot, Port: port, StorageDataPath: root, RetentionPeriod: "30d", MemoryAllowedBytes: 128 << 20, AuthUsername: "constructed-user", AuthPassword: "constructed-contract-secret", EnableDelete: true}
	args, err := vlsup.BuildArgs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	p, err := (vlsup.RealFactory{Env: []string{"GOMEMLIMIT=268435456B", "GOMAXPROCS=2"}, DiagnosticSink: &vlsup.RecordingSink{}}).Start(ctx, bin, args)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Stop(5 * time.Second); err != nil {
			t.Error(err)
		}
		if h, ok := p.(vlsup.ExecHandle); ok {
			v := h.Snapshot()
			t.Logf("隔离批准VL退出: pid=%d code=%d known=%v exited=%v", p.PID(), v.ExitCode, v.ExitCodeKnown, v.Exited)
			if !v.Exited || !v.ExitCodeKnown || v.ExitCode != 0 {
				t.Errorf("隔离引擎未干净退出: %+v", v)
			}
		}
	})
	c, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: "http://127.0.0.1:" + strconv.Itoa(port), Username: cfg.AuthUsername, Password: cfg.AuthPassword})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if err = c.Health(ctx); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("引擎未就绪: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cutoff := time.Now().UTC().Add(-time.Hour).Truncate(time.Second).Add(123456789 * time.Nanosecond)
	type row struct {
		ID, Source, Level string
		At                time.Time
	}
	rows := []row{{"older", "inst:contract", "DEBUG", cutoff.Add(-time.Hour)}, {"before", "inst:contract", "DEBUG", cutoff.Add(-time.Nanosecond)}, {"at", "inst:contract", "DEBUG", cutoff}, {"after", "inst:contract", "DEBUG", cutoff.Add(time.Nanosecond)}, {"other-source", "inst:other", "DEBUG", cutoff.Add(-time.Nanosecond)}, {"other-level", "inst:contract", "ERROR", cutoff.Add(-time.Nanosecond)}}
	// 合法来源标识中的标点会形成同形短语；来源和级别都必须按整个值精确匹配。
	rows = append(rows, row{"source-suffix", "inst:contract-extra", "DEBUG", cutoff.Add(-time.Nanosecond)}, row{"source-prefix", "extra:inst:contract", "DEBUG", cutoff.Add(-time.Nanosecond)}, row{"source-colon", "inst:contract:extra", "DEBUG", cutoff.Add(-time.Nanosecond)}, row{"source-dot", "inst:contract.extra", "DEBUG", cutoff.Add(-time.Nanosecond)}, row{"unexpected-level", "inst:contract", "DEBUG-EXTRA", cutoff.Add(-time.Nanosecond)})
	var body bytes.Buffer
	for _, r := range rows {
		encoded, e := json.Marshal(map[string]string{"_time": r.At.Format(time.RFC3339Nano), "_msg": r.ID, "event_id": r.ID, "log_source_id": r.Source, "level": r.Level})
		if e != nil {
			t.Fatal(e)
		}
		body.Write(encoded)
		body.WriteByte('\n')
	}
	if _, err = c.InsertJSONLines(ctx, body.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Post(ctx, "/internal/force_flush", nil); err != nil {
		t.Fatal(err)
	}
	// 批准引擎删除可能留下仅_time/_stream的内部空占位；业务集合按构造来源字段识别，
	// 不把无来源占位当用户日志，也不忽略任何仍保留原来源/级别的构造记录。
	readIDs := func() []string {
		t.Helper()
		data, e := c.Get(ctx, "/select/logsql/query", url.Values{"query": {"log_source_id:*"}, "start": {cutoff.Add(-2 * time.Hour).Format(time.RFC3339Nano)}, "end": {cutoff.Add(time.Hour).Format(time.RFC3339Nano)}, "limit": {"100"}})
		if e != nil {
			t.Fatal(e)
		}
		var ids []string
		scan := bufio.NewScanner(bytes.NewReader(data))
		for scan.Scan() {
			var record map[string]string
			if e = json.Unmarshal(scan.Bytes(), &record); e != nil {
				t.Fatal(e)
			}
			if record["event_id"] == "" {
				t.Logf("构造查询中的空event_id记录: %s", scan.Bytes())
			}
			ids = append(ids, record["event_id"])
		}
		if e = scan.Err(); e != nil {
			t.Fatal(e)
		}
		sort.Strings(ids)
		return ids
	}
	wantBefore := []string{"after", "at", "before", "older", "other-level", "other-source", "source-colon", "source-dot", "source-prefix", "source-suffix", "unexpected-level"}
	if got := readIDs(); !reflect.DeepEqual(got, wantBefore) {
		t.Fatalf("构造输入未逐条可见，不得执行删除: got=%v want=%v", got, wantBefore)
	}
	id, err := (ClientDeleter{Client: c}).RunDeleteTask(ctx, `level:="DEBUG" AND log_source_id:="inst:contract"`, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	for {
		data, e := c.Get(ctx, "/delete/active_tasks", nil)
		if e != nil {
			t.Fatal(e)
		}
		var tasks []struct {
			ID string `json:"task_id"`
		}
		if e = json.Unmarshal(data, &tasks); e != nil {
			t.Fatal(e)
		}
		active := false
		for _, task := range tasks {
			if task.ID == id {
				active = true
			}
		}
		if !active {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("异步删除任务未完成")
		case <-time.After(10 * time.Millisecond):
		}
	}
	wantAfter := []string{"after", "at", "other-level", "other-source", "source-colon", "source-dot", "source-prefix", "source-suffix", "unexpected-level"}
	if got := readIDs(); !reflect.DeepEqual(got, wantAfter) {
		t.Fatalf("真实批准引擎半开截止/来源/级别隔离错误: got=%v want=%v", got, wantAfter)
	}
	t.Logf("批准SHA=%s port=%d cutoff=%s input=%v exact_remaining=%v task=%s completed=true", logasset.LinuxExecutableSHA256, port, cutoff.Format(time.RFC3339Nano), wantBefore, wantAfter, id)
}
