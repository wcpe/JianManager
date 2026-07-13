package memguard

import (
	"strings"
	"testing"
)

// TestParseXmxMB 各单位与缺省解析（FR-317）。
func TestParseXmxMB(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want int64
	}{
		{"G 单位", "java -Xms2048M -Xmx2G -jar server.jar nogui", 2048},
		{"M 单位", "java -Xmx2048M -jar server.jar", 2048},
		{"小写 g", "java -xmx4g -jar s.jar", 4096},
		{"K 单位向上取整", "java -Xmx2097152k -jar s.jar", 2048},
		{"无 Xmx", "java -jar server.jar nogui", 0},
		{"多个取第一个", "java -Xmx1G -Xmx8G -jar s.jar", 1024},
		{"空命令", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseXmxMB(tt.cmd); got != tt.want {
				t.Fatalf("ParseXmxMB(%q) = %d, want %d", tt.cmd, got, tt.want)
			}
		})
	}
}

// TestEstimateStartMB docker 上限优先 > Xmx 估算 > 保守默认（FR-317）。
func TestEstimateStartMB(t *testing.T) {
	if got := EstimateStartMB("java -Xmx2G -jar s.jar", 4096); got != 4096 {
		t.Fatalf("docker MemLimit 应优先: %d", got)
	}
	// 2048*1.15+256 = 2611
	if got := EstimateStartMB("java -Xmx2G -jar s.jar", 0); got != 2611 {
		t.Fatalf("Xmx 估算不对: %d", got)
	}
	if got := EstimateStartMB("./start.sh", 0); got != defaultEstimateMB {
		t.Fatalf("无声明应用保守默认: %d", got)
	}
}

// TestDefaultReserveMB max(512, 10%)。
func TestDefaultReserveMB(t *testing.T) {
	if got := DefaultReserveMB(4096); got != 512 {
		t.Fatalf("4G 主机保留应 512: %d", got)
	}
	if got := DefaultReserveMB(16384); got != 1638 {
		t.Fatalf("16G 主机保留应 10%%=1638: %d", got)
	}
}

// TestCheck 水位判定边界与错误文案可操作性（FR-317）。
func TestCheck(t *testing.T) {
	// 可用 3000，需求 2611，保留 512 → 3000-2611=389 < 512 拒绝
	err := Check(3000, 2611, 512)
	if err == nil {
		t.Fatal("应拒绝")
	}
	for _, want := range []string{"3000", "2611", "512", "拒绝启动"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误文案缺 %q: %v", want, err)
		}
	}
	// 恰好等于保留线 → 放行
	if err := Check(3123, 2611, 512); err != nil {
		t.Fatalf("等于水位线应放行: %v", err)
	}
}
