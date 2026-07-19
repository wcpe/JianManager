package version

import "testing"

func TestRequested(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "无参数", want: false},
		{name: "版本参数", args: []string{"--version"}, want: true},
		{name: "版本参数后带额外内容", args: []string{"--version", "ignored"}, want: true},
		{name: "配置路径", args: []string{"config.yml"}, want: false},
		{name: "相似子命令", args: []string{"version"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Requested(tt.args); got != tt.want {
				t.Fatalf("Requested(%q) = %v，期望 %v", tt.args, got, tt.want)
			}
		})
	}
}
