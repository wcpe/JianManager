package query

import (
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// ResourceBudget 由 Worker 注入共享准入器，查询面不自行采样或修改资源上限。
type ResourceBudget interface {
	Admit(vlsup.WorkClass) error
	Reserve(vlsup.ReservationKind, int64) (func(), error)
}

// SetResourceBudget 支持运行期装配，nil 保持既有兼容行为。
func (s *Service) SetResourceBudget(budget ResourceBudget) {
	s.resourceMu.Lock()
	s.resourceBudget = budget
	s.resourceMu.Unlock()
}

// NewServiceWithOptions 为独立装配场景提供与 setter 相同的入口。
func NewServiceWithOptions(planner *Planner, client RangeClient, buildID string, opts Options) *Service {
	s := NewService(planner, client, buildID)
	s.SetResourceBudget(opts.ResourceBudget)
	return s
}

// Close 在 Worker 停机时关闭所有固定视图。
func (s *Service) Close() error { return s.planner.Close() }

// ReleaseView 用于创建失败、取消交付或本地调用方主动结束读取。
func (s *Service) ReleaseView(viewID string) error { return s.planner.ReleaseView(viewID) }

// foregroundAdmission 在规划深拷贝和 sidecar IO 前先做轻量准入；实际读前仍复核资源变化。
func (s *Service) foregroundAdmission() *QueryError {
	s.resourceMu.RLock()
	budget := s.resourceBudget
	s.resourceMu.RUnlock()
	if budget != nil {
		if err := budget.Admit(vlsup.WorkForeground); err != nil {
			return &QueryError{Code: ErrCodeBudgetExceeded, Message: fmt.Sprintf("query admission: %v", err), Retryable: true, RetryAfter: 1000}
		}
	}
	return nil
}

func (s *Service) planForRead(ctx context.Context, req QueryRequest) PlanResult {
	if ctx.Err() != nil {
		return PlanResult{Err: newErr(ErrCodeCancelled, "query cancelled before planning"),
			Coverage: Coverage{PartialReasons: []string{ReasonCancelled}, EnumerationState: EnumCancelled}}
	}
	if err := s.foregroundAdmission(); err != nil {
		return PlanResult{Err: err, Coverage: Coverage{PartialReasons: []string{ReasonBudgetExceeded}, EnumerationState: EnumOpen}}
	}
	return s.planFromRequest(req)
}

// beginRead 的 pin 覆盖整个实际执行期；取消只禁止新读，不提前释放仍在读的协程。
func (s *Service) beginRead(ctx context.Context, req QueryRequest, plan *PlanResult) (func(), *QueryError) {
	created := req.View == nil || req.View.ViewID == ""
	viewID := viewIDOf(plan.View, req.View)
	fail := func(err *QueryError) (func(), *QueryError) {
		if created || (plan.View != nil && plan.View.Transient) {
			logLeaseCleanupError(s.planner.ReleaseView(viewID))
		}
		return func() {}, err
	}
	if ctx.Err() != nil {
		return fail(newErr(ErrCodeCancelled, "query cancelled before read"))
	}
	s.resourceMu.RLock()
	budget := s.resourceBudget
	s.resourceMu.RUnlock()
	var unreserve func()
	if budget != nil {
		if err := budget.Admit(vlsup.WorkForeground); err != nil {
			return fail(newErr(ErrCodeBudgetExceeded, fmt.Sprintf("query admission: %v", err)))
		}
		if req.AllowProjectionDrift {
			bytes := req.Budget.MaxBytes
			if bytes == 0 {
				bytes = DefaultMaxBytes
			}
			if bytes > math.MaxInt64 {
				return fail(newErr(ErrCodeBudgetExceeded, "export reservation overflow"))
			}
			var err error
			unreserve, err = budget.Reserve(vlsup.ReservationExport, int64(bytes))
			if err != nil {
				return fail(newErr(ErrCodeBudgetExceeded, fmt.Sprintf("export reservation: %v", err)))
			}
		}
	}
	pin, err := s.planner.PinRead(viewID)
	if err != nil {
		if unreserve != nil {
			unreserve()
		}
		return fail(newErr(ErrCodeViewStale, fmt.Sprintf("query read lease stale: %v", err)))
	}
	stopCancel := context.AfterFunc(ctx, func() {
		if created || (plan.View != nil && plan.View.Transient) {
			logLeaseCleanupError(s.planner.ReleaseView(viewID))
		}
	})
	var once sync.Once
	return func() {
		once.Do(func() {
			stopCancel()
			pin()
			if unreserve != nil {
				unreserve()
			}
			if plan.View != nil && plan.View.Transient || created && ctx.Err() != nil {
				logLeaseCleanupError(s.planner.ReleaseView(viewID))
			}
		})
	}, nil
}

// pinRange 在真正进入 RangeClient 前再次准入，TTL/淘汰后的排队任务不能借旧 request pin 新读。
func (s *Service) pinRange(ctx context.Context, plan *PlanResult) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return ctx, func() {}, err
	}
	pin, err := s.planner.PinRead(plan.View.ViewID)
	if err != nil {
		return ctx, func() {}, err
	}
	readCtx, cancel := context.WithTimeout(ctx, plan.View.ExpiresAt.Sub(s.planner.now()))
	if err := readCtx.Err(); err != nil {
		cancel()
		pin()
		return readCtx, func() {}, err
	}
	return readCtx, releaseOnce(func() { cancel(); pin() }), nil
}

// revalidateRead 防止到期、淘汰或关闭期间完成的旧请求再公布可续页或完整耗尽。
func (s *Service) revalidateRead(ctx context.Context, plan *PlanResult, queryErr **QueryError) {
	if ctx.Err() != nil {
		*queryErr = newErr(ErrCodeCancelled, "query cancelled during read")
		markReadError(&plan.Coverage, *queryErr)
		return
	}
	if *queryErr != nil {
		return
	}
	pin, err := s.planner.PinRead(plan.View.ViewID)
	if err != nil {
		*queryErr = newErr(ErrCodeViewStale, fmt.Sprintf("view invalidated during read: %v", err))
		markReadError(&plan.Coverage, *queryErr)
		return
	}
	pin()
}

func markReadError(coverage *Coverage, err *QueryError) {
	switch err.Code {
	case ErrCodeViewStale:
		coverage.EnumerationState = EnumStale
		coverage.MarkIncomplete(ReasonConflictGeneration)
	case ErrCodeCancelled:
		coverage.EnumerationState = EnumCancelled
		coverage.MarkIncomplete(ReasonCancelled)
	default:
		coverage.MarkIncomplete(ReasonBudgetExceeded)
	}
}
