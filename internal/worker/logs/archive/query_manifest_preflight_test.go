package archive

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func preflightChecksumFixture(t *testing.T, prefix string, count int) string {
	t.Helper()
	objects := make(map[string]string, count)
	for i := 0; i < count; i++ {
		objects[prefix+strconv.Itoa(i)] = "test-hash"
	}
	data, err := json.Marshal(objects)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestManifestPreflightRejectsDuplicateNormalizedContainers(t *testing.T) {
	left, right := preflightChecksumFixture(t, "left-", 512), preflightChecksumFixture(t, "right-", 513)
	for _, name := range []string{"objects", "ObJeCtS", "objectſ", `object\u0073`, "checksums", "CHECKSUMS", "checkſumſ", `check\u0073ums`} {
		t.Run(name, func(t *testing.T) {
			payload := `{"checksums":{"objects":` + left + `,"` + name + `":` + right + `}}`
			if strings.EqualFold(name, "checksums") || name == `check\u0073ums` {
				payload = `{"checksums":{"objects":` + left + `},"` + name + `":{"objects":` + right + `}}`
			}
			var typed struct {
				Checksums struct {
					Objects map[string]string `json:"objects"`
				} `json:"checksums"`
			}
			if err := json.Unmarshal([]byte(payload), &typed); err != nil {
				t.Fatal(err)
			}
			if len(typed.Checksums.Objects) != 1025 {
				t.Fatalf("构造器未复现标准解码器合并：%d", len(typed.Checksums.Objects))
			}
			err := preflightManifestJSON(json.NewDecoder(strings.NewReader(payload)), "root", 0)
			if err == nil || !strings.Contains(err.Error(), "duplicate") {
				t.Fatalf("重复规范容器未在解码前拒绝：%v", err)
			}
			t.Logf("manifest_bytes=%d typed_fixture_entries=%d early_rejection=%v", len(payload), len(typed.Checksums.Objects), err)
		})
	}
}

func TestManifestPreflightPreservesCaseSensitiveObjectPaths(t *testing.T) {
	payload := `{"checksums":{"objects":{"raw/A.log":"upper-hash","raw/a.log":"lower-hash"}}}`
	if err := preflightManifestJSON(json.NewDecoder(strings.NewReader(payload)), "root", 0); err != nil {
		t.Fatal(err)
	}
	var typed struct {
		Checksums struct {
			Objects map[string]string `json:"objects"`
		} `json:"checksums"`
	}
	if err := json.Unmarshal([]byte(payload), &typed); err != nil {
		t.Fatal(err)
	}
	if len(typed.Checksums.Objects) != 2 || typed.Checksums.Objects["raw/A.log"] != "upper-hash" || typed.Checksums.Objects["raw/a.log"] != "lower-hash" {
		t.Fatal("真实对象路径不应被字段名折叠逻辑合并")
	}
}

func TestManifestPreflightChecksUniqueContainerCardinality(t *testing.T) {
	for _, count := range []int{1024, 1025} {
		payload := `{"checksums":{"objects":` + preflightChecksumFixture(t, "raw/", count) + `}}`
		err := preflightManifestJSON(json.NewDecoder(strings.NewReader(payload)), "root", 0)
		if count == 1024 && err != nil {
			t.Fatalf("合法上界被拒绝：%v", err)
		}
		if count == 1025 && (err == nil || !strings.Contains(err.Error(), "cardinality")) {
			t.Fatalf("超出数量闸未被拒绝：%v", err)
		}
	}
}

func TestManifestPreflightRejectsDuplicateArrayContainerFields(t *testing.T) {
	for _, payload := range []string{`{"raw_files":[],"RAW_FILES":[]}`, `{"coverage":{},"COVERAGE":{}}`, `{"coverage":{"source_generations":[],"SOURCE_GENERATIONS":[]}}`, `{"coverage":{"partial_reasons":[],"PARTIAL_REASONS":[]}}`} {
		if err := preflightManifestJSON(json.NewDecoder(strings.NewReader(payload)), "root", 0); err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("重复规范数组容器未被拒绝：%v", err)
		}
	}
}
