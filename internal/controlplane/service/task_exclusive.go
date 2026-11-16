package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 本文件实现「重型任务的单在途互斥」（任务队列语义）。
//
// 【为什么需要它】JDK 一键安装、运行时（Node.js）安装、全局包安装这几个动作都是
// **重型且互相踩踏**的：它们写同一个托管目录（<data>/opt/jdks、<data>/opt/runtimes），
// 并发跑会争抢磁盘与网络，且失败时难以判断是谁踩了谁。此前只能靠前端防重（而且前端
// 只能挡同一个浏览器 tab 的连点），换个 tab、换个人、直接调 API 都能绕过。
//
// 所以真正的闸门必须在 CP：**同一节点上，同一互斥组只能有一个非终态任务**。
//
// 互斥组（ExclusiveGroup）的划分原则是「写同一批资源」：
//   - runtime_install：所有运行时安装（JDK 与 Node.js 共用一个组）
//     —— 它们都写节点级托管运行时目录，且 Node.js 安装在极端情况下会动 PATH 邻近文件；
//   - pkg_install：全局包安装（写 <data>/opt/runtimes/global/**），与运行时安装分开，
//     因为它不动运行时二进制本身。若将来发现两者确实互相影响，把它们并进一组即可。
//
// 【为什么不直接对 kind 做互斥】kind 是「这是什么任务」，不是「这是抢哪块资源」。
// 两个不同 kind 完全可能抢同一块盘（jdk_install 与 runtime_install 就是）。

// ErrTaskExclusiveBusy 表示同节点同互斥组已有非终态任务。
var ErrTaskExclusiveBusy = errors.New("该节点已有同类任务在跑")

// TaskExclusiveBusyError 携带占用者信息，便于前端直接展示「是谁在占着」并提供跳转。
type TaskExclusiveBusyError struct {
	// Existing 占用中的任务（非终态）。
	Existing *model.Task
	// Reused 表示这次冲突其实是**幂等命中**：占用者就是同一个任务（同参数）。
	// 此时对用户而言不是"被拦住了"，而是"你要的这件事已经在做了"，前端文案与动作都不同，
	// 所以必须在错误里区分出来，不能让它和"不同参数被互斥"混成一个提示。
	Reused bool
}

func (e *TaskExclusiveBusyError) Error() string {
	if e == nil || e.Existing == nil {
		return ErrTaskExclusiveBusy.Error()
	}
	t := e.Existing
	if e.Reused {
		return fmt.Sprintf("相同任务已在执行中：%s（%s，进度 %d%%），已为你复用该任务", t.Title, t.State, t.Progress)
	}
	return fmt.Sprintf("该节点已有同类任务在跑：%s（%s，进度 %d%%）", t.Title, t.State, t.Progress)
}

// Is 让 errors.Is(err, ErrTaskExclusiveBusy) 成立（调用方/路由据此映射 HTTP 409）。
func (e *TaskExclusiveBusyError) Is(target error) bool { return target == ErrTaskExclusiveBusy }

// Unwrap 保留底层哨兵错误语义。
func (e *TaskExclusiveBusyError) Unwrap() error { return ErrTaskExclusiveBusy }

// exclusiveGroupOf 把任务 kind 映射到互斥组；返回空串表示该 kind 不参与互斥。
//
// 【为什么要显式列出而不是默认全互斥】任务中心里还有大量「互不干扰」的长任务
// （备份、快照、克隆……它们各自操作自己的路径）。默认全互斥会让一台节点上
// 只能同时跑一件事，把并行能力砍掉。所以这里**只**登记确认会互相踩踏的那些组，
// 新增重型任务时按同样标准判断：它写的是不是别人也在写的那块资源。
func exclusiveGroupOf(kind string) string {
	switch kind {
	case model.TaskKindJDKInstall, model.TaskKindRuntimeInstall:
		// 运行时安装共用一个组：都写节点级托管运行时目录。
		return "runtime_install"
	case model.TaskKindPkgInstall:
		// 全局包安装写 <data>/opt/runtimes/global，与运行时二进制分离。
		return "pkg_install"
	default:
		return ""
	}
}

// ExclusiveGroupOf 导出给路由/前端做「这个动作会不会被占用」的预判展示。
func ExclusiveGroupOf(kind string) string { return exclusiveGroupOf(kind) }

// TaskAdmitSpec 一次重型任务的「准入申请」。
//
// 把「建任务」从单纯 INSERT 提升为「准入」的理由：一个重型任务的创建要同时满足两件事，
// 而它们必须在**同一个原子步骤**里判定，不能由调用方各查一次：
//  1. 幂等——同节点、同 kind、同参数的请求已在跑 → 直接复用那个任务（不是报错）；
//  2. 互斥——同节点、同互斥组已有别的任务在跑 → 拒绝，并告知是谁在占着。
type TaskAdmitSpec struct {
	NodeID     uint
	InstanceID uint
	Kind       string
	Title      string
	Detail     string
	CreatedBy  uint
	// IdemKey 幂等键（同节点同 kind 同参数 → 复用）。空串表示不参与幂等。
	IdemKey string
}

// AdmitTask 是重型任务创建的**唯一入口**：幂等复用 → 互斥拒绝 → 建任务。
//
// 返回 reused=true 表示命中了幂等键，返回的是**已存在**的那个任务（调用方不该再下发 Worker）。
//
// 【三层防护，缺一不可】
//  1. 这里先查一次：给出准确的人话错误（「JDK 21 正在装，进度 40%」），而不是一句 DB 约束冲突；
//  2. 数据库部分唯一索引：兜住「查完之后、插入之前」被并发插队的那个窗口；
//  3. 插入失败后再查一次：把 DB 约束错误翻译回同一个业务错误（见 isUniqueViolation）。
//
// 只做第 1 层会在并发下失效；只做第 2 层则错误信息用户看不懂。两层都要，第 3 层补翻译。
func (s *TaskService) AdmitTask(taskID string, spec TaskAdmitSpec) (*model.Task, bool, error) {
	group := exclusiveGroupOf(spec.Kind)
	idem := strings.TrimSpace(spec.IdemKey)

	// 第 1 层：幂等复用。相同参数的任务在跑 = 你要做的事已经在做了，复用它是正确语义。
	if idem != "" {
		existing, err := s.findActiveBy(spec.NodeID, "idem_key", idem)
		if err != nil {
			return nil, false, err
		}
		if existing != nil {
			return existing, true, nil
		}
	}

	// 第 1 层续：互斥拒绝。不同参数但抢同一块资源 → 不能并发。
	if group != "" {
		existing, err := s.findActiveByGroup(spec.NodeID, group)
		if err != nil {
			return nil, false, err
		}
		if existing != nil {
			return nil, false, &TaskExclusiveBusyError{Existing: existing}
		}
	}

	// 第 2 层：落库。列上带上互斥组与幂等键，让 DB 约束生效。
	t := &model.Task{
		TaskID:         taskID,
		NodeID:         spec.NodeID,
		InstanceID:     spec.InstanceID,
		Kind:           spec.Kind,
		State:          model.TaskStatePending,
		Title:          spec.Title,
		Detail:         spec.Detail,
		CreatedBy:      spec.CreatedBy,
		ExclusiveGroup: group,
		IdemKey:        idem,
	}
	if err := s.db.Create(t).Error; err != nil {
		// 第 3 层：把约束冲突翻译回业务语义——否则用户只会看到一句
		// 「UNIQUE constraint failed: tasks.node_id, tasks.idem_key」。
		existing, reused, handled, terr := s.translateTaskConflict(spec, err)
		if terr != nil {
			return nil, false, terr
		}
		if handled {
			return existing, reused, nil
		}
		return nil, false, fmt.Errorf("创建任务失败: %w", err)
	}
	return t, false, nil
}

// translateTaskConflict 把一个因唯一约束失败的插入，翻译回业务语义。
//
// 走到这里说明「检查时没有、插入时有了」——即另一个请求刚刚抢先进来了（这正是
// 应用层检查挡不住的那个窗口）。返回四元组：
//   - existing：命中的在途任务（handled 时非 nil）
//   - reused：命中的是幂等键 → 调用方应把它当「复用成功」，不再下发 Worker
//   - handled：是否已翻译（false 表示不是唯一约束冲突，调用方按原错误上抛）
//   - err：查询本身出错
func (s *TaskService) translateTaskConflict(spec TaskAdmitSpec, createErr error) (
	existing *model.Task, reused, handled bool, err error) {
	if !isUniqueViolation(createErr) {
		return nil, false, false, nil
	}
	idem := strings.TrimSpace(spec.IdemKey)
	if idem != "" {
		found, ferr := s.findActiveBy(spec.NodeID, "idem_key", idem)
		if ferr != nil {
			return nil, false, false, ferr
		}
		if found != nil {
			// 幂等命中不是错误：并发请求做的是同一件事，复用它即可。
			return found, true, true, nil
		}
	}
	if group := exclusiveGroupOf(spec.Kind); group != "" {
		found, ferr := s.findActiveByGroup(spec.NodeID, group)
		if ferr != nil {
			return nil, false, false, ferr
		}
		if found != nil {
			return nil, false, false, &TaskExclusiveBusyError{Existing: found}
		}
	}
	return nil, false, false, nil
}

// findActiveBy 按某个键列查同节点下的非终态任务。
func (s *TaskService) findActiveBy(nodeID uint, column, value string) (*model.Task, error) {
	var rows []model.Task
	err := s.db.
		Where(fmt.Sprintf("node_id = ? AND %s = ? AND state IN ?", column), nodeID, value,
			[]model.TaskState{model.TaskStatePending, model.TaskStateRunning}).
		Order("id asc").Limit(1).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("检查任务占用失败: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// findActiveByGroup 查同节点同互斥组的非终态任务。
//
// 兼容没有 exclusive_group 列的历史行（迁移前的数据该列为空）：此时退回按
// kind IN (同组全部 kind) 判定，行为与改造前一致，不会因为漏填列而放过占用。
func (s *TaskService) findActiveByGroup(nodeID uint, group string) (*model.Task, error) {
	var rows []model.Task
	err := s.db.
		Where("node_id = ? AND state IN ? AND (exclusive_group = ? OR (exclusive_group = '' AND kind IN ?))",
			nodeID,
			[]model.TaskState{model.TaskStatePending, model.TaskStateRunning},
			group, kindsInGroup(group)).
		Order("id asc").Limit(1).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("检查任务互斥失败: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// AcquireExclusive 是 AdmitTask 的**只检查不落库**版本，保留给已有调用方与测试。
//
// 【什么时候还用它】确实需要在「建任务之前」单独探测占用态的场景（例如预检接口）。
// 新代码应当直接用 AdmitTask——只有它才带 DB 约束兜底，单独调这个无法防并发。
func (s *TaskService) AcquireExclusive(nodeID uint, kind, _ string) error {
	group := exclusiveGroupOf(kind)
	if group == "" {
		return nil
	}
	existing, err := s.findActiveByGroup(nodeID, group)
	if err != nil {
		return err
	}
	if existing != nil {
		return &TaskExclusiveBusyError{Existing: existing}
	}
	return nil
}

// kindsInGroup 返回某互斥组包含的全部 kind（供 SQL 的 kind IN (...) 用）。
//
// 【为什么要在 Go 里反向算而不是在 SQL 里比较组名】组名是 Go 侧概念、DB 里只有 kind。
// 反向枚举一次比在 DB 存一列 group 更省迁移成本；组数量很小（个位数），代价可忽略。
func kindsInGroup(group string) []string {
	all := []string{
		model.TaskKindJDKInstall,
		model.TaskKindRuntimeInstall,
		model.TaskKindPkgInstall,
	}
	out := make([]string, 0, len(all))
	for _, k := range all {
		if exclusiveGroupOf(k) == group {
			out = append(out, k)
		}
	}
	return out
}

// BuildIdemKey 生成幂等键：同节点 + 同 kind + 同参数的确定性摘要。
//
// 【为什么用摘要而不是直接拼字符串】参数里可能带路径/镜像地址等长字段，
// 拼起来会超列宽，且不同参数拼接后可能撞车（"a"+"bc" 与 "ab"+"c"）。
// SHA-256 前 16 字节 hex（32 字符）既固定长度又无碰撞风险。
//
// 【为什么参数要按调用方给定顺序参与】这些参数是「这次操作要做什么」的身份标识，
// 顺序由调用点固定（如 vendor|major|arch），不在这里排序——排序反而会让
// 语义不同的调用（比如 arch 未指定 vs 指定 auto）算出同一个键。
func BuildIdemKey(nodeID uint, kind string, parts ...string) string {
	h := sha256.New()
	fmt.Fprintf(h, "node=%d\x00kind=%s", nodeID, kind)
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// DescribeExclusiveBusy 把占用者写成人读一句话（路由回给前端的 message 用）。
func DescribeExclusiveBusy(err error) string {
	var busy *TaskExclusiveBusyError
	if errors.As(err, &busy) {
		return busy.Error()
	}
	return strings.TrimSpace(err.Error())
}
