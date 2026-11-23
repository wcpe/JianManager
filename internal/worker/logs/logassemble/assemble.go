// Package logassemble Worker 日志平台生产装配（T11）。
//
// 将 Catalog + QueryPlanner + query.Service + grpcsvc 组装为可注册到
// WorkerService 的日志查询面。生产由 apps/worker 注入受管 VL RangeClient；
// 未接线或 VL 不健康时用 UnimplementedRangeClient，能力协商返回 LOG_UNSUPPORTED。
package logassemble

import (
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/query/grpcsvc"
)

// Stack 装配结果。
type Stack struct {
	Catalog *catalog.Catalog
	Planner *query.Planner
	Query   *query.Service
	LogRPC  *grpcsvc.Service
}

// SetArchiveBackend connects Registry/Rehydrate to the Worker RPC surface.
func (s *Stack) SetArchiveBackend(backend query.ArchiveBackend) {
	if s != nil && s.Query != nil {
		s.Query.SetArchiveBackend(backend)
	}
}

// Build 组装日志查询栈。
// buildID 写入 capabilities（如 Worker 版本）；rangeClient 为 nil 时用 UnimplementedRangeClient。
func Build(journal catalog.Journal, rangeClient query.RangeClient, buildID string) *Stack {
	cat := catalog.New(journal)
	planner := query.NewPlanner(cat, nil)
	qs := query.NewService(planner, rangeClient, buildID)
	return &Stack{
		Catalog: cat,
		Planner: planner,
		Query:   qs,
		LogRPC:  grpcsvc.New(qs, planner),
	}
}

// BuildDisabled 返回显式禁用的日志 RPC（老 Worker 形态：supported=false）。
func BuildDisabled() *grpcsvc.Service {
	return grpcsvc.NewDisabled()
}
