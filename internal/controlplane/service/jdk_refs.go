package service

import (
	"sort"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 本文件是「JDK 引用关系」的唯一判定源。
//
// 【为什么单独成文件】引用关系此前只存在于 buildJDKMatrix（全局运行时页）内部，
// 而删除守卫又写在 JDKService.Delete 里、用另一段 SQL 表达同一件事——两处口径
// 一旦漂移，就会出现「页面说没人用、删除却 409」或反过来「页面说被占用、其实能删」。
// 现在绑定解析（ResolveJDKRefs）与删除判定（JDKEvaluateDelete）都收敛到这里，
// 全局矩阵、节点级清单、删除守卫三处共用同一份纯函数。

// JDKRefInstance 引用某 JDK 的实例（引用关系下钻 / 删除占用方提示）。
type JDKRefInstance struct {
	ID     uint                 `json:"id"`
	UUID   string               `json:"uuid"`
	Name   string               `json:"name"`
	Status model.InstanceStatus `json:"status"`
	// Binding 绑定方式：direct=按具体 JDK（jdk_id）；major=按 Java 大版本（java_major_version）解析到本 JDK。
	Binding string `json:"binding"`
}

// 绑定方式常量。
const (
	JDKBindingDirect = "direct"
	JDKBindingMajor  = "major"
)

// majorKey 是 (节点, 大版本) 解析键。
type majorKey struct {
	nodeID uint
	major  int
}

// resolveMajorBinding 计算每个 (节点, 大版本) 解析命中的 jdk_id（取 id 最大，与 ResolveForInstance 一致）。
func resolveMajorBinding(jdks []model.NodeJDK) map[majorKey]uint {
	out := make(map[majorKey]uint)
	for _, j := range jdks {
		k := majorKey{nodeID: j.NodeID, major: j.MajorVersion}
		if cur, ok := out[k]; !ok || j.ID > cur {
			out[k] = j.ID
		}
	}
	return out
}

// ResolveJDKRefs 由 JDK 清单与实例清单推导 jdkID → 引用实例，并返回引用边总数。
//
// 引用解析规则（与启动时 ResolveForInstance 的绑定语义一致）：
//   - 直接绑定：instance.jdk_id != 0 → binding=direct；
//   - 大版本绑定：instance.jdk_id==0 且 instance.java_major_version==jdk.major_version 且同节点，
//     解析到「同节点同大版本中 id 最大」的那一个 JDK，binding=major。
//
// jdks 只需覆盖将被展示的那批 JDK；但**大版本解析必须在完整的节点内 JDK 集合上做**，
// 否则「同节点同大版本取 id 最大」会退化成「取传入子集里最大」，把引用指到别的 JDK 上。
// 调用方若要展示单节点，传该节点全部 JDK 即可满足；若要展示子集，须先传全集解析再筛选。
func ResolveJDKRefs(jdks []model.NodeJDK, instances []model.Instance) (map[uint][]JDKRefInstance, int) {
	resolvedMajor := resolveMajorBinding(jdks)
	refs := make(map[uint][]JDKRefInstance, len(jdks))
	instanceRefs := 0
	for _, inst := range instances {
		var targetJDK uint
		binding := ""
		switch {
		case inst.JDKID != 0:
			targetJDK = inst.JDKID
			binding = JDKBindingDirect
		case inst.JavaMajorVersion != 0:
			if jid, ok := resolvedMajor[majorKey{nodeID: inst.NodeID, major: inst.JavaMajorVersion}]; ok {
				targetJDK = jid
				binding = JDKBindingMajor
			}
		}
		if targetJDK == 0 {
			continue
		}
		refs[targetJDK] = append(refs[targetJDK], JDKRefInstance{
			ID:      inst.ID,
			UUID:    inst.UUID,
			Name:    inst.Name,
			Status:  inst.Status,
			Binding: binding,
		})
		instanceRefs++
	}
	// 稳定排序：先按实例名再按 id，便于 UI 与测试确定（与矩阵原实现一致）。
	for id := range refs {
		list := refs[id]
		sort.Slice(list, func(a, b int) bool {
			if list[a].Name != list[b].Name {
				return list[a].Name < list[b].Name
			}
			return list[a].ID < list[b].ID
		})
		refs[id] = list
	}
	return refs, instanceRefs
}

// 删除守卫的拒绝原因。
const (
	// JDKDeleteBlockedDirect 有实例按 jdk_id 直接绑定了它。
	JDKDeleteBlockedDirect = "direct"
	// JDKDeleteBlockedMajor 托管 JDK 是该节点该大版本唯一的托管项，有实例按大版本依赖它。
	JDKDeleteBlockedMajor = "major"
)

// JDKEvaluateDelete 判断该 JDK 当前能否删除，返回是否被拦与原因（空串=可删）。
//
// 【必须与 JDKService.Delete 的守卫同源】这里复刻的是 Delete 的两段判定：
//  1. 直接绑定（jdk_id）一律拦；
//  2. 大版本绑定只在「托管 + 该节点该大版本没有第二个托管 JDK」时拦——
//     同大版本还有别的托管项时 resolveJDKPath 会挑到另一个，报占用会让
//     「多装一个同版本再删旧的」这类正常操作被误拦。
//
// jdks 必须是**该节点全部 JDK**（判定 2 要数同大版本的其它托管项）。
func JDKEvaluateDelete(jdk model.NodeJDK, jdks []model.NodeJDK, refs []JDKRefInstance) (blocked bool, reason string) {
	hasDirect, hasMajor := false, false
	for _, r := range refs {
		switch r.Binding {
		case JDKBindingDirect:
			hasDirect = true
		case JDKBindingMajor:
			hasMajor = true
		}
	}
	if hasDirect {
		return true, JDKDeleteBlockedDirect
	}
	if !hasMajor {
		return false, ""
	}
	// 与 Delete 一致：这条路径只在托管 JDK 上有意义（外部登记目录不归平台删）。
	if !jdk.Managed || jdk.MajorVersion <= 0 {
		return false, ""
	}
	for _, other := range jdks {
		if other.ID != jdk.ID && other.NodeID == jdk.NodeID && other.MajorVersion == jdk.MajorVersion && other.Managed {
			return false, ""
		}
	}
	return true, JDKDeleteBlockedMajor
}

// BlockedRefs 从引用清单里筛出「真正导致删除被拦」的那些实例。
//
// 用于删除弹窗的占用方名单：被 major 拦时，直接绑定的实例也要列出来（用户要知道完整影响面）；
// 未被拦时（例如同大版本还有别的托管 JDK）返回空，避免出现「说没人拦却列了一堆实例」的矛盾提示。
func BlockedRefs(blocked bool, refs []JDKRefInstance) []JDKRefInstance {
	if !blocked {
		return nil
	}
	return refs
}
