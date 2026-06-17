package grpc

import (
	"testing"
)

func TestDetectConfigFormat(t *testing.T) {
	supported := map[string]string{
		"server.properties": "properties",
		"spigot.yml":        "yaml",
		"paper.yaml":        "yaml",
		"velocity.toml":     "toml",
		"whitelist.json":    "json",
		"eula.txt":          "txt",
		"server.conf":       "txt",
	}
	for path, want := range supported {
		got, ok := detectConfigFormat(path)
		if !ok {
			t.Fatalf("%s 应被识别", path)
		}
		if got != want {
			t.Fatalf("%s 期望格式 %s, 实际 %s", path, want, got)
		}
	}

	if _, ok := detectConfigFormat("plugin.jar"); ok {
		t.Fatalf("未支持的扩展名不应被识别")
	}
}

func TestParseConfigFieldsProperties_PreservesOrderAndComments(t *testing.T) {
	src := "# 主端口\nserver-port=25565\n\n# 难度\ndifficulty=1\n"
	fields := parseConfigFields("properties", src)
	if len(fields) != 2 {
		t.Fatalf("期望 2 个字段，实际 %d", len(fields))
	}
	if fields[0].Key != "server-port" || fields[0].Value != "25565" || fields[0].Line != 2 {
		t.Fatalf("字段 1 解析错误: %+v", fields[0])
	}
	if fields[1].Key != "difficulty" || fields[1].Value != "1" || fields[1].Line != 5 {
		t.Fatalf("字段 2 解析错误: %+v", fields[1])
	}
}

func TestValidateConfigText_BranchByFormat(t *testing.T) {
	if !validateConfigText("json", `{"a":1}`).Valid {
		t.Fatalf("合法 JSON 应通过")
	}
	if validateConfigText("json", "{a:1}").Valid {
		t.Fatalf("非法 JSON 应失败")
	}
	if !validateConfigText("yaml", "a: 1\n").Valid {
		t.Fatalf("合法 YAML 应通过")
	}
	if validateConfigText("yaml", "a: : :").Valid {
		t.Fatalf("非法 YAML 应失败")
	}
	if !validateConfigText("toml", "a = 1\n").Valid {
		t.Fatalf("合法 TOML 应通过")
	}
	if validateConfigText("toml", "a =").Valid {
		t.Fatalf("非法 TOML 应失败")
	}
	if !validateConfigText("properties", "a=1").Valid {
		t.Fatalf("properties 始终返回 Valid=true")
	}
}

func TestInferScalarType(t *testing.T) {
	cases := map[string]string{
		"true":  "bool",
		"False": "bool",
		"42":    "int",
		"-7":    "int",
		"hello": "string",
		"  ":    "string",
	}
	for in, want := range cases {
		if got := inferScalarType(in); got != want {
			t.Fatalf("inferScalarType(%q) 期望 %s 实际 %s", in, want, got)
		}
	}
}
