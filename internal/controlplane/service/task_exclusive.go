package service

import (
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
}

func (e *TaskExclusiveBusyError) Error() string {
	if e == nil || e.Existing == nil {
		return ErrTaskExclusiveBusy.Error()
	}
	t := e.Existing
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

// AcquireExclusive 在创建任务**之前**检查互斥占位。
//
// 【为什么在 CreateTask 之前调用而不是在 CreateTask 内部】调用方需要在真正下发
// Worker RPC 之前就知道被拒（否则会先建任务、再下发、再回滚，留下一串失败任务）。
// 所以这里只做「检查 + 返回占用者」，真正落库由随后的 CreateTask 完成。
//
// 【为什么不是绝对原子】检查与插入之间有极小的窗口（两个请求同时通过检查）。
// 要做到严格原子得靠 DB 唯一索引：
//
//	CREATE UNIQUE INDEX ... ON tasks(node_id, <group>) WHERE state IN ('pending','running')
//
// 那是部分索引，SQLite/MySQL/PG 语法各不相同，且需要迁移脚本。当前规模下
// （同一操作员对同一节点并发点两次的概率极低，且前端已有同步闸门）
// 用「检查 + 已有的前端闸门」覆盖现实风险；这里的返回值也让上层能把占用者
// 直接展示给用户，比一个光秃秃的 409 有用得多。**若将来发现真的被并发打穿，
// 上部分唯一索引是正确解法**（注释留在此处，避免后人重复踩空）。
func (s *TaskService) AcquireExclusive(nodeID uint, kind, _ string) error {
	group := exclusiveGroupOf(kind)
	if group == "" {
		return nil
	}
	var busy []model.Task
	// 同节点、同组、非终态即视为占用。终态判定用 NOT IN 而不是 state='pending' OR ...，
	// 这样将来新增中间态不必再改这里。
	if err := s.db.
		Where("node_id = ? AND kind IN ? AND state NOT IN ?",
			nodeID, kindsInGroup(group), []model.TaskState{
				model.TaskStateSucceeded, model.TaskStateFailed, model.TaskStateCanceled,
			}).
		Order("id asc").
		Limit(1).
		Find(&busy).Error; err != nil {
		return fmt.Errorf("检查任务互斥失败: %w", err)
	}
	if len(busy) > 0 {
		return &TaskExclusiveBusyError{Existing: &busy[0]}
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

// DescribeExclusiveBusy 把占用者写成人读一句话（路由回给前端的 message 用）。
func DescribeExclusiveBusy(err error) string {
	var busy *TaskExclusiveBusyError
	if errors.As(err, &busy) {
		return busy.Error()
	}
	return strings.TrimSpace(err.Error())
}
