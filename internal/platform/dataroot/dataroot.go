// Package dataroot 提供平台「项目自包含便携运行时」的单一数据根与 FHS 式目录布局。
//
// 数据根默认为进程工作目录下的 ./data，可经环境变量 JIANMANAGER_DATA_DIR 覆盖。
// Control Plane 与 Worker Node 同源约定此根：二者均通过本包解析路径，使 JDK、
// 服务器工作目录、配置、制品库等运行态数据全部收口到根内，整体拷走仍自洽。
//
// 参见 ADR-010: 项目自包含便携运行时目录布局（细化 ADR-007/008）。
package dataroot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvVar 是覆盖数据根的环境变量名。
const EnvVar = "JIANMANAGER_DATA_DIR"

// DefaultDir 是未设置 EnvVar 时的默认数据根（相对进程工作目录）。
const DefaultDir = "data"

// Root 表示一个已解析的数据根，所有路径方法都相对它求值。
// Root 不可变，可被多个 goroutine 安全共享（只读）。
type Root struct {
	// base 是数据根的绝对路径。
	base string
}

// layoutDirs 是首次启动需确保存在的 FHS 式子目录（相对数据根）。
// 参见 ADR-010 决策 2。
var layoutDirs = []string{
	"bin",
	"etc",
	filepath.Join("opt", "jdks"),
	filepath.Join("var", "servers"),
	filepath.Join("var", "log"),
	filepath.Join("var", "artifacts"),
	filepath.Join("var", "index"),
	"cache",
}

// Resolve 解析数据根路径但不创建任何目录。
// 优先级：显式入参 override（非空）> 环境变量 JIANMANAGER_DATA_DIR > 默认 ./data。
// 返回的 Root 持有绝对路径。
func Resolve(override string) (*Root, error) {
	dir := override
	if dir == "" {
		dir = os.Getenv(EnvVar)
	}
	if dir == "" {
		dir = DefaultDir
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("解析数据根绝对路径失败: %w", err)
	}
	return &Root{base: abs}, nil
}

// Init 解析数据根并按 FHS 布局初始化（缺失则创建），返回就绪的 Root。
// 幂等：已存在的目录不受影响。
func Init(override string) (*Root, error) {
	r, err := Resolve(override)
	if err != nil {
		return nil, err
	}
	if err := r.EnsureLayout(); err != nil {
		return nil, err
	}
	return r, nil
}

// EnsureLayout 创建数据根及其 FHS 式子目录（若不存在）。幂等。
func (r *Root) EnsureLayout() error {
	for _, d := range layoutDirs {
		full := filepath.Join(r.base, d)
		if err := os.MkdirAll(full, 0o755); err != nil {
			return fmt.Errorf("创建数据根目录 %s 失败: %w", full, err)
		}
	}
	return nil
}

// Base 返回数据根的绝对路径。
func (r *Root) Base() string { return r.base }

// JDKsDir 返回托管 JDK 根目录 <root>/opt/jdks。
func (r *Root) JDKsDir() string { return filepath.Join(r.base, "opt", "jdks") }

// ServersDir 返回服务器工作目录根 <root>/var/servers。
func (r *Root) ServersDir() string { return filepath.Join(r.base, "var", "servers") }

// LogDir 返回日志目录 <root>/var/log。
func (r *Root) LogDir() string { return filepath.Join(r.base, "var", "log") }

// ArtifactsDir 返回制品库根目录 <root>/var/artifacts（见 ADR-011）。
func (r *Root) ArtifactsDir() string { return filepath.Join(r.base, "var", "artifacts") }

// IndexDir 返回全文搜索索引根目录 <root>/var/index（每实例一子目录，见 ADR-017）。
// 这是 Worker 本地派生资产（倒排索引），绝不进 CP 数据库；可随时删除重建。
func (r *Root) IndexDir() string { return filepath.Join(r.base, "var", "index") }

// CacheDir 返回临时缓存目录 <root>/cache（下载中转/解压）。
func (r *Root) CacheDir() string { return filepath.Join(r.base, "cache") }

// EtcDir 返回配置目录 <root>/etc。
func (r *Root) EtcDir() string { return filepath.Join(r.base, "etc") }

// BinDir 返回辅助可执行目录 <root>/bin。
func (r *Root) BinDir() string { return filepath.Join(r.base, "bin") }

// Abs 把一个相对数据根的路径解析为绝对路径。
// 已是绝对路径的入参原样返回（兼容历史用户手填的绝对工作目录）。
func (r *Root) Abs(rel string) string {
	if rel == "" {
		return r.base
	}
	if filepath.IsAbs(rel) {
		return filepath.Clean(rel)
	}
	return filepath.Join(r.base, filepath.FromSlash(rel))
}

// Rel 把一个绝对路径转换为相对数据根、以「/」分隔的可移植相对路径。
// 若路径在数据根之外则原样返回（无法相对化时不强行改写）。
func (r *Root) Rel(abs string) string {
	if abs == "" {
		return ""
	}
	rel, err := filepath.Rel(r.base, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return abs
	}
	return filepath.ToSlash(rel)
}
