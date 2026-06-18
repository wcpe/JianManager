package service

import (
	"regexp"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

func TestSlugify(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"lowercase", "MyServer", "myserver"},
		{"spaces to dash", "my server", "my-server"},
		{"collapse separators", "my  --  server", "my-server"},
		{"trim edges", "__hub__", "hub"},
		{"unicode stripped", "生存服 survival", "survival"},
		{"all invalid", "***", ""},
		{"keep digits", "lobby01", "lobby01"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := slugify(c.in); got != c.want {
				t.Fatalf("slugify(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSlugifyMaxLen(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := slugify(long)
	if len(got) > 48 {
		t.Fatalf("slug too long: %d", len(got))
	}
}

var workdirRe = regexp.MustCompile(`^var/servers/[a-z0-9-]+-[0-9a-f]{8}$`)

func TestAllocWorkDirRelShape(t *testing.T) {
	cases := []string{"My Server", "生存服", "***", "lobby"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			got := allocWorkDirRel(name)
			if !workdirRe.MatchString(got) {
				t.Fatalf("allocWorkDirRel(%q) = %q, does not match %s", name, got, workdirRe)
			}
			// 相对路径（便携性）：不得是绝对路径、不得含盘符或反斜杠。
			if strings.HasPrefix(got, "/") || strings.Contains(got, ":") || strings.Contains(got, "\\") {
				t.Fatalf("allocWorkDirRel must be a portable relative path, got %q", got)
			}
		})
	}
}

func TestAllocWorkDirRelUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		got := allocWorkDirRel("same-name")
		if seen[got] {
			t.Fatalf("duplicate workdir allocation: %q", got)
		}
		seen[got] = true
	}
}

func TestAllocWorkDirRelFallback(t *testing.T) {
	got := allocWorkDirRel("")
	if !strings.HasPrefix(got, "var/servers/instance-") {
		t.Fatalf("empty name should fall back to instance-*, got %q", got)
	}
}

func newInstanceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// 每个测试一个独立的命名内存库（mode=memory + 唯一名 + cache=shared），
	// 既隔离测试间状态，又避免 Windows 下 SQLite 文件被占用导致 TempDir 清理失败。
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.GroupInstance{}, &model.GroupQuota{}, &model.Bot{}, &model.Backup{}))
	return db
}

// MC 实例：忽略用户手填 WorkDir，系统在 var/servers 下按相对路径分配（ADR-007/ADR-010）。
func TestCreate_MinecraftAllocatesRelativeWorkDir(t *testing.T) {
	db := newInstanceTestDB(t)
	svc := NewInstanceService(db, nil, nil)

	inst, err := svc.Create(CreateInstanceRequest{
		NodeID:       1,
		Name:         "My Survival Server",
		Type:         model.InstanceTypeMinecraftJava,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "java -jar server.jar",
		WorkDir:      "/etc/passwd", // 用户手填应被忽略
	})
	require.NoError(t, err)
	if !workdirRe.MatchString(inst.WorkDir) {
		t.Fatalf("MC workdir = %q, want allocated var/servers/<slug>-<id>", inst.WorkDir)
	}
	if !strings.HasPrefix(inst.WorkDir, "var/servers/my-survival-server-") {
		t.Fatalf("slug not derived from name: %q", inst.WorkDir)
	}
	if strings.Contains(inst.WorkDir, "passwd") {
		t.Fatalf("user-provided path must be ignored, got %q", inst.WorkDir)
	}
}

// generic 实例：保留用户传入的 WorkDir（系统分配仅作用于 MC 实例）。
func TestCreate_GenericKeepsUserWorkDir(t *testing.T) {
	db := newInstanceTestDB(t)
	svc := NewInstanceService(db, nil, nil)

	inst, err := svc.Create(CreateInstanceRequest{
		NodeID:       1,
		Name:         "custom",
		Type:         model.InstanceTypeGeneric,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "./run.sh",
		WorkDir:      "/opt/custom",
	})
	require.NoError(t, err)
	if inst.WorkDir != "/opt/custom" {
		t.Fatalf("generic workdir = %q, want user-provided /opt/custom", inst.WorkDir)
	}
}
