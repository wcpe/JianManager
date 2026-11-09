package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ClientDistTrackingService 客户端分发拉取/下载追踪。
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
	// ErrCode 语义错误码；成功事件留空。
	ErrCode string
	// ErrReason 可读错误原因。
	ErrReason  string
	DurationMs int64
	// 日志详情字段：保存排障所需的请求/响应详情；凭证类内容不落库。
	Method          string
	Path            string
	RequestBody     string
	ResponseBody    string
	RequestHeaders  map[string]string
	ResponseHeaders map[string]string
}

// ClientDistEventDetail 单条分发请求详情。
type ClientDistEventDetail struct {
	model.ClientDistEvent
	PlayerName      string            `json:"playerName"`
	CoreVersion     string            `json:"coreVersion"`
	RequestBody     string            `json:"requestBody"`
	ResponseBody    string            `json:"responseBody"`
	RequestHeaders  map[string]string `json:"requestHeaders"`
	ResponseHeaders map[string]string `json:"responseHeaders"`
}

// ClientDistEventSearchFilter 分页检索条件。
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

// ClientDistEventView 分发事件与运行态诊断字段（运行态字段为尽力关联）。
type ClientDistEventView struct {
	model.ClientDistEvent
	PlayerName  string `json:"playerName"`
	CoreVersion string `json:"coreVersion"`
}

// ClientDistEventPage 分页检索响应（357）。
type ClientDistEventPage struct {
	Items    []ClientDistEventView `json:"items"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"pageSize"`
	Total    int64                 `json:"total"`
}

// ClientDistErrorSummaryQuery 错误摘要查询条件。
type ClientDistErrorSummaryQuery struct {
	ChannelID   string
	From        time.Time
	To          time.Time
	TopN        int
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

// ClientDistRealtimeQuery 近实时查询参数。
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

// RecordSafe 记录不应阻断玩家请求的分发事件；失败时保留告警供运维排查。
func (s *ClientDistTrackingService) RecordSafe(e ClientDistEventInput) {
	if err := s.Record(e); err != nil {
		slog.Warn("记录客户端分发事件失败", "kind", e.Kind, "channelId", e.ChannelID, "status", e.Status, "error", err)
	}
}

// Cleanup 删除早于保留期的明细行（聚合长留）；返回删除行数。
func (s *ClientDistTrackingService) Cleanup() (int64, error) {
	cutoff := time.Now().Add(-time.Duration(s.retentionDays) * 24 * time.Hour)
	res := s.db.Where("created_at < ?", cutoff).Delete(&model.ClientDistEvent{})
	return res.RowsAffected, res.Error
}

// Start 启动后台滚动清理循环（数据量治理，仿）。
func (s *ClientDistTrackingService) Start() {
	go func() {
		t := time.NewTicker(s.cleanupEvery)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				if _, err := s.Cleanup(); err != nil {
					slog.Warn("清理客户端分发事件失败", "error", err)
				}
			}
		}
	}()
}

// Stop 停止后台清理循环。
func (s *ClientDistTrackingService) Stop() { close(s.stop) }

// ClientDistEventFilter 明细检索过滤条件（检索增 Outcome/ErrCode）。空字段不约束。
type ClientDistEventFilter struct {
	ChannelID string
	MachineID string
	IP        string
	Kind      string
	// Outcome 成功/失败维度："success"（0<status<400，含 200/206/304）| "failure"（status>=400）| ""（不约束）。
	Outcome string
	// ErrCode 语义错误码精确筛。
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

// SearchEvents 分页检索明细，支持运行态维度筛选。
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

// ErrorSummary 聚合错误码 TopN 与最近失败样例。
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

// GetEventDetail 返回单条明细与脱敏 header。
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

// Realtime 返回近实时分发请求健康度。
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
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("序列化客户端请求头失败", "error", err)
		return "{}"
	}
	return string(b)
}

func parseHeaderJSON(s string) map[string]string {
	out := map[string]string{}
	if s == "" {
		return out
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		slog.Warn("解析客户端请求头失败，已忽略损坏数据", "error", err)
	}
	return out
}

// ── 机器级更新清单与钻取 ────────────────────────────────────────
//
// 【为什么在 CP 侧聚合而不是前端拉明细自己算】
// 前端的机器清单要按「机器」去重后排序（更新次数 / 最近更新 / 版本滞后）再分页。
// 明细表是短保留的（默认 14 天）且按事件行存储，一次 7 天窗、上千台机器的明细可达数十万行；
// 拉到浏览器再去重排序既不可分页（total 要在服务端算），也会把「明细墙」推给前端。
//
// 【为什么排序与分页必须在 SQL 里做】
// 前端契约里 `total` 决定分页器，`page/pageSize` 决定当前页；
// 若在内存里聚合全量再切片，翻到第 2 页就要重算全部机器——与本仓既有
// 「千级实例走服务端搜索」的口径一致，不放任前端全量列举。

// ClientMachineListQuery 机器清单查询条件。
// From/To 必填（零值视为非法，由 service 返回错误而不是默认全量）。
type ClientMachineListQuery struct {
	ChannelID string
	From      time.Time
	To        time.Time
	Sort      string // updates | lastUpdateAt | versionLag
	Order     string // asc | desc
	Page      int
	PageSize  int
}

// ClientMachineSummary 单台机器在窗口内的更新聚合。
//
// 字段与前端契约 `client-dist-machines-contracts.ts` 的 ClientMachineSummary 逐字对齐。
type ClientMachineSummary struct {
	MachineID      string `json:"machineId"`
	ChannelID      string `json:"channelId"`
	Updates        int64  `json:"updates"`
	LastUpdateAt   string `json:"lastUpdateAt"`
	LatestVersion  int    `json:"latestVersion"`
	CurrentVersion int    `json:"currentVersion"`
	VersionLag     int    `json:"versionLag"`
	DownloadBytes  int64  `json:"downloadBytes"`
	// PlayerName / InstallID / IP 来自另外两张表（运行态快照与遥测明细），非明细表聚合得出。
	// 取不到时留空串（前端按「未上报」渲染），**不以占位串冒充**。
	PlayerName string `json:"playerName"`
	InstallID  string `json:"installId"`
	IP         string `json:"ip"`
}

// ClientMachineListResult 机器清单分页结果。
type ClientMachineListResult struct {
	ChannelID string                 `json:"channelId"`
	From      string                 `json:"from"`
	To        string                 `json:"to"`
	Exact     bool                   `json:"exact"`
	Total     int64                  `json:"total"`
	Page      int                    `json:"page"`
	PageSize  int                    `json:"pageSize"`
	Items     []ClientMachineSummary `json:"items"`
}

// ClientMachineEvent 单机器时间线上的一个事件。
type ClientMachineEvent struct {
	Ts         string `json:"ts"`
	Version    int    `json:"version"`
	Result     string `json:"result"`
	Bytes      int64  `json:"bytes"`
	DurationMs int64  `json:"durationMs"`
}

// ClientMachineEventsResult 单机器时间线。
type ClientMachineEventsResult struct {
	MachineID string               `json:"machineId"`
	ChannelID string               `json:"channelId"`
	From      string               `json:"from"`
	To        string               `json:"to"`
	Items     []ClientMachineEvent `json:"items"`
}

// machineRow SQL 聚合的中间行（只用于取值，不对外）。
type machineRow struct {
	MachineID string
	ChannelID string
	Updates   int64
	// LastUpdateAt 用字符串接：`MAX(created_at)` 是**聚合结果**，数据库不会给它带上
	// 列的时区元信息，SQLite 下驱动返回的是 string，直接 Scan 到 time.Time 会报
	// `unsupported Scan, storing driver.Value type string into type *time.Time`。
	// 解析在后处理里做（兼容几种常见时间格式）。
	LastUpdateAt   string
	LatestVersion  int
	CurrentVersion int
	DownloadBytes  int64
}

// 机器清单的排序白名单。用白名单而不是拼接字符串：排序字段来自 query，
// 直接拼进 ORDER BY 就是注入面（本仓既有的分页实现同样按白名单映射）。
var clientMachineSortColumns = map[string]string{
	"updates":      "updates",
	"lastUpdateAt": "last_update_at",
	"versionLag":   "version_lag",
}

// machineEventResultOf 把 HTTP status + err_code 归到前端契约的四个取值。
//
// 归并与契约 `ClientMachineEvent.result` 的枚举一致：success / fail-static / rollback / error。
// 判序要点：**先看 err_code 再看 status**——rollback 与 fail-static 都是 200/206 之外的主流形态，
// 只按 status 判会把它们统统压成 error，运营就看不出「是回滚了还是挂了」。
func machineEventResultOf(status int, errCode string) string {
	switch strings.ToLower(strings.TrimSpace(errCode)) {
	case "rollback", "rollback_failed", "rolled_back":
		return "rollback"
	case "static_mismatch", "static_fail", "static_verify_failed":
		return "fail-static"
	}
	if status > 0 && status < 400 {
		return "success"
	}
	return "error"
}

// QueryMachines 按机器维度聚合窗口内的更新事件（服务端排序 + 分页）。
//
// Exact 的含义与前端契约一致：窗口起点落在明细保留窗内时为 true（聚合精确），
// 否则为 false（明细已被清理，只能给出近似值）。这里不谎报 true——
// 运营据此判断「这台机器的更新次数是不是真的只有这么点」。
func (s *ClientDistTrackingService) QueryMachines(q ClientMachineListQuery) (*ClientMachineListResult, error) {
	if q.From.IsZero() || q.To.IsZero() || !q.To.After(q.From) {
		return nil, fmt.Errorf("机器清单时间窗非法")
	}
	page, size := normalizePage(q.Page, q.PageSize)

	col, ok := clientMachineSortColumns[q.Sort]
	if !ok {
		col = "updates"
	}
	dir := "DESC"
	if strings.EqualFold(q.Order, "asc") {
		dir = "ASC"
	}

	base := s.db.Model(&model.ClientDistEvent{}).
		Where("created_at >= ? AND created_at <= ?", q.From, q.To).
		Where("machine_id <> ''")
	if q.ChannelID != "" {
		base = base.Where("channel_id = ?", q.ChannelID)
	}

	// latest_version 必须取**全局最新**（该频道/窗口内所有机器见过的最大 version），
	// 而不是`MAX(version)` 在对单台机器的事件里取。
	//
	// 【为何这么改】在单台的分组里取 MAX，得到的是「这台机器自己用过的最高版本」；
	// 一台从来只拉过 v4 的机器，按那个口径 lag = 4-4 = 0，而实际上它已经落后好几版。
	// 版本滞后的语义前提就是「跟平台最新比」，所以基准必须来自全局。
	// 仍然只碰明细表（spec §3「不改后端 schema」）。
	// 【为何必须按频道过滤】latest_version 的语义是「这个频道当前的最新客户端版本」。
	// 外部查询已经按 channel_id 圈定了「看哪个频道」，但子查询是**独立 SQL 作用域**，
	// 不带外部 WHERE。早期版本就漏了这一段：按频道 A 看时，子查询把频道 B 的
	// 更高版本号也算成了 A 的最新版，于是 A 频道里每台机器都显得「落后好几版」。
	// 版本号跨频道本来就没有可比性（各频道各自发版）。
	//
	// 用 SQL 条件分支而不是无条件加参数：未指定频道时语义就是「全局最新」，
	// 硬拼一个 channel_id = '' 会把基准永远算成 0。
	latestVersionSub := "COALESCE((SELECT MAX(e3.version) FROM client_dist_events e3 " +
		"WHERE e3.created_at >= ? AND e3.created_at <= ? AND e3.version > 0"
	var latestArgs []any
	// 频道过滤条件单独拼好，SELECT 与 ORDER BY 共用同一份——
	// 两处一旦漂移，排序基准与展示值就成了两个频道的事。
	channelCond := ""
	if q.ChannelID != "" {
		channelCond = " AND e3.channel_id = ?"
		latestArgs = append(latestArgs, q.ChannelID)
	}
	latestVersionSub += channelCond + "), 0)"

	selectSQL := "machine_id, MAX(channel_id) AS channel_id, COUNT(*) AS updates, " +
		"MAX(created_at) AS last_update_at, " +
		latestVersionSub + " AS latest_version, " +
		"COALESCE(SUM(bytes), 0) AS download_bytes"

	// 绑定参数必须与 SQL 里的占位符出现顺序严格一致：
	// [latest 的时间窗] → [可选的 channel_id] → [current 的时间窗]。
	selectArgs := []any{q.From, q.To}
	selectArgs = append(selectArgs, latestArgs...)
	selectArgs = append(selectArgs, q.From, q.To)

	// current_version：这台机器实际装的版本。
	//
	// 【必须限定 kind='manifest' 且 version>0】明细表里并非所有事件都带版本号：
	// artifact（制品下载）与 core（updater 本体）落库时 Version 恒为 0（跨版本共享）。
	// updater 的正常时序是「先拉 manifest(vN) → 再下载制品(v0)×N」，
	// 所以「最近一条事件」通常就是 artifact。
	// 不看 kind 直接取最近一条，会把一台已经升到最新版的机器算成 v0：
	// latest 9 - current 0 = 滞后 9，它反而排到了「最需更新」的最前面。
	// 早期用例没抓住是因为它们只种了 manifest 事件。
	// 子查询为独立作用域，e2 带 kind 条件不会影响外层分组。
	selectSQL += ", COALESCE((SELECT e2.version FROM client_dist_events e2 " +
		"WHERE e2.machine_id = client_dist_events.machine_id " +
		"AND e2.created_at >= ? AND e2.created_at <= ? " +
		"AND e2.kind = 'manifest' AND e2.version > 0 " +
		"ORDER BY e2.created_at DESC LIMIT 1), 0) AS current_version"

	countStmt := base.Session(&gorm.Session{}).Select("COUNT(DISTINCT machine_id)")
	var total int64
	if err := countStmt.Scan(&total).Error; err != nil {
		return nil, fmt.Errorf("统计机器数失败: %w", err)
	}

	var rows []machineRow
	agg := base.Session(&gorm.Session{}).
		Select(selectSQL, selectArgs...).
		Group("machine_id")

	// 排序键：version_lag 是派生值（latest_version - current_version），
	// 它**不是**聚合结果里的真实列；白名单里的真实列（updates / last_update_at）直接按列名排序。
	//
	// 【为何必须绑参而不是拼字面量】早期版本在 ORDER BY 里用 Go 格式化的字符串字面量
	// 重建了同一个时间窗，带来两个真问题：
	//   ① 时区：字面量强制 UTC 且不带偏移，而 SELECT 侧走的是 time.Time 绑定
	//      （驱动按落库文本的形态序列化，本地时区 +08:00）。两个基准一旦不等，
	//      排序用的就是「另一个窗口」的 lag，与展示值矛盾；
	//   ② 精度：字面量砍到整秒，而上界是 `<=`，于是落在同一秒内的行被文本比较判为
	//      大于窗口上界而排除——闭区间被变相改成开区间。
	//
	// 【为何必须用 clause.OrderBy{Expression} + 单层括号】
	// 实测三种写法会让排序**静默消失或直接报语法错**：
	//   - `Order(clause.Expr{...})`：Expr 不是 OrderBy 类型，被整个忽略（SQL 里没有 ORDER BY）；
	//   - 先 Order(OrderBy{Expression}) 再 Order("machine_id ASC")：
	//     MergeClause 只拷贝 Columns，Expression 在第二次合并时丢失；
	//   - 把整个表达式包成**双括号**再跟 DESC：SQLite 报 `near "DESC": syntax error`
	//     （用裸 db.Raw 同样能复现，所以这是 SQLite 解析器对括号表达式的限制，不是 GORM 的问题）。
	//     **单层**括号、或不包外层括号都正常。
	// 所以：主排序键与次序键合成一条 Expression，外层只留一层括号，只调一次 Order。
	// 方向串 dir 已被二值化成 ASC/DESC（不来自用户拼接）。
	if col == "version_lag" {
		lagSQL := "(COALESCE((SELECT MAX(e3.version) FROM client_dist_events e3 " +
			"WHERE e3.created_at >= ? AND e3.created_at <= ? AND e3.version > 0" + channelCond + "), 0) " +
			"- COALESCE((SELECT e2.version FROM client_dist_events e2 " +
			"WHERE e2.machine_id = client_dist_events.machine_id " +
			"AND e2.created_at >= ? AND e2.created_at <= ? " +
			"AND e2.kind = 'manifest' AND e2.version > 0 " +
			"ORDER BY e2.created_at DESC LIMIT 1), 0)) " + dir + ", machine_id ASC"
		agg = agg.Order(clause.OrderBy{Expression: clause.Expr{SQL: lagSQL, Vars: selectArgs}})
	} else {
		// 次序键（machine_id ASC）与主键同批：同排序值下若无唯一键，翻页会重复或漏行。
		agg = agg.Order(clause.OrderBy{Columns: []clause.OrderByColumn{
			{Column: clause.Column{Name: col}, Desc: dir == "DESC"},
			{Column: clause.Column{Name: "machine_id"}},
		}})
	}
	agg = agg.Offset((page - 1) * size).Limit(size)
	if err := agg.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("聚合机器清单失败: %w", err)
	}

	items := make([]ClientMachineSummary, 0, len(rows))
	identity := s.machineIdentityOf(rows)
	for _, r := range rows {
		lag := r.LatestVersion - r.CurrentVersion
		if lag < 0 {
			lag = 0
		}
		id := identity[r.MachineID]
		items = append(items, ClientMachineSummary{
			MachineID:      r.MachineID,
			ChannelID:      r.ChannelID,
			Updates:        r.Updates,
			LastUpdateAt:   formatAggregatedTime(r.LastUpdateAt),
			LatestVersion:  r.LatestVersion,
			CurrentVersion: r.CurrentVersion,
			VersionLag:     lag,
			DownloadBytes:  r.DownloadBytes,
			PlayerName:     id.playerName,
			InstallID:      id.installID,
			IP:             id.ip,
		})
	}

	return &ClientMachineListResult{
		ChannelID: q.ChannelID,
		From:      q.From.UTC().Format(time.RFC3339),
		To:        q.To.UTC().Format(time.RFC3339),
		Exact:     s.windowExact(q.From, q.To),
		Total:     total,
		Page:      page,
		PageSize:  size,
		Items:     items,
	}, nil
}

// QueryMachineEvents 取单台机器在窗口内的更新时间线。
//
// 直接复用明细检索 + 契约映射，不另建一套查询：
// 时间线的每一条本来就对应一条明细事件，重复实现只会让两处口径漂移。
func (s *ClientDistTrackingService) QueryMachineEvents(machineID, channelID string, from, to time.Time) (*ClientMachineEventsResult, error) {
	if strings.TrimSpace(machineID) == "" {
		return nil, fmt.Errorf("machineId 不能为空")
	}
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return nil, fmt.Errorf("机器时间线时间窗非法")
	}
	events, err := s.QueryEvents(ClientDistEventFilter{
		ChannelID: channelID,
		MachineID: machineID,
		Since:     &from,
		Until:     &to,
		// 时间线的用途是「这台机器最近发生了什么」，一次给足一屏即可；
		// QueryEvents 自带 1000 条硬上限，避免恶意 machineId 把整段历史拉出来。
		Limit: 500,
	})
	if err != nil {
		return nil, err
	}
	items := make([]ClientMachineEvent, 0, len(events))
	for _, e := range events {
		items = append(items, ClientMachineEvent{
			Ts:         e.CreatedAt.UTC().Format(time.RFC3339),
			Version:    e.Version,
			Result:     machineEventResultOf(e.Status, e.ErrCode),
			Bytes:      e.Bytes,
			DurationMs: e.DurationMs,
		})
	}
	return &ClientMachineEventsResult{
		MachineID: machineID,
		ChannelID: channelID,
		From:      from.UTC().Format(time.RFC3339),
		To:        to.UTC().Format(time.RFC3339),
		Items:     items,
	}, nil
}

// formatAggregatedTime 把数据库返回的聚合时间值归一成 RFC3339。
//
// 【为何不能直接 Scan 到 time.Time】见 machineRow.LastUpdateAt 的注释。
// 这里兼容 GORM/SQLite 的几种输出形态（含带纳秒、带时区后缀的变体）；
// 全部解析失败时返回空串——前端对空串的渲染是「—」，
// 比抛一个 500 让整页无法加载要好（该字段不影响排序主路径）。
func formatAggregatedTime(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999-07:00:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

// machineIdentity 三张表拼出来的机器展示信息。
type machineIdentity struct {
	playerName string
	installID  string
	ip         string
}

// machineIdentityOf 批量取机器展示信息（玩家名/IP/安装 ID）。
//
// 【为什么不在主查询里 JOIN】主查询是带 GROUP BY 的聚合 + 相关子查询，再 JOIN 两张表
// 会把分组列逐个引入 SELECT/GROUP BY，改一处就得同步改排序表达式。
// 这里只对**当前页**那一小批 machineId（≤ pageSize）做两次 IN 查询，
// 两次都走 machine_id 索引，代价远低于把主查询变复杂。
//
// 【为什么允许取不到】运行态快照只存最近一次心跳、遥测是短保留且可 opt-out：
// 老客户端/已清理的机器就是没有这些值。界面应显示「未上报」而不是空串或 0。
// 查询失败也不阻断清单——它只是展示增强，不是统计口径（失败仅返回空映射）。
func (s *ClientDistTrackingService) machineIdentityOf(rows []machineRow) map[string]machineIdentity {
	out := make(map[string]machineIdentity, len(rows))
	if len(rows) == 0 {
		return out
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.MachineID != "" {
			ids = append(ids, r.MachineID)
		}
	}
	if len(ids) == 0 {
		return out
	}

	var states []model.ClientRuntimeState
	if err := s.db.Model(&model.ClientRuntimeState{}).
		Where("machine_id IN ?", ids).
		Order("last_heartbeat_at DESC").
		Find(&states).Error; err != nil {
		slog.Warn("查询机器运行态失败，清单仍返回", "error", err)
	} else {
		for _, st := range states {
			// ORDER BY 保证同一个 machineId 多条时先到的是最新心跳；后者不覆盖。
			if _, ok := out[st.MachineID]; ok {
				continue
			}
			out[st.MachineID] = machineIdentity{playerName: st.PlayerName, ip: st.IP}
		}
	}

	var teles []model.ClientTelemetry
	if err := s.db.Model(&model.ClientTelemetry{}).
		Select("machine_id, install_id").
		Where("machine_id IN ? AND install_id <> ''", ids).
		Order("created_at DESC").
		Find(&teles).Error; err != nil {
		slog.Warn("查询机器遥测失败，安装 ID 缺省", "error", err)
	} else {
		for _, t := range teles {
			if _, ok := out[t.MachineID]; ok && out[t.MachineID].installID != "" {
				continue
			}
			cur := out[t.MachineID]
			cur.installID = t.InstallID
			out[t.MachineID] = cur
		}
	}
	return out
}

// windowExact 判断查询窗口是否完整落在明细保留期内（决定 exact 标记）。
//
// 只要求**起点**在保留窗内：终点必然 <= now，故起点在窗内即整段都在窗内。
func (s *ClientDistTrackingService) windowExact(from, to time.Time) bool {
	_ = to
	cutoff := time.Now().Add(-time.Duration(s.retentionDays) * 24 * time.Hour)
	return !from.Before(cutoff)
}
