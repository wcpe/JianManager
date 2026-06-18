package service

import "testing"

func TestParseLaunchSpec(t *testing.T) {
	if spec, err := parseLaunchSpec("  "); err != nil || spec != nil {
		t.Fatalf("空串应返回 (nil,nil)，得到 spec=%v err=%v", spec, err)
	}
	if _, err := parseLaunchSpec("{not json"); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
	spec, err := parseLaunchSpec(`{"memoryMb":2048,"jvmArgs":["-XX:+UseG1GC"],"coreJar":"paper.jar"}`)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if spec.MemoryMb != 2048 || spec.CoreJar != "paper.jar" || len(spec.JvmArgs) != 1 {
		t.Fatalf("解析结果不符: %+v", spec)
	}
}

func TestDeriveStartCommand(t *testing.T) {
	tests := []struct {
		name    string
		spec    *LaunchSpec
		want    string
		wantErr bool
	}{
		{"nil", nil, "", true},
		{"缺少 coreJar", &LaunchSpec{MemoryMb: 1024}, "", true},
		{
			"完整",
			&LaunchSpec{MemoryMb: 4096, JvmArgs: []string{"-XX:+UseG1GC"}, CoreJar: "paper.jar", ExtraArgs: []string{"--world", "w"}},
			"java -Xms4096M -Xmx4096M -XX:+UseG1GC -jar paper.jar nogui --world w",
			false,
		},
		{
			"无内存无额外参数",
			&LaunchSpec{CoreJar: "server.jar"},
			"java -jar server.jar nogui",
			false,
		},
		{
			"含空格的 coreJar 被引号包裹",
			&LaunchSpec{CoreJar: "paper 1.21.jar"},
			`java -jar "paper 1.21.jar" nogui`,
			false,
		},
		{
			"空白 jvmArg 被剔除",
			&LaunchSpec{MemoryMb: 512, JvmArgs: []string{"", "  ", "-Dfoo=bar"}, CoreJar: "s.jar"},
			"java -Xms512M -Xmx512M -Dfoo=bar -jar s.jar nogui",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := deriveStartCommand(tt.spec)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("期望报错，得到 %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外报错: %v", err)
			}
			if got != tt.want {
				t.Fatalf("命令不符:\n want %q\n got  %q", tt.want, got)
			}
		})
	}
}
