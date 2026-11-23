package logtail

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
	"github.com/wcpe/JianManager/internal/worker/logs/tailer"
)

// TestInstanceNameFromPath 锤死路径 → 实例名的推导。
//
// 推导错了会直接导致「日志写到错的源」——比查不到更难排查。
func TestInstanceNameFromPath(t *testing.T) {
	cases := []struct {
		path       string
		wantName   string
		wantStream string
	}{
		{
			path:       "/home/user/servers/game-01/logs/latest.log",
			wantName:   "game-01",
			wantStream: "stdout",
		},
		{
			path:       "/home/user/servers/login-02/logs/2026-11-05-1.log",
			wantName:   "login-02",
			wantStream: "stdout",
		},
		{
			// bungee 形态：日志在工作目录根
			path:       "/home/user/servers/bungee-main/proxy.log.0",
			wantName:   "bungee-main",
			wantStream: "stdout",
		},
		{
			path:     "/tmp/unrelated.txt",
			wantName: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			gotName, gotStream := instanceNameFromPath(tc.path)
			if gotName != tc.wantName {
				t.Errorf("实例名：期望 %q 实际 %q", tc.wantName, gotName)
			}
			if tc.wantName != "" && gotStream != tc.wantStream {
				t.Errorf("流名：期望 %q 实际 %q", tc.wantStream, gotStream)
			}
		})
	}
}

// TestInstancePathResolver 验证「实例名 → 实例ID」的映射确实生效。
func TestInstancePathResolver(t *testing.T) {
	resolve := InstancePathResolver(func(name string) (string, bool) {
		if name == "game-01" {
			return "318", true
		}
		return "", false
	})

	id, stream := resolve.Resolve("/home/user/servers/game-01/logs/latest.log")
	if id != "inst:318/stdout" {
		t.Fatalf("期望 inst:318/stdout，实际 %q", id)
	}
	if stream != "stdout" {
		t.Fatalf("期望 stream=stdout，实际 %q", stream)
	}

	// 未注册的实例：必须返回空（不得猜测）
	id2, _ := resolve.Resolve("/home/user/servers/unknown/logs/latest.log")
	if id2 != "" {
		t.Fatalf("未注册实例应返回空，实际 %q", id2)
	}
}

// TestContentIDStableAndDistinct 锤死内容指纹的两个性质。
//
// 这是将来「回补前查重」的基石：同一条日志无论采几次都要得相同 id，
// 不同日志要得不同 id。
func TestContentIDStableAndDistinct(t *testing.T) {
	a := contentID("inst:318/file", "stdout", "18:28:02", "hello")
	b := contentID("inst:318/file", "stdout", "18:28:02", "hello")
	if a != b {
		t.Fatalf("相同输入必须得到相同 id：%s vs %s", a, b)
	}

	// 内容不同
	c := contentID("inst:318/file", "stdout", "18:28:02", "hello world")
	if a == c {
		t.Fatal("内容不同时必须得到不同 id")
	}
	// 源不同
	d := contentID("inst:320/file", "stdout", "18:28:02", "hello")
	if a == d {
		t.Fatal("源不同时必须得到不同 id（否则两个实例的日志会互相去重）")
	}
	// 时间不同
	e := contentID("inst:318/file", "stdout", "18:28:03", "hello")
	if a == e {
		t.Fatal("时间不同时必须得到不同 id")
	}
}

// TestBuildRowFields 验证写入 VL 的字段完整且可解析。
//
// # 为什么 `_time` 必须是完整 RFC3339
//
// MC 日志行只写 `[18:28:02]`，不带日期也不带时区。直接把它当 `_time` 落库，
// VL 会按「接收时间所在的年-月-日 + 这个时刻」自己猜一个——回补历史文件时
// 整批数据会落到今天，时间窗查询直接失效。所以归一化的产物必须是带日期与时区的时间。
func TestBuildRowFields(t *testing.T) {
	line := tailer.Line{
		Path:    "/home/user/servers/game-01/logs/latest.log",
		Content: []byte(`[18:28:02] [Server thread/INFO]: hello world`),
	}
	sink := NewSink(nil, SourceResolverFunc(func(string) (string, string) { return "inst:318/file", "stdout" }), time.UTC)
	ts := sink.eventTime(line, "inst:318/file")
	if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
		t.Fatalf("_time 必须是可解析的 RFC3339，实际 %q: %v", ts, err)
	}
	if !strings.HasSuffix(ts, "18:28:02Z") {
		t.Errorf("_time 应保留行内时刻 18:28:02，实际 %q", ts)
	}
	row, err := buildRow(line, "inst:318/file", "stdout", ts, textInfo{text: string(line.Content), charset: normalize.CharsetUTF8})
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(row), &parsed); err != nil {
		t.Fatalf("产出的行必须是合法 JSON: %v", err)
	}
	if parsed["_time"] != ts {
		t.Errorf("_time 应为 %q，实际 %q", ts, parsed["_time"])
	}
	if !strings.Contains(parsed["_msg"], "hello world") {
		t.Errorf("_msg 应保留原文，实际 %q", parsed["_msg"])
	}
	if parsed["log_source_id"] != "inst:318/file" {
		t.Errorf("log_source_id 错：%q", parsed["log_source_id"])
	}
	if parsed["level"] != "INFO" {
		t.Errorf("level 错：%q", parsed["level"])
	}
	if parsed["event_id"] == "" {
		t.Error("event_id 不得为空")
	}
}

// TestBuildRowFallsBackToNowWhenNoTimestamp 验证「解析不出时间」时的降级路径。
//
// 不能因为解析不出时间就丢行——用户明确说过日志不重要、有就行。
func TestBuildRowFallsBackToNowWhenNoTimestamp(t *testing.T) {
	line := tailer.Line{
		Path:    "/home/user/servers/game-01/logs/latest.log",
		Content: []byte(`	at SomeClass.method(SomeClass.java:119)`),
	}
	sink := NewSink(nil, nil, time.UTC)
	ts := sink.eventTime(line, "inst:318/file")
	if ts == "" {
		t.Fatal("解析不出时间时必须用当前时间兜底，不能为空")
	}
	if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
		t.Errorf("兜底时间应为 RFC3339，实际 %q", ts)
	}
	row, err := buildRow(line, "inst:318/file", "stdout", ts, textInfo{text: string(line.Content), charset: normalize.CharsetUTF8})
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]string
	_ = json.Unmarshal([]byte(row), &parsed)
	if parsed["_time"] != ts {
		t.Errorf("_time 应为 %q，实际 %q", ts, parsed["_time"])
	}
}

// TestSinkSkipsUnresolvedSource 验证「无法识别源」时跳过而不是写错标签。
func TestSinkSkipsUnresolvedSource(t *testing.T) {
	s := NewSink(nil, SourceResolverFunc(func(string) (string, string) { return "", "" }), time.UTC)
	lines := []tailer.Line{{Path: "/unknown/path.log", Content: []byte("hello")}}
	if err := s.Consume(context.Background(), lines); err != nil {
		// client 为 nil，但因为所有行都被跳过，不应走到写入
		t.Fatalf("全部跳过时不应报错：%v", err)
	}
	st := s.Snapshot()
	if st.UnresolvedSource != 1 {
		t.Errorf("应记 1 次无法识别源，实际 %d", st.UnresolvedSource)
	}
	if st.Written != 0 {
		t.Errorf("不应写入任何行，实际 %d", st.Written)
	}
}
