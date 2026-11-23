package process

import (
	"testing"
)

// TestJarsInCommand 抽取命令行里的 jar 基名。
func TestJarsInCommand(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want []string
	}{
		{"简单", "java -jar paper.jar nogui", []string{"paper.jar"}},
		{"带路径", "java -jar /srv/mc/game-1/paper.jar", []string{"paper.jar"}},
		{"带引号", `java -jar "paper-1.20.1-196.jar" nogui`, []string{"paper-1.20.1-196.jar"}},
		{"无 jar", "java -cp foo:bar Main", nil},
		{"空", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := jarsInCommand(c.cmd)
			if len(got) != len(c.want) {
				t.Fatalf("数量不符: got=%v want=%v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("第 %d 项: got=%q want=%q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestLooksLikeInstanceRuntime 粗判外部进程是否像本实例的运行时。
//
// 这是“防手滑”的阀门，不是安全边界——所以期望它宽松而不是严苛。
func TestLooksLikeInstanceRuntime(t *testing.T) {
	cases := []struct {
		name    string
		foreign string
		start   string
		want    bool
	}{
		{
			name:    "真实场景：tmux 里的 paper 服被系统 java 启动",
			foreign: "/opt/graalvm-community-jdk-17.0.9/bin/java -Xms2G -Xmx4G -jar paper.jar --nogui",
			start:   "java -Xms2048M -Xmx4096M -jar paper.jar nogui",
			want:    true,
		},
		{
			name:    "jar 名不同但都是 java",
			foreign: "java -jar paper-1.20.1-196.jar nogui",
			start:   "java -jar paper.jar nogui",
			want:    true, // 两边都提到 java —— 按设计放行
		},
		{
			name:    "完全不相干的进程（非 java）",
			foreign: "nginx: worker process",
			start:   "java -jar paper.jar nogui",
			want:    false,
		},
		{
			name:    "两边都空：无法比对，不武断拒绝",
			foreign: "",
			start:   "",
			want:    true,
		},
		{
			name:    "外部空：无法比对",
			foreign: "",
			start:   "java -jar paper.jar",
			want:    true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := looksLikeInstanceRuntime(c.foreign, c.start); got != c.want {
				t.Fatalf("got=%v want=%v", got, c.want)
			}
		})
	}
}

// TestAdoptInPlaceOnMissingInstance 不存在的实例必须报错而不是静默成功。
func TestAdoptInPlaceOnMissingInstance(t *testing.T) {
	m := NewManager(t.TempDir())
	if _, err := m.AdoptInPlaceRuntime("no-such-uuid"); err == nil {
		t.Fatal("不存在的实例应报错")
	}
}

// TestAdoptInPlaceIdempotentWhenRunning 已 RUNNING 且被收养的进程还活着 → 幂等返回（不去碰进程）。
//
// 注意幂等的**前提是被收养的那个 PID 确实还活着**：只看 state==RUNNING 是不够的，
// 运维重启服后旧 PID 已死、状态却仍被心跳维持成 RUNNING，那时必须重新认领（见
// TestAdoptInPlace_ReclaimsAfterServerRestart）。
func TestAdoptInPlaceIdempotentWhenRunning(t *testing.T) {
	m := NewManager(t.TempDir())
	const uuid = "inst-1"
	alivePID := 9601
	m.recoverPIDAlive = func(pid int) bool { return pid == alivePID }
	m.mu.Lock()
	m.instances[uuid] = &Instance{UUID: uuid, State: StateRunning, AdoptedPID: alivePID}
	m.mu.Unlock()

	pid, err := m.AdoptInPlaceRuntime(uuid)
	if err != nil {
		t.Fatalf("RUNNING 且进程存活应幂等成功，实际报错: %v", err)
	}
	if pid != 0 {
		t.Fatalf("幂等路径不应返回 PID，实际 %d", pid)
	}
}

// TestAdoptInPlaceNoRuntimeObserved 目录下没观测到活进程时返回哨兵错误。
func TestAdoptInPlaceNoRuntimeObserved(t *testing.T) {
	m := NewManager(t.TempDir())
	const uuid = "inst-2"
	m.mu.Lock()
	m.instances[uuid] = &Instance{UUID: uuid, State: StateStopped, WorkDir: t.TempDir(), StartCommand: "java -jar paper.jar"}
	m.mu.Unlock()

	_, err := m.AdoptInPlaceRuntime(uuid)
	if err == nil {
		t.Fatal("无活进程应返回错误")
	}
}

// TestReleaseInPlaceRuntime 释放只清登记，不改其他实例。
func TestReleaseInPlaceRuntime(t *testing.T) {
	m := NewManager(t.TempDir())
	const uuid = "inst-3"
	m.mu.Lock()
	m.instances[uuid] = &Instance{UUID: uuid, State: StateRunning, AdoptedPID: 12345}
	m.mu.Unlock()

	if err := m.ReleaseInPlaceRuntime(uuid); err != nil {
		t.Fatalf("释放应成功: %v", err)
	}
	m.mu.RLock()
	inst := m.instances[uuid]
	adopted, state := inst.AdoptedPID, inst.State
	m.mu.RUnlock()
	if adopted != 0 {
		t.Fatalf("AdoptedPID 应清零，实际 %d", adopted)
	}
	if state != StateStopped {
		t.Fatalf("状态应为 STOPPED，实际 %v", state)
	}
}
