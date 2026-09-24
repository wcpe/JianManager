package logcoord

import "context"

// TargetResolver 将 CP 鉴权得到的授权目标 ID 解析为可查询目标信息。
//
// 实现必须返回全部授权目标（含历史数据持有者），不得在解析层做 online 过滤。
type TargetResolver interface {
	Resolve(ctx context.Context, targetIDs []string) ([]TargetInfo, error)
}

// WorkerDialer 按 WorkerID 获取日志查询客户端（生产为反向 gRPC 隧道）。
type WorkerDialer interface {
	Dial(ctx context.Context, workerID string) (WorkerClient, error)
}

// WorkerSearchRequest 单 Worker Search 请求。
type WorkerSearchRequest struct {
	TargetIDs    []string    `json:"target_ids"`
	FromUTC      string      `json:"from_utc"`
	ToUTC        string      `json:"to_utc"`
	Filter       string      `json:"filter"`
	Budget       QueryBudget `json:"budget"`
	ViewID       string      `json:"view_id"`
	Cursor       string      `json:"cursor,omitempty"`
	OrderVersion string      `json:"order_version"`
	MetricField  string      `json:"metric_field,omitempty"`
}

// WorkerTargetResult Worker 侧单目标覆盖/查询结果。
type WorkerTargetResult struct {
	TargetID          string        `json:"target_id"`
	State             CoverageState `json:"state"`
	Reasons           []string      `json:"reasons,omitempty"`
	ClosedVisibleSeq  string        `json:"closed_visible_seq,omitempty"`
	CatalogGeneration string        `json:"catalog_generation,omitempty"`
	// Metric 可选数值（Stats 用）。
	MetricSum   float64 `json:"metric_sum,omitempty"`
	MetricMin   float64 `json:"metric_min,omitempty"`
	MetricMax   float64 `json:"metric_max,omitempty"`
	MetricCount uint64  `json:"metric_count,omitempty"`
}

// WorkerSearchResponse 单 Worker Search 响应。
type WorkerSearchResponse struct {
	ViewID     string               `json:"view_id"`
	Items      []Event              `json:"items"`
	Targets    []WorkerTargetResult `json:"targets"`
	Exhausted  bool                 `json:"exhausted"`
	NextCursor string               `json:"next_cursor,omitempty"`
	// Truncated Worker 侧因预算/上限截断。
	Truncated   bool   `json:"truncated"`
	Unsupported bool   `json:"unsupported"`
	Error       string `json:"error,omitempty"`
}

// WorkerStatsPoint 单 Worker 统计点（已按维度/时间桶预聚合）。
type WorkerStatsPoint struct {
	Dimensions    map[string]string `json:"dimensions,omitempty"`
	TimeBucketUTC string            `json:"time_bucket_utc,omitempty"`
	Agg           StatsAggregate    `json:"agg"`
}

// WorkerStatsResponse 单 Worker Stats 响应。
type WorkerStatsResponse struct {
	ViewID  string               `json:"view_id"`
	Points  []WorkerStatsPoint   `json:"points"`
	Targets []WorkerTargetResult `json:"targets"`
	// Truncated 统计输入不完整等。
	Truncated   bool   `json:"truncated"`
	Unsupported bool   `json:"unsupported"`
	Error       string `json:"error,omitempty"`
}

// WorkerFacetDimension 单 Worker 维度 Facet。
type WorkerFacetDimension struct {
	Dimension      string       `json:"dimension"`
	Values         []FacetValue `json:"values"`
	Truncated      bool         `json:"truncated"`
	TruncatedCount uint64       `json:"truncated_count"`
}

// WorkerFacetsResponse 单 Worker Facets 响应。
type WorkerFacetsResponse struct {
	ViewID      string                 `json:"view_id"`
	Dimensions  []WorkerFacetDimension `json:"dimensions"`
	Targets     []WorkerTargetResult   `json:"targets"`
	Unsupported bool                   `json:"unsupported"`
	Error       string                 `json:"error,omitempty"`
}

// WorkerClient 单 Worker 日志查询面（FR-478 契约的协调器侧投影）。
// 本包不实现隧道传输；测试注入 fake。
type WorkerClient interface {
	OpenView(ctx context.Context, req WorkerSearchRequest) (*WorkerSearchResponse, error)
	Search(ctx context.Context, req WorkerSearchRequest) (*WorkerSearchResponse, error)
	Tail(ctx context.Context, req WorkerSearchRequest, mode string) (*WorkerSearchResponse, error)
	Fields(ctx context.Context, req WorkerSearchRequest) (*WorkerFieldsResponse, error)
	Stats(ctx context.Context, req WorkerSearchRequest, groupBy []string, timeBucket string) (*WorkerStatsResponse, error)
	Facets(ctx context.Context, req WorkerSearchRequest, dimensions []string, dimensionLimit uint32) (*WorkerFacetsResponse, error)
}

type WorkerFieldsResponse struct {
	ViewID      string               `json:"view_id"`
	Fields      []string             `json:"fields"`
	Targets     []WorkerTargetResult `json:"targets"`
	Unsupported bool                 `json:"unsupported"`
	Error       string               `json:"error,omitempty"`
}
