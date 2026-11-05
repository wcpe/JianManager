package vlsup

import (
	"strings"
	"testing"
)

// TestParseStreamsSplitsLabels 锤死「原始串 → (源, 流)」的拆分。
//
// 拆错的后果不报错、只表现为前端筛选项为空，必须能被用例直接抓住。
func TestParseStreamsSplitsLabels(t *testing.T) {
	body := `{"values":[{"value":"{log_source_id=\"inst:318/file\",stream=\"stdout\"}","hits":7378}]}`
	streams, complete, err := parseStreams([]byte(body))
	if err != nil {
		t.Fatalf("parseStreams: %v", err)
	}
	if !complete {
		t.Fatalf("有非零 hits 时必须 complete=true")
	}
	if len(streams) != 1 {
		t.Fatalf("期望 1 条 stream，得到 %d", len(streams))
	}
	got := streams[0]
	if got.LogSourceID != "inst:318/file" {
		t.Fatalf("log_source_id 拆分错: %q", got.LogSourceID)
	}
	if got.StreamName != "stdout" {
		t.Fatalf("stream 拆分错: %q", got.StreamName)
	}
	if got.Hits != 7378 {
		t.Fatalf("hits 错: %d", got.Hits)
	}
	if !strings.HasPrefix(got.Raw, "{log_source_id=") {
		t.Fatalf("raw 必须无損保留，得到 %q", got.Raw)
	}
}

// TestParseStreamsAllZeroMeansTruncated 锤死「有项但全零 ⇒ 上游截断」。
//
// 这是实测到的 limit 陷阱：limit < 流数时 VL 把**所有** hits 静默归零。
// 若把这种响应当成「这些流真的没数据」，用户会看到「21 个流全是 0 条」。
func TestParseStreamsAllZeroMeansTruncated(t *testing.T) {
	body := `{"values":[
		{"value":"{log_source_id=\"inst:320/file\",stream=\"stdout\"}","hits":0},
		{"value":"{log_source_id=\"inst:325/file\",stream=\"stdout\"}","hits":0}
	]}`
	streams, complete, err := parseStreams([]byte(body))
	if err != nil {
		t.Fatalf("parseStreams: %v", err)
	}
	if len(streams) != 2 {
		t.Fatalf("期望 2 条 stream，得到 %d", len(streams))
	}
	if complete {
		t.Fatalf("有项但全零必须判 complete=false（那是截断形态，不是「没数据」）")
	}
}

// TestParseStreamsEmptyIsComplete 对照：真实空窗返回零项，且那是**确定的**空。
//
// 与 TestParseStreamsAllZeroMeansTruncated 成对：两者前者=n项全零、后者=0项，
// 语义必须分开。若把 0 项也判成 truncation，空窗会被误报成「数据不完整」。
func TestParseStreamsEmptyIsComplete(t *testing.T) {
	streams, complete, err := parseStreams([]byte(`{"values":[]}`))
	if err != nil {
		t.Fatalf("parseStreams: %v", err)
	}
	if len(streams) != 0 {
		t.Fatalf("期望 0 条，得到 %d", len(streams))
	}
	if !complete {
		t.Fatalf("零项是确定的空，必须 complete=true")
	}
}

// TestParseStreamsSortedByHitsDesc 确认顺序：条数降序。
//
// 前端直接渲染返回顺序，若这里不排，用户看到的是 VL 的内部顺序（实际是 hits 升序），
// 最关心的「日志最多的源」会排在最后。
func TestParseStreamsSortedByHitsDesc(t *testing.T) {
	body := `{"values":[
		{"value":"{log_source_id=\"a/file\",stream=\"stdout\"}","hits":5},
		{"value":"{log_source_id=\"b/file\",stream=\"stdout\"}","hits":900},
		{"value":"{log_source_id=\"c/file\",stream=\"stdout\"}","hits":70}
	]}`
	streams, _, err := parseStreams([]byte(body))
	if err != nil {
		t.Fatalf("parseStreams: %v", err)
	}
	want := []string{"b/file", "c/file", "a/file"}
	for i, w := range want {
		if streams[i].LogSourceID != w {
			t.Fatalf("第 %d 项期望 %q，得到 %q", i, w, streams[i].LogSourceID)
		}
	}
}

// TestParseStreamsMalformedJSON 不把解析失败伪装成「空结果」。
func TestParseStreamsMalformedJSON(t *testing.T) {
	if _, _, err := parseStreams([]byte(`not json`)); err == nil {
		t.Fatal("非 JSON 必须报错，不得返回空结果")
	}
}

// TestParseStreamLabelsEdgeCases 确认异常形态不会 panic、也不会凭空造值。
func TestParseStreamLabelsEdgeCases(t *testing.T) {
	cases := []struct {
		raw   string
		key   string
		want  string
		empty bool
	}{
		{raw: `{log_source_id="x/y",stream="stdout"}`, key: "stream", want: "stdout"},
		{raw: `{}`, key: "stream", want: "", empty: true},
		{raw: `no_braces`, key: "stream", want: "", empty: true},
		{raw: `{broken}`, key: "stream", want: "", empty: true},
		{raw: `{stream=""}`, key: "stream", want: ""},
	}
	for _, tc := range cases {
		got := parseStreamLabels(tc.raw)
		if v, ok := got[tc.key]; ok != !tc.empty || v != tc.want {
			t.Fatalf("raw=%q key=%q 期望 (%q,%v)，得到 (%q,%v)", tc.raw, tc.key, tc.want, !tc.empty, v, ok)
		}
	}
}
