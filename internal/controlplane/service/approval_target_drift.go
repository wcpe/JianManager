package service

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 目标漂移校验（FR-491）。
//
// ## 为什么需要它
//
// 审批的意义是「有权限的人批准了一件**具体的事**」。若从批准到执行之间目标变了，
// 批准的那件事与实际执行的事就不再是同一件——此时执行等于把批准人的授权
// 挪用到他没看过的东西上。这是审批机制最根本的失效方式。
//
// ## 校验什么，不校验什么
//
// 只校验**执行前能被客观比对**的事实：
//   - 实例：存在性、节点归属、`updated_at`（改配置 / 换二进制 / 改名都会推进它）；
//   - 节点：存在性、`updated_at`。
//
// **刻意不校验**「实例是否在运行」这类会随正常运维波动的事实：
// 批「停止实例」时它必须是在跑的，但批准后运维手工停了它，再执行 stop 也无害——
// 把这类波动判为漂移会让审批在真实运维节奏里频繁失败，反而促使人绕过审批。
//
// ## 缺失 evidence 时的语义
//
// 旧票据（本功能上线前提交的）没有快照。此时**不阻断**：宁可放过一次无法校验的执行，
// 也不要让存量票据全部执行失败——那会让上线瞬间堆积一批「批准了但永远失败」的票。
// 这是有意的取舍，已在 spec 记录（见文件末注释）。
//
// ## 为什么 Version 用 updated_at 纳秒而非递增版本号
//
// 现有实例/节点表没有乐观锁版本列。`updated_at` 已被所有写路径维护，
// 用它作「内容是否变过」的代理是当下零迁移成本的正确选择。
// 复查条件：若将来引入显式版本列（如 `revision`），应改用版本列——
// 因为 `updated_at` 在同一纳秒内的两次写入无法区分（现实中不可能，但属理论缺口）。
//
// ## 为什么 version 在 evidence 里存字符串而不是数字
//
// evidence 落库是 text 列：`json.Marshal(map[string]any)` 写入，
// 读出时 `json.Unmarshal` 回 `map[string]any`——**所有数字都变成 float64**。
// 而纳秒时间戳（2026 年约 1.8e18）远超 float64 能精确表示的整数上限 2^53(≈9e15)，
// 往返一次必然被舍入到 256 纳秒的倍数：于是「目标根本没变」也会被判成漂移，
// 每张票据的执行都会永久失败。
//
// 因此 `version` 以十进制字符串写入 JSON，取值时再解析回 int64，精度完整保留。
// 这里刻意不退一步改用毫秒时间戳（1.8e12 可被 float64 精确表示）：
// 毫秒级会把「同一毫秒内的两次真实写更新」误判为「没变」，
// 等于放行一次针对已变更目标的执行——那是安全漏洞，比精度冗余更糟。
// decode 侧仍容忍数字形式（便于手工构造与调试），但数字形式本就是有损的。

// TargetSnapshot 是申请时冻结的目标状态（序列化后存入 ApprovalTicket.Evidence）。
//
// 字段名即 JSON 键，**落库后不得重命名**——重命名会让存量票据的校验永久失败。
type TargetSnapshot struct {
	// Kind instance / node。
	Kind string `json:"kind"`
	// ID 目标主键。
	ID uint `json:"id"`
	// Name 申请时的名字，仅用于失败时给出可读原因（不参与漂移判定——改名由 updatedAt 覆盖）。
	Name string `json:"name,omitempty"`
	// NodeID 实例所属节点（实例才有）。参与判定：实例被迁到别的节点后，
	// 原批准隐含的「在哪台机器上执行」已不成立。
	NodeID uint `json:"nodeId,omitempty"`
	// Status 申请时的状态，**不参与判定**（见文件头取舍）。
	Status string `json:"status,omitempty"`
	// Version 申请时的 updated_at（Unix 纳秒）。漂移判定的主依据。
	// 在 evidence JSON 里以字符串承载，原因见文件头「为什么 version 存字符串」。
	Version int64 `json:"version,omitempty"`
	// CapturedAt 快照采集时刻（RFC3339），供审计与排查。
	CapturedAt string `json:"capturedAt,omitempty"`
}

// CaptureTargetSnapshot 采集申请时的目标快照。
//
// 返回 nil 表示「无法采集」（目标不存在或类型不支持）——此时票据照常落库但 Evidence 为空，
// 执行侧按「无快照不阻断」处理。不在此处报错拒绝提交：申请阶段的首要职责是**记录意图**，
// 目标不存在这类问题由后续的授权与执行阶段拒绝更合适（且信息更准确）。
func CaptureTargetSnapshot(instances *InstanceService, nodes *NodeService, targetType, targetID string) map[string]any {
	id, ok := parseUintOrZero(targetID)
	if !ok || id == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	switch targetType {
	case "instance":
		if instances == nil {
			return nil
		}
		inst, err := instances.GetByID(id)
		if err != nil || inst == nil {
			return nil
		}
		return snapshotToMap(TargetSnapshot{
			Kind: "instance", ID: inst.ID, Name: inst.Name, NodeID: inst.NodeID,
			Status: string(inst.Status), Version: inst.UpdatedAt.UnixNano(), CapturedAt: now,
		})
	case "node":
		if nodes == nil {
			return nil
		}
		node, err := nodes.GetByID(id)
		if err != nil || node == nil {
			return nil
		}
		return snapshotToMap(TargetSnapshot{
			Kind: "node", ID: node.ID, Name: node.Name,
			Status: fmt.Sprintf("%d", node.Status), Version: node.UpdatedAt.UnixNano(), CapturedAt: now,
		})
	default:
		return nil
	}
}

func snapshotToMap(s TargetSnapshot) map[string]any {
	return map[string]any{
		"kind": s.Kind, "id": s.ID, "name": s.Name, "nodeId": s.NodeID,
		"status": s.Status,
		// version 存十进制字符串：纳秒值超过 float64 精确整数上限，存数字会丢精度，
		// 见文件头「为什么 version 在 evidence 里存字符串而不是数字」。
		"version":    strconv.FormatInt(s.Version, 10),
		"capturedAt": s.CapturedAt,
	}
}

// VerifyTargetDrift 构建执行前漂移校验器，供 ApprovalExecutor.SetTargetVerifier 装配。
//
// 返回 nil 表示两个服务都不可用——此时装配层不注入校验器，执行器退化为
// 「只校验 payload 哈希」（与 SetTargetVerifier 未调用时的行为一致）。
func VerifyTargetDrift(instances *InstanceService, nodes *NodeService) func(t *model.ApprovalTicket, evidence map[string]any) error {
	if instances == nil && nodes == nil {
		return nil
	}
	return func(t *model.ApprovalTicket, evidence map[string]any) error {
		if t == nil {
			return fmt.Errorf("%w: 票据为空", ErrApprovalTargetDrift)
		}
		// 无快照：旧票据或采集失败。不阻断，见文件头「缺失 evidence 时的语义」。
		snap, ok := decodeTargetSnapshot(evidence)
		if !ok {
			return nil
		}
		switch snap.Kind {
		case "instance":
			if instances == nil {
				return nil
			}
			inst, err := instances.GetByID(snap.ID)
			if err != nil || inst == nil {
				return fmt.Errorf("%w: 实例 #%d（%s）已不存在", ErrApprovalTargetDrift, snap.ID, snap.Name)
			}
			if inst.NodeID != snap.NodeID {
				return fmt.Errorf("%w: 实例 #%d 已从节点 #%d 迁至 #%d",
					ErrApprovalTargetDrift, snap.ID, snap.NodeID, inst.NodeID)
			}
			if inst.UpdatedAt.UnixNano() != snap.Version {
				return fmt.Errorf("%w: 实例 #%d（%s）在申请后已被修改（申请于 %s）",
					ErrApprovalTargetDrift, snap.ID, snap.Name, snap.CapturedAt)
			}
		case "node":
			if nodes == nil {
				return nil
			}
			node, err := nodes.GetByID(snap.ID)
			if err != nil || node == nil {
				return fmt.Errorf("%w: 节点 #%d（%s）已不存在", ErrApprovalTargetDrift, snap.ID, snap.Name)
			}
			if node.UpdatedAt.UnixNano() != snap.Version {
				return fmt.Errorf("%w: 节点 #%d（%s）在申请后已被修改（申请于 %s）",
					ErrApprovalTargetDrift, snap.ID, snap.Name, snap.CapturedAt)
			}
		default:
			// 未知类型：不阻断（可能是新资源类型，本校验器尚未覆盖）。
			return nil
		}
		return nil
	}
}

// decodeTargetSnapshot 从 evidence map 还原快照。
//
// JSON 数字经 map[string]any 往返后是 float64，因此逐字段做类型收敛，
// 不能直接断言成 int64/uint（那样在真实路径上必然失败——单测里若直接用
// 构造好的 map 就会漏掉这个差异，故此处显式处理两种来源）。
func decodeTargetSnapshot(evidence map[string]any) (TargetSnapshot, bool) {
	if len(evidence) == 0 {
		return TargetSnapshot{}, false
	}
	kind, _ := evidence["kind"].(string)
	if kind == "" {
		return TargetSnapshot{}, false
	}
	snap := TargetSnapshot{
		Kind:       kind,
		ID:         toUintFromAny(evidence["id"]),
		NodeID:     toUintFromAny(evidence["nodeId"]),
		Version:    toVersionFromAny(evidence["version"]),
		Name:       toStringFromAny(evidence["name"]),
		Status:     toStringFromAny(evidence["status"]),
		CapturedAt: toStringFromAny(evidence["capturedAt"]),
	}
	if snap.ID == 0 {
		return TargetSnapshot{}, false
	}
	if snap.Version == 0 {
		// 有 kind 有 id 但没版本：无法判定漂移。按「无快照」处理，不阻断。
		return TargetSnapshot{}, false
	}
	return snap, true
}

func toUintFromAny(v any) uint {
	switch n := v.(type) {
	case float64:
		if n > 0 {
			return uint(n)
		}
	case int:
		if n > 0 {
			return uint(n)
		}
	case int64:
		if n > 0 {
			return uint(n)
		}
	case uint:
		return n
	case json.Number:
		if i, err := n.Int64(); err == nil && i > 0 {
			return uint(i)
		}
	}
	return 0
}

// toVersionFromAny 把 evidence 里的 version 收敛成 int64 纳秒。
//
// 正常路径是十进制字符串（见文件头「为什么 version 存字符串」），
// 同时容忍数字形态：float64 / json.Number / int64 / int。
// 数字形态在纳秒量级上本就是有损的（超出 float64 精确整数上限），
// 保留它只是为了让手工构造的 evidence 与调试场景不至于直接失效，
// 不能作为落库格式依赖。
func toVersionFromAny(v any) int64 {
	switch n := v.(type) {
	case string:
		if n == "" {
			return 0
		}
		if i, err := strconv.ParseInt(n, 10, 64); err == nil {
			return i
		}
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	return 0
}

func toStringFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// parseUintOrZero 把票据里的字符串 ID 解析成 uint。
func parseUintOrZero(s string) (uint, bool) {
	if s == "" {
		return 0, false
	}
	var out uint
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		out = out*10 + uint(r-'0')
	}
	return out, true
}
