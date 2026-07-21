package service

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ClientDistTrackingService 客户端分发拉取/下载追踪（FR-093，见 ADR-023）。
//
// 数据量治理：明细 `client_dist_event` 短保留 + 后台滚动清理；写时增量 upsert 聚合 `client_dist_daily` 长保留。
// 写入弱一致 best-effort——失败仅返回错误供调用方忽略，**绝不阻断玩家拉取**。
type ClientDistTrackingService struct {
	db            *gorm.DB
	retentionDays int
	cleanupEvery  time.Duration
	stop          chan struct{}
}

// NewClientDistTrackingService 创建追踪服务（明细默认保留 14 天，每 6h 清理）。
func NewClientDistTrackingService(db *gorm.DB) *ClientDistTrackingService {
	return &ClientDistTrackingService{
		db:            db,
		retentionDays: 14,
		cleanupEvery:  6 * time.Hour,
		stop:          make(chan struct{}),
	}
}

// ClientDistEventInput 一次拉取/下载事件输入。
type ClientDistEventInput struct {
	ChannelID   string
	MachineID   string
	IP          string
	Kind        string // manifest | artifact
	Version     int
	ArtifactSHA string
	Bytes       int64
	Status      int
	// ErrCode 语义错误码（FR-249）；成功事件留空。
	ErrCode string
	// ErrReason 可读错误原因（FR-265）。
	ErrReason  string
	DurationMs int64
	// FR-265 日志详情字段：保存排障所需的请求/响应详情；凭证类内容不落库。
	Method          string
	Path            string
	RequestBody     string
	ResponseBody    string
	RequestHeaders  map[string]string
	ResponseHeaders map[string]string
}

// ClientDistEventDetail 单条分发请求详情（FR-265）。
type ClientDistEventDetail struct {
	model.ClientDistEvent
	PlayerName     string            `json:"playerName"`
	CoreVersion    string            `json:"coreVersion"`
	RequestBody     string            `json:"requestBody"`
	ResponseBody    string            `json:"responseBody"`
	RequestHeaders  map[string]string `json:"requestHeaders"`
	ResponseHeaders map[string]string `json:"responseHeaders"`
}

// ClientDistEventSearchFilter 分页检索条件（FR-265）。
type ClientDistEventSearchFilter struct {
	ClientDistEventFilter
	ArtifactSHA    string
	RuntimeVersion *int
	CoreVersion    string
	Platform       string
	Lag            *int
	Page           int
	PageSize       int
}

// ClientDistEventView 分发事件与运行态诊断字段（FR-357，运行态字段为尽力关联）。
type ClientDistEventView struct {
	model.ClientDistEvent
	PlayerName string `json:"playerName"`
	CoreVersion string `json:"coreVersion"`
}

// ClientDistEventPage 分页检索响应（FR-265/357）。
type ClientDistEventPage struct {
	Items    []ClientDistEventView `json:"items"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"pageSize"`
	Total    int64                 `json:"total"`
}

// ClientDistErrorSummaryQuery 错误摘要查询条件（FR-357）。
type ClientDistErrorSummaryQuery struct {
	ChannelID string
	From      time.Time
	To        time.Time
	TopN      int
	SampleLimit int
}

// ClientDistErrorCount 错误码聚合项。
type ClientDistErrorCount struct {
	ErrCode string `json:"errCode"`
	Count   int64  `json:"count"`
}

type errorCountGroup struct {
	ErrCode string
	Status  int
	Count   int64
}

// ClientDistFailureSample 最近失败样例，敏感维度在服务端脱敏。
type ClientDistFailureSample struct {
	ID        uint      `json:"id"`
	Time      time.Time `json:"time"`
	ChannelID string    `json:"channelId"`
	Kind      string    `json:"kind"`
	ErrCode   string    `json:"errCode"`
	ErrReason string    `json:"errReason"`
	Status    int       `json:"status"`
	IP        string    `json:"ip"`
	MachineID string    `json:"machineId"`
}

// ClientDistErrorSummary 错误码 TopN 与最近失败样例。
type ClientDistErrorSummary struct {
	From      time.Time                 `json:"from"`
	To        time.Time                 `json:"to"`
	TopErrors []ClientDistErrorCount    `json:"topErrors"`
	Samples   []ClientDistFailureSample `json:"samples"`
}

// ClientDistRealtimeQuery 近实时查询参数（FR-265）。
type ClientDistRealtimeQuery struct {
	ChannelID string
	Now       time.Time
}

// ClientDistRealtimeSummary 近 1h KPI。
type ClientDistRealtimeSummary struct {
	ManifestPulls  int64 `json:"manifestPulls"`
	ArtifactPulls  int64 `json:"artifactPulls"`
	ErrorRequests  int64 `json:"errorRequests"`
	ActiveMachines int64 `json:"activeMachines"`
}

// ClientDistRatePoint 24h 请求速率点。
type ClientDistRatePoint struct {
	TS       time.Time `json:"ts"`
	Manifest int64     `json:"manifest"`
	Artifact int64     `json:"artifact"`
	Error    int64     `json:"error"`
}

// ClientDistRecentError 最近错误事件。
type ClientDistRecentError struct {
	ID        uint      `json:"id"`
	Time      time.Time `json:"time"`
	ChannelID string    `json:"channelId"`
	Kind      string    `json:"kind"`
	Target    string    `json:"target"`
	IP        string    `json:"ip"`
	Status    int       `json:"status"`
	ErrCode   string    `json:"errCode"`
}

// ClientDistRealtime 近实时观测结果。
type ClientDistRealtime struct {
	Summary1h      ClientDistRealtimeSummary `json:"summary1h"`
	RequestRate24h []ClientDistRatePoint     `json:"requestRate24h"`
	RecentErrors   []ClientDistRecentError   `json:"recentErrors"`
	TopIPs1h       []StatsIP                 `json:"topIps1h"`
}

// Record 记录一次拉取/下载事件：写明细 + 写时增量 upsert 当日聚合。best-effort（失败不阻断）。
func (s *ClientDistTrackingService) Record(e ClientDistEventInput) error {
	// 制品端点跨频道共享、路径无频道段，故 ChannelID 可空（按 kind 全局聚合）；仅 kind 空才跳过。
	if s == nil || e.Kind == "" {
		return nil
	}
	if len(e.MachineID) > machineIDMaxLen {
		e.MachineID = e.MachineID[:machineIDMaxLen]
	}
	now := time.Now()
	reqHeaders := sanitizeRequestHeaders(e.RequestHeaders)
	respHeaders := sanitizeResponseHeaders(e.ResponseHeaders)
	ev := &model.ClientDistEvent{
		ChannelID: e.ChannelID, MachineID: e.MachineID, IP: e.IP, Kind: e.Kind,
		Version: e.Version, ArtifactSHA: e.ArtifactSHA, Bytes: e.Bytes, Status: e.Status,
		ErrCode: e.ErrCode, ErrReason: trunc(e.ErrReason, 255), DurationMs: e.DurationMs,
		Method: strings.ToUpper(trunc(e.Method, 8)), Path: sanitizePath(e.Path),
		RequestBody: trunc(e.RequestBody, 4096), ResponseBody: trunc(e.ResponseBody, 4096),
		RequestHeadersJSON: mustJSON(reqHeaders), ResponseHeadersJSON: mustJSON(respHeaders),
		ETag: trunc(respHeaders["ETag"], 128), CreatedAt: now,
	}
	if err := s.db.Create(ev).Error; err != nil {
		return fmt.Errorf("写分发明细失败: %w", err)
	}
	day := now.UTC().Format("2006-01-02")
	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "day"}, {Name: "channel_id"}, {Name: "version"}, {Name: "kind"}},
		DoUpdates: clause.Assignments(map[string]any{
			"requests": gorm.Expr("requests + 1"),
			"bytes":    gorm.Expr("bytes + ?", e.Bytes),
		}),
	}).Create(&model.ClientDistDaily{
		Day: day, ChannelID: e.ChannelID, Version: e.Version, Kind: e.Kind, Requests: 1, Bytes: e.Bytes,
	}).Error
}

// Cleanup 删除早于保留期的明细行（聚合长留）；返回删除行数。
func (s *ClientDistTrackingService) Cleanup() (int64, error) {
	cutoff := time.Now().Add(-time.Duration(s.retentionDays) * 24 * time.Hour)
	res := s.db.Where("created_at < ?", cutoff).Delete(&model.ClientDistEvent{})
	return res.RowsAffected, res.Error
}

// Start 启动后台滚动清理循环（FR-093 数据量治理，仿 FR-060）。
func (s *ClientDistTrackingService) Start() {
	go func() {
		t := time.NewTicker(s.cleanupEvery)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				_, _ = s.Cleanup()
			}
		}
	}()
}

// Stop 停止后台清理循环。
func (s *ClientDistTrackingService) Stop() { close(s.stop) }

// ClientDistEventFilter 明细检索过滤条件（FR-093 检索；FR-249 增 Outcome/ErrCode）。空字段不约束。
type ClientDistEventFilter struct {
	ChannelID string
	MachineID string
	IP        string
	Kind      string
	// Outcome 成功/失败维度（FR-249）："success"（0<status<400，含 200/206/304）| "failure"（status>=400）| ""（不约束）。
	Outcome string
	// ErrCode 语义错误码精确筛（FR-249）。
	ErrCode string
	Version *int
	Since   *time.Time
	Until   *time.Time
	Limit   int
}

// QueryEvents 按条件检索明细（created_at DESC）。供管理面追溯（IP/机器码/频道/版本/时间）。
func (s *ClientDistTrackingService) QueryEvents(f ClientDistEventFilter) ([]model.ClientDistEvent, error) {
	q := s.applyEventFilter(s.db.Model(&model.ClientDistEvent{}), f)
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	var events []model.ClientDistEvent
	if err := q.Order("created_at DESC").Limit(limit).Find(&events).Error; err != nil {
		return nil, fmt.Errorf("检索分发明细失败: %w", err)
	}
	return events, nil
}

// SearchEvents 分页检索明细，支持运行态维度筛选（FR-265）。
func (s *ClientDistTrackingService) SearchEvents(f ClientDistEventSearchFilter) (*ClientDistEventPage, error) {
	page, size := normalizePage(f.Page, f.PageSize)
	q := s.applyEventFilter(s.db.Model(&model.ClientDistEvent{}), f.ClientDistEventFilter)
	if f.ArtifactSHA != "" {
		q = q.Where("artifact_sha = ?", f.ArtifactSHA)
	}
	q = s.applyRuntimeFilters(q, f)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("统计分发明细失败: %w", err)
	}
	var events []model.ClientDistEvent
	if err := q.Order("created_at DESC").Offset((page - 1) * size).Limit(size).Find(&events).Error; err != nil {
		return nil, fmt.Errorf("检索分发明细失败: %w", err)
	}
	items, err := s.enrichEventViews(events)
	if err != nil {
		return nil, fmt.Errorf("关联分发运行态失败: %w", err)
	}
	return &ClientDistEventPage{Items: items, Page: page, PageSize: size, Total: total}, nil
}

// ErrorSummary 聚合错误码 TopN 与最近失败样例（FR-357）。
func (s *ClientDistTrackingService) ErrorSummary(q ClientDistErrorSummaryQuery) (*ClientDistErrorSummary, error) {
	topN, sampleLimit := normalizeErrorSummaryLimits(q.TopN, q.SampleLimit)
	base := s.errorEventQuery(q)
	var groups []errorCountGroup
	if err := base.Session(&gorm.Session{}).Select("err_code, status, COUNT(*) AS count").Group("err_code, status").Scan(&groups).Error; err != nil {
		return nil, fmt.Errorf("聚合分发错误码失败: %w", err)
	}
	counts := mergeErrorCounts(groups)
	if len(counts) > topN {
		counts = counts[:topN]
	}
	var events []model.ClientDistEvent
	if err := base.Session(&gorm.Session{}).Order("created_at DESC").Limit(sampleLimit).Find(&events).Error; err != nil {
		return nil, fmt.Errorf("查询分发失败样例失败: %w", err)
	}
	return &ClientDistErrorSummary{From: q.From, To: q.To, TopErrors: counts, Samples: failureSamples(events)}, nil
}

// GetEventDetail 返回单条明细与脱敏 header（FR-265）。
func (s *ClientDistTrackingService) GetEventDetail(id uint) (*ClientDistEventDetail, error) {
	var ev model.ClientDistEvent
	if err := s.db.First(&ev, id).Error; err != nil {
		return nil, err
	}
	views, err := s.enrichEventViews([]model.ClientDistEvent{ev})
	if err != nil {
		return nil, err
	}
	return &ClientDistEventDetail{
		ClientDistEvent: ev,
		PlayerName:      views[0].PlayerName,
		CoreVersion:     views[0].CoreVersion,
		RequestBody:     ev.RequestBody,
		ResponseBody:    ev.ResponseBody,
		RequestHeaders:  parseHeaderJSON(ev.RequestHeadersJSON),
		ResponseHeaders: parseHeaderJSON(ev.ResponseHeadersJSON),
	}, nil
}

// Realtime 返回近实时分发请求健康度（FR-265）。
func (s *ClientDistTrackingService) Realtime(q ClientDistRealtimeQuery) (*ClientDistRealtime, error) {
	now := q.Now
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	out := &ClientDistRealtime{RequestRate24h: []ClientDistRatePoint{}, RecentErrors: []ClientDistRecentError{}, TopIPs1h: []StatsIP{}}
	base1h := s.db.Model(&model.ClientDistEvent{}).Where("created_at >= ?", now.Add(-time.Hour))
	base24h := s.db.Model(&model.ClientDistEvent{}).Where("created_at >= ?", now.Add(-24*time.Hour))
	if q.ChannelID != "" {
		base1h = base1h.Where("channel_id = ?", q.ChannelID)
		base24h = base24h.Where("channel_id = ?", q.ChannelID)
	}
	if err := base1h.Session(&gorm.Session{}).Where("kind = ?", "manifest").Count(&out.Summary1h.ManifestPulls).Error; err != nil {
		return nil, err
	}
	if err := base1h.Session(&gorm.Session{}).Where("kind = ?", "artifact").Count(&out.Summary1h.ArtifactPulls).Error; err != nil {
		return nil, err
	}
	if err := base1h.Session(&gorm.Session{}).Where("status >= ?", 400).Count(&out.Summary1h.ErrorRequests).Error; err != nil {
		return nil, err
	}
	if err := base1h.Session(&gorm.Session{}).Where("machine_id != ''").Distinct("machine_id").Count(&out.Summary1h.ActiveMachines).Error; err != nil {
		return nil, err
	}
	if err := realtimeRate(base24h, &out.RequestRate24h); err != nil {
		return nil, err
	}
	if err := recentErrors(base24h, &out.RecentErrors); err != nil {
		return nil, err
	}
	if err := topIPs(base1h, &out.TopIPs1h); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ClientDistTrackingService) applyEventFilter(q *gorm.DB, f ClientDistEventFilter) *gorm.DB {
	if f.ChannelID != "" {
		q = q.Where("channel_id = ?", f.ChannelID)
	}
	if f.MachineID != "" {
		q = q.Where("machine_id = ?", f.MachineID)
	}
	if f.IP != "" {
		q = q.Where("ip = ?", f.IP)
	}
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	switch f.Outcome {
	case "failure":
		q = q.Where("status >= ?", 400)
	case "success":
		q = q.Where("status > 0 AND status < ?", 400)
	}
	if f.ErrCode != "" {
		q = q.Where("err_code = ?", f.ErrCode)
	}
	if f.Version != nil {
		q = q.Where("version = ?", *f.Version)
	}
	if f.Since != nil {
		q = q.Where("created_at >= ?", *f.Since)
	}
	if f.Until != nil {
		q = q.Where("created_at <= ?", *f.Until)
	}
	return q
}

func (s *ClientDistTrackingService) enrichEventViews(events []model.ClientDistEvent) ([]ClientDistEventView, error) {
	items := make([]ClientDistEventView, len(events))
	machines := make([]string, 0, len(events))
	for i, event := range events {
		items[i].ClientDistEvent = event
		if event.MachineID != "" {
			machines = append(machines, event.MachineID)
		}
	}
	if len(machines) == 0 {
		return items, nil
	}
	var states []model.ClientRuntimeState
	if err := s.db.Where("machine_id IN ?", machines).Find(&states).Error; err != nil {
		return nil, err
	}
	byIdentity := make(map[string]model.ClientRuntimeState, len(states))
	for _, state := range states {
		byIdentity[state.ChannelID+"\x00"+state.MachineID] = state
	}
	for i := range items {
		state := byIdentity[items[i].ChannelID+"\x00"+items[i].MachineID]
		items[i].PlayerName = state.PlayerName
		items[i].CoreVersion = state.CoreVersion
	}
	return items, nil
}

func (s *ClientDistTrackingService) errorEventQuery(q ClientDistErrorSummaryQuery) *gorm.DB {
	db := s.db.Model(&model.ClientDistEvent{}).Where("(status >= ? OR err_code != '')", 400)
	if q.ChannelID != "" {
		db = db.Where("channel_id = ?", q.ChannelID)
	}
	if !q.From.IsZero() {
		db = db.Where("created_at >= ?", q.From)
	}
	if !q.To.IsZero() {
		db = db.Where("created_at < ?", q.To)
	}
	return db
}

func normalizeErrorSummaryLimits(topN, samples int) (int, int) {
	if topN <= 0 || topN > 50 {
		topN = 10
	}
	if samples <= 0 || samples > 100 {
		samples = 20
	}
	return topN, samples
}

func mergeErrorCounts(groups []errorCountGroup) []ClientDistErrorCount {
	merged := make(map[string]int64, len(groups))
	for _, group := range groups {
		code := group.ErrCode
		if code == "" {
			code = fmt.Sprintf("HTTP_%d", group.Status)
		}
		merged[code] += group.Count
	}
	out := make([]ClientDistErrorCount, 0, len(merged))
	for code, count := range merged {
		out = append(out, ClientDistErrorCount{ErrCode: code, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].ErrCode < out[j].ErrCode
		}
		return out[i].Count > out[j].Count
	})
	return out
}

func failureSamples(events []model.ClientDistEvent) []ClientDistFailureSample {
	out := make([]ClientDistFailureSample, 0, len(events))
	for _, event := range events {
		code := event.ErrCode
		if code == "" {
			code = fmt.Sprintf("HTTP_%d", event.Status)
		}
		out = append(out, ClientDistFailureSample{
			ID: event.ID, Time: event.CreatedAt, ChannelID: event.ChannelID, Kind: event.Kind,
			ErrCode: code, ErrReason: event.ErrReason, Status: event.Status,
			IP: maskEventIP(event.IP), MachineID: maskEventMachine(event.MachineID),
		})
	}
	return out
}

func maskEventMachine(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 10 {
		return "***"
	}
	return value[:6] + "…" + value[len(value)-4:]
}

func maskEventIP(value string) string {
	parts := strings.Split(value, ".")
	if len(parts) == 4 {
		parts[3] = "*"
		return strings.Join(parts, ".")
	}
	parts = strings.Split(value, ":")
	if len(parts) > 2 {
		return strings.Join(parts[:2], ":") + ":*"
	}
	return value
}

func (s *ClientDistTrackingService) applyRuntimeFilters(q *gorm.DB, f ClientDistEventSearchFilter) *gorm.DB {
	if f.RuntimeVersion == nil && f.CoreVersion == "" && f.Platform == "" && f.Lag == nil {
		return q
	}
	var states []model.ClientRuntimeState
	rq := s.db.Model(&model.ClientRuntimeState{})
	if f.ChannelID != "" {
		rq = rq.Where("channel_id = ?", f.ChannelID)
	}
	if f.RuntimeVersion != nil {
		rq = rq.Where("local_version = ?", *f.RuntimeVersion)
	}
	if f.CoreVersion != "" {
		rq = rq.Where("core_version = ?", f.CoreVersion)
	}
	if f.Platform != "" {
		rq = rq.Where("platform = ?", f.Platform)
	}
	if err := rq.Find(&states).Error; err != nil || len(states) == 0 {
		return q.Where("1 = 0")
	}
	if f.Lag != nil {
		latest := latestByChannel(states)
		filtered := states[:0]
		for _, st := range states {
			lag := latest[st.ChannelID] - st.LocalVersion
			if lag < 0 {
				lag = 0
			}
			if lag == *f.Lag {
				filtered = append(filtered, st)
			}
		}
		states = filtered
	}
	machines := make([]string, 0, len(states))
	for _, st := range states {
		machines = append(machines, st.MachineID)
	}
	if len(machines) == 0 {
		return q.Where("1 = 0")
	}
	return q.Where("machine_id IN ?", machines)
}

func normalizePage(page, size int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 100
	}
	if size > 500 {
		size = 500
	}
	return page, size
}

func realtimeRate(q *gorm.DB, out *[]ClientDistRatePoint) error {
	var rows []model.ClientDistEvent
	if err := q.Find(&rows).Error; err != nil {
		return err
	}
	byHour := map[string]*ClientDistRatePoint{}
	var order []string
	for _, r := range rows {
		ts := r.CreatedAt.UTC().Truncate(time.Hour)
		key := ts.Format(time.RFC3339)
		p := byHour[key]
		if p == nil {
			p = &ClientDistRatePoint{TS: ts}
			byHour[key] = p
			order = append(order, key)
		}
		switch r.Kind {
		case "manifest":
			p.Manifest++
		case "artifact":
			p.Artifact++
		}
		if r.Status >= 400 {
			p.Error++
		}
	}
	sort.Strings(order)
	for _, k := range order {
		*out = append(*out, *byHour[k])
	}
	return nil
}

func recentErrors(q *gorm.DB, out *[]ClientDistRecentError) error {
	var rows []model.ClientDistEvent
	if err := q.Where("status >= ?", 400).Order("created_at DESC").Limit(10).Find(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		target := r.ArtifactSHA
		if r.Kind == "manifest" && r.Version > 0 {
			target = fmt.Sprintf("v%d", r.Version)
		}
		*out = append(*out, ClientDistRecentError{ID: r.ID, Time: r.CreatedAt, ChannelID: r.ChannelID, Kind: r.Kind, Target: target, IP: r.IP, Status: r.Status, ErrCode: r.ErrCode})
	}
	return nil
}

func topIPs(q *gorm.DB, out *[]StatsIP) error {
	return q.Select("ip, COUNT(*) AS count").Where("ip != ''").Group("ip").Order("count DESC").Limit(10).Scan(out).Error
}

func sanitizeRequestHeaders(in map[string]string) map[string]string {
	return sanitizeHeaders(in)
}

func sanitizeResponseHeaders(in map[string]string) map[string]string {
	return sanitizeHeaders(in)
}

func sanitizeHeaders(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		canon := canonicalHeader(k)
		if secretHeader(canon) {
			if v != "" && strings.EqualFold(canon, "X-Client-Key") {
				out[canon] = "present"
			}
			continue
		}
		out[canon] = trunc(v, 1024)
	}
	return out
}

func secretHeader(k string) bool {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "authorization", "cookie", "set-cookie", "proxy-authorization", "x-api-key", "x-auth-token", "x-client-key":
		return true
	}
	return false
}

func canonicalHeader(k string) string {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "user-agent":
		return "User-Agent"
	case "if-none-match":
		return "If-None-Match"
	case "range":
		return "Range"
	case "x-machine-id":
		return "X-Machine-Id"
	case "x-client-core-version":
		return "X-Client-Core-Version"
	case "x-client-key":
		return "X-Client-Key"
	case "etag":
		return "ETag"
	case "cache-control":
		return "Cache-Control"
	case "content-length":
		return "Content-Length"
	case "content-range":
		return "Content-Range"
	}
	return k
}

func sanitizePath(p string) string {
	if p == "" {
		return ""
	}
	if u, err := url.Parse(p); err == nil && u.Path != "" {
		q := u.Query()
		for k := range q {
			if secretQueryParam(k) {
				q.Set(k, "present")
			}
		}
		u.RawQuery = q.Encode()
		return trunc(u.RequestURI(), 1024)
	}
	return trunc(p, 1024)
}

func secretQueryParam(k string) bool {
	lk := strings.ToLower(strings.TrimSpace(k))
	return strings.Contains(lk, "token") || strings.Contains(lk, "key") || strings.Contains(lk, "secret") || strings.Contains(lk, "password")
}

func mustJSON(v map[string]string) string {
	if len(v) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func parseHeaderJSON(s string) map[string]string {
	out := map[string]string{}
	if s == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}
