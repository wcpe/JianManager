package grpc

import (
	"context"

	"github.com/wcpe/JianManager/internal/worker/logs/query/grpcsvc"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// SetLogQueryService 注入Log RPC 服务层（T11 装配）。
// nil 时 Log* 走 UnimplementedWorkerServiceServer 默认实现。
func (s *Server) SetLogQueryService(q *grpcsvc.Service) {
	if s == nil {
		return
	}
	s.logQuery = q
}

func (s *Server) logRPC() *grpcsvc.Service {
	if s == nil {
		return nil
	}
	q, _ := s.logQuery.(*grpcsvc.Service)
	return q
}

// GetLogCapabilities能力协商。
func (s *Server) GetLogCapabilities(ctx context.Context, req *workerpb.GetLogCapabilitiesRequest) (*workerpb.GetLogCapabilitiesResponse, error) {
	if q := s.logRPC(); q != nil {
		return q.GetLogCapabilities(ctx, req)
	}
	return s.UnimplementedWorkerServiceServer.GetLogCapabilities(ctx, req)
}

// LogCreateViewWorker-local Query View 创建。
func (s *Server) LogCreateView(ctx context.Context, req *workerpb.LogCreateViewRequest) (*workerpb.LogCreateViewResponse, error) {
	if q := s.logRPC(); q != nil {
		return q.LogCreateView(ctx, req)
	}
	return s.UnimplementedWorkerServiceServer.LogCreateView(ctx, req)
}

// LogSearch检索。
func (s *Server) LogSearch(ctx context.Context, req *workerpb.LogSearchRequest) (*workerpb.LogSearchResponse, error) {
	if q := s.logRPC(); q != nil {
		return q.LogSearch(ctx, req)
	}
	return s.UnimplementedWorkerServiceServer.LogSearch(ctx, req)
}

// LogStats聚合。
func (s *Server) LogStats(ctx context.Context, req *workerpb.LogStatsRequest) (*workerpb.LogStatsResponse, error) {
	if q := s.logRPC(); q != nil {
		return q.LogStats(ctx, req)
	}
	return s.UnimplementedWorkerServiceServer.LogStats(ctx, req)
}

// LogFields字段列表。
func (s *Server) LogFields(ctx context.Context, req *workerpb.LogFieldsRequest) (*workerpb.LogFieldsResponse, error) {
	if q := s.logRPC(); q != nil {
		return q.LogFields(ctx, req)
	}
	return s.UnimplementedWorkerServiceServer.LogFields(ctx, req)
}

// LogFacetsFacets。
func (s *Server) LogFacets(ctx context.Context, req *workerpb.LogFacetsRequest) (*workerpb.LogFacetsResponse, error) {
	if q := s.logRPC(); q != nil {
		return q.LogFacets(ctx, req)
	}
	return s.UnimplementedWorkerServiceServer.LogFacets(ctx, req)
}

// LogStreams stream 枚举（窗内有哪些流、各多少条）。
//
// 必须在此显式 delegate：Server 嵌入的是 UnimplementedWorkerServiceServer，
// 缺了这些方法时调用会**恒返回 Unimplemented**，而 CP 会把它当成
// 「这台 Worker 不支持 streams」——功能上线了但永远用不了。
func (s *Server) LogStreams(ctx context.Context, req *workerpb.LogStreamsRequest) (*workerpb.LogStreamsResponse, error) {
	if q := s.logRPC(); q != nil {
		return q.LogStreams(ctx, req)
	}
	return s.UnimplementedWorkerServiceServer.LogStreams(ctx, req)
}

// LogStreamFieldNames stream 标签维度名。
func (s *Server) LogStreamFieldNames(ctx context.Context, req *workerpb.LogStreamFieldNamesRequest) (*workerpb.LogStreamFieldNamesResponse, error) {
	if q := s.logRPC(); q != nil {
		return q.LogStreamFieldNames(ctx, req)
	}
	return s.UnimplementedWorkerServiceServer.LogStreamFieldNames(ctx, req)
}

// LogStreamFieldValues stream 标签维度取值分布。
func (s *Server) LogStreamFieldValues(ctx context.Context, req *workerpb.LogStreamFieldValuesRequest) (*workerpb.LogStreamFieldValuesResponse, error) {
	if q := s.logRPC(); q != nil {
		return q.LogStreamFieldValues(ctx, req)
	}
	return s.UnimplementedWorkerServiceServer.LogStreamFieldValues(ctx, req)
}

// LogTail 流式 Tail。
func (s *Server) LogTail(req *workerpb.LogTailRequest, stream workerpb.WorkerService_LogTailServer) error {
	if q := s.logRPC(); q != nil {
		return q.LogTail(req, stream)
	}
	return s.UnimplementedWorkerServiceServer.LogTail(req, stream)
}

// LogRehydrate恢复任务入口（未实现时 LOG_UNSUPPORTED）。
func (s *Server) LogRehydrate(ctx context.Context, req *workerpb.LogRehydrateRequest) (*workerpb.LogTaskResponse, error) {
	if q := s.logRPC(); q != nil {
		return q.LogRehydrate(ctx, req)
	}
	return s.UnimplementedWorkerServiceServer.LogRehydrate(ctx, req)
}

// LogArchiveStatus归档状态。
func (s *Server) LogArchiveStatus(ctx context.Context, req *workerpb.LogArchiveStatusRequest) (*workerpb.LogArchiveStatusResponse, error) {
	if q := s.logRPC(); q != nil {
		return q.LogArchiveStatus(ctx, req)
	}
	return s.UnimplementedWorkerServiceServer.LogArchiveStatus(ctx, req)
}

// LogRuntimeStatus受管 VictoriaLogs namespace 状态。
func (s *Server) LogRuntimeStatus(ctx context.Context, req *workerpb.LogRuntimeStatusRequest) (*workerpb.LogRuntimeStatusResponse, error) {
	if q := s.logRPC(); q != nil {
		return q.LogRuntimeStatus(ctx, req)
	}
	return s.UnimplementedWorkerServiceServer.LogRuntimeStatus(ctx, req)
}

// LogRuntimeControl经 CP 隧道控制受管 namespace。
func (s *Server) LogRuntimeControl(ctx context.Context, req *workerpb.LogRuntimeControlRequest) (*workerpb.LogTaskResponse, error) {
	if q := s.logRPC(); q != nil {
		return q.LogRuntimeControl(ctx, req)
	}
	return s.UnimplementedWorkerServiceServer.LogRuntimeControl(ctx, req)
}

func (s *Server) LogMigratePartition(ctx context.Context, req *workerpb.LogMigratePartitionRequest) (*workerpb.LogTaskResponse, error) {
	if q := s.logRPC(); q != nil {
		return q.LogMigratePartition(ctx, req)
	}
	return s.UnimplementedWorkerServiceServer.LogMigratePartition(ctx, req)
}

