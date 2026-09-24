package lifecycle

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// Runtime 是 lifecycle 允许调用的 VL 薄表面。
// 不实现完整进程控制；supervisor 启停仍在 vlsup。
type Runtime interface {
	// Status 返回 namespace 账面状态（health ≠ 分区恢复 ≠ 查询可用）。
	Status(ns vlsup.Namespace) (vlsup.InstanceStatus, error)
	// AttachStorage 将物理目录登记到对应 VL namespace（staging attach / residual 感知）。
	AttachStorage(ns vlsup.Namespace, dirID, path string) error
	// DetachStorage 解除目录挂载（detach ≠ 迁移完成）。
	DetachStorage(ns vlsup.Namespace, dirID string) error
}

// VLSupRuntime 以 vlsup.Supervisor 为后端的 Runtime 适配。
// Attach/Detach 在 foundation 层做账面登记；真实 VL storage 接线属 FR-475 Runbook C。
type VLSupRuntime struct {
	Sup *vlsup.Supervisor
	// attached dirID → storage path（测试/账面）。
	attached map[string]string
}

// NewVLSupRuntime 创建适配器。sup 为 nil 时 Status 返回错误。
func NewVLSupRuntime(sup *vlsup.Supervisor) *VLSupRuntime {
	return &VLSupRuntime{Sup: sup, attached: map[string]string{}}
}

// Status 实现 Runtime。
func (r *VLSupRuntime) Status(ns vlsup.Namespace) (vlsup.InstanceStatus, error) {
	if r == nil || r.Sup == nil {
		return vlsup.InstanceStatus{}, fmt.Errorf("lifecycle: vlsup supervisor not configured")
	}
	return r.Sup.Status(ns)
}

// AttachStorage 实现 Runtime（账面登记；不启动 VL 进程）。
func (r *VLSupRuntime) AttachStorage(ns vlsup.Namespace, dirID, path string) error {
	if r == nil {
		return fmt.Errorf("lifecycle: runtime nil")
	}
	if dirID == "" {
		return fmt.Errorf("lifecycle: attach dirID required")
	}
	if r.attached == nil {
		r.attached = map[string]string{}
	}
	r.attached[dirID] = path
	_ = ns
	return nil
}

// DetachStorage 实现 Runtime。
func (r *VLSupRuntime) DetachStorage(ns vlsup.Namespace, dirID string) error {
	if r == nil {
		return fmt.Errorf("lifecycle: runtime nil")
	}
	delete(r.attached, dirID)
	_ = ns
	return nil
}

// AttachedDirs 返回当前账面已挂载目录 ID。
func (r *VLSupRuntime) AttachedDirs() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.attached))
	for id := range r.attached {
		out = append(out, id)
	}
	return out
}

// NamespaceForOwner 将 catalog.Owner 映射到 vlsup namespace。
func NamespaceForOwner(owner catalog.Owner) vlsup.Namespace {
	switch owner {
	case catalog.OwnerHot:
		return vlsup.NamespaceHot
	case catalog.OwnerCold:
		return vlsup.NamespaceCold
	case catalog.OwnerArchive:
		return vlsup.NamespaceRehydrate
	default:
		return vlsup.NamespaceCold
	}
}
