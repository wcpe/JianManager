package service

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

const (
	ClientDistExportStatsSummary = "stats-summary"
	ClientDistExportEvents       = "dist-events"
	ClientDistExportSecurityLogs = "security-logs"
	clientDistExportMaxRows      = 10000
	clientDistExportBatchSize    = 200
)

var clientDistExportKinds = map[string]bool{
	ClientDistExportStatsSummary: true,
	ClientDistExportEvents:       true,
	ClientDistExportSecurityLogs: true,
}

// ClientDistExportFilter 是 CSV 导出的冻结筛选集合。
type ClientDistExportFilter struct {
	Kind       string
	EventKind  string
	ChannelID  string
	Range      string
	ErrCode    string
	Outcome    string
	IP         string
	MachineID  string
	PlayerName string
	LogType    string
	Version    *int
	From, To   time.Time
}

// ClientDistExportService 以分页查询和流式 CSV 写入导出分发数据。
type ClientDistExportService struct {
	db            *gorm.DB
	observability *ClientDistObservabilityService
	maxRows       int
}

// NewClientDistExportService 创建 CSV 导出服务。
func NewClientDistExportService(db *gorm.DB, observability *ClientDistObservabilityService) *ClientDistExportService {
	return &ClientDistExportService{db: db, observability: observability, maxRows: clientDistExportMaxRows}
}

// SetMaxRowsForTest 仅用于缩小截断边界，避免测试创建一万行数据。
func (s *ClientDistExportService) SetMaxRowsForTest(maxRows int) {
	if maxRows > 0 {
		s.maxRows = maxRows
	}
}

// ValidClientDistExportKind 判断 kind 是否属于冻结枚举。
func ValidClientDistExportKind(kind string) bool { return clientDistExportKinds[kind] }

// Truncated 在写响应头前判断数据行是否超过单次上限。
func (s *ClientDistExportService) Truncated(f ClientDistExportFilter) (bool, error) {
	var count int64
	var err error
	switch f.Kind {
	case ClientDistExportStatsSummary:
		return false, nil
	case ClientDistExportEvents:
		err = s.eventQuery(f).Count(&count).Error
	case ClientDistExportSecurityLogs:
		count, err = s.countSecurityLogs(f)
	default:
		return false, fmt.Errorf("非法导出类型")
	}
	return count > int64(s.maxRows), err
}

// WriteCSV 写入 UTF-8 BOM、camelCase 表头和数据行。
func (s *ClientDistExportService) WriteCSV(w io.Writer, f ClientDistExportFilter, truncated bool) error {
	if _, err := io.WriteString(w, "\ufeff"); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	var err error
	switch f.Kind {
	case ClientDistExportStatsSummary:
		err = s.writeStats(cw, f)
	case ClientDistExportEvents:
		err = s.writeEvents(cw, f)
	case ClientDistExportSecurityLogs:
		err = s.writeSecurityLogs(cw, f)
	default:
		err = fmt.Errorf("非法导出类型")
	}
	if err == nil && truncated {
		err = cw.Write([]string{"truncated=true"})
	}
	cw.Flush()
	if err != nil {
		return err
	}
	return cw.Error()
}

func (s *ClientDistExportService) writeStats(cw *csv.Writer, f ClientDistExportFilter) error {
	header := []string{"channelId", "from", "to", "manifestPulls", "artifactPulls", "downloadBytes", "updateTotal", "updateSuccess", "updateFailStatic", "updateRolledBack", "updateError", "successRate", "failStaticRate", "rollbackRate", "activeMachines", "activeMachinesExact"}
	if err := cw.Write(header); err != nil {
		return err
	}
	result, err := s.observability.Query(ObservabilityQuery{ChannelID: f.ChannelID, From: f.From, To: f.To})
	if err != nil {
		return err
	}
	x := result.Summary
	return cw.Write([]string{f.ChannelID, formatExportTime(f.From), formatExportTime(f.To), i64(x.ManifestPulls), i64(x.ArtifactPulls), i64(x.DownloadBytes), i64(x.UpdateTotal), i64(x.UpdateSuccess), i64(x.UpdateFailStatic), i64(x.UpdateRolledBack), i64(x.UpdateError), rate(x.SuccessRate), rate(x.FailStaticRate), rate(x.RollbackRate), i64(x.ActiveMachines), strconv.FormatBool(x.ActiveMachinesExact)})
}

func (s *ClientDistExportService) writeEvents(cw *csv.Writer, f ClientDistExportFilter) error {
	header := []string{"id", "channelId", "machineId", "playerName", "coreVersion", "ip", "kind", "version", "artifactSha", "bytes", "status", "outcome", "errCode", "errReason", "method", "path", "etag", "durationMs", "createdAt"}
	if err := cw.Write(header); err != nil {
		return err
	}
	written := 0
	for offset := 0; written < s.maxRows; offset += clientDistExportBatchSize {
		var rows []model.ClientDistEvent
		limit := min(clientDistExportBatchSize, s.maxRows-written)
		if err := s.eventQuery(f).Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		views, err := s.eventViews(rows)
		if err != nil {
			return err
		}
		for i, row := range rows {
			if err := cw.Write(eventCSVRow(row, views[i])); err != nil {
				return err
			}
			written++
		}
	}
	return nil
}

func (s *ClientDistExportService) eventQuery(f ClientDistExportFilter) *gorm.DB {
	q := s.db.Model(&model.ClientDistEvent{}).Where("created_at >= ? AND created_at < ?", f.From, f.To)
	q = applyExportCommon(q, f, "channel_id", "machine_id", "ip")
	if f.EventKind != "" {
		q = q.Where("kind = ?", f.EventKind)
	}
	if f.ErrCode != "" {
		q = q.Where("err_code = ?", f.ErrCode)
	}
	if f.Version != nil {
		q = q.Where("version = ?", *f.Version)
	}
	switch f.Outcome {
	case "success":
		q = q.Where("status > 0 AND status < 400")
	case "failure":
		q = q.Where("status >= 400")
	}
	return q
}

type exportEventView struct{ PlayerName, CoreVersion string }

func (s *ClientDistExportService) eventViews(rows []model.ClientDistEvent) ([]exportEventView, error) {
	machines := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.MachineID != "" {
			machines = append(machines, row.MachineID)
		}
	}
	var states []model.ClientRuntimeState
	if len(machines) > 0 {
		if err := s.db.Where("machine_id IN ?", machines).Find(&states).Error; err != nil {
			return nil, err
		}
	}
	byID := make(map[string]model.ClientRuntimeState, len(states))
	for _, state := range states {
		byID[state.ChannelID+"\x00"+state.MachineID] = state
	}
	out := make([]exportEventView, len(rows))
	for i, row := range rows {
		state := byID[row.ChannelID+"\x00"+row.MachineID]
		out[i] = exportEventView{PlayerName: state.PlayerName, CoreVersion: state.CoreVersion}
	}
	return out, nil
}

func eventCSVRow(row model.ClientDistEvent, view exportEventView) []string {
	outcome := "success"
	if row.Status >= 400 {
		outcome = "failure"
	}
	path := strings.SplitN(row.Path, "?", 2)[0]
	return []string{strconv.FormatUint(uint64(row.ID), 10), row.ChannelID, maskExportID(row.MachineID), maskExportPlayer(view.PlayerName), view.CoreVersion, row.IP, row.Kind, strconv.Itoa(row.Version), row.ArtifactSHA, i64(row.Bytes), strconv.Itoa(row.Status), outcome, row.ErrCode, row.ErrReason, row.Method, path, row.ETag, i64(row.DurationMs), formatExportTime(row.CreatedAt)}
}

type securityExportItem struct {
	ID, Type, Title, ChannelID, MachineID, InstallID, PlayerName, IP, Status, ErrCode string
	CreatedAt                                                                         time.Time
	Detail                                                                            any
}

var securityExportSources = []string{"hello", "risk", "action", "request", "runtime", "telemetry"}

func (s *ClientDistExportService) countSecurityLogs(f ClientDistExportFilter) (int64, error) {
	var total int64
	for _, source := range securityExportSources {
		if f.LogType != "" && f.LogType != "all" && f.LogType != source {
			continue
		}
		count, err := s.countSecuritySource(source, f)
		if err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

func (s *ClientDistExportService) countSecuritySource(source string, f ClientDistExportFilter) (int64, error) {
	modelValue, timeCol := securitySourceModel(source)
	if modelValue == nil {
		return 0, nil
	}
	q := s.db.Model(modelValue).Where(timeCol+" >= ? AND "+timeCol+" < ?", f.From, f.To)
	q = applySecurityExportFilter(q, source, f)
	var count int64
	return count, q.Count(&count).Error
}

func (s *ClientDistExportService) writeSecurityLogs(cw *csv.Writer, f ClientDistExportFilter) error {
	header := []string{"id", "type", "title", "channelId", "machineId", "installId", "playerName", "ip", "status", "errCode", "createdAt", "detail"}
	if err := cw.Write(header); err != nil {
		return err
	}
	written := 0
	for _, source := range securityExportSources {
		if f.LogType != "" && f.LogType != "all" && f.LogType != source {
			continue
		}
		for offset := 0; written < s.maxRows; offset += clientDistExportBatchSize {
			rows, err := s.securitySourceRows(source, f, offset, min(clientDistExportBatchSize, s.maxRows-written))
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				break
			}
			for _, row := range rows {
				if err := cw.Write(securityCSVRow(row)); err != nil {
					return err
				}
				written++
			}
		}
		if written >= s.maxRows {
			break
		}
	}
	return nil
}

func (s *ClientDistExportService) securitySourceRows(source string, f ClientDistExportFilter, offset, limit int) ([]securityExportItem, error) {
	switch source {
	case "hello":
		return s.securityHelloRows(f, offset, limit)
	case "risk":
		return s.securityRiskRows(f, offset, limit)
	case "action":
		return s.securityActionRows(f, offset, limit)
	case "request":
		return s.securityRequestRows(f, offset, limit)
	case "runtime":
		return s.securityRuntimeRows(f, offset, limit)
	case "telemetry":
		return s.securityTelemetryRows(f, offset, limit)
	default:
		return nil, nil
	}
}

func (s *ClientDistExportService) securityHelloRows(f ClientDistExportFilter, offset, limit int) ([]securityExportItem, error) {
	var rows []model.ClientSecurityHello
	q := applySecurityExportFilter(s.db.Where("created_at >= ? AND created_at < ?", f.From, f.To), "hello", f)
	if err := q.Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]securityExportItem, 0, len(rows))
	for _, r := range rows {
		status := "rejected"
		if r.Accepted {
			status = "accepted"
		}
		out = append(out, securityExportItem{ID: logID("hello", r.ID), Type: "hello", Title: "安全画像上报", ChannelID: r.ChannelID, MachineID: r.MachineID, InstallID: r.InstallID, PlayerName: r.PlayerName, IP: r.IP, Status: status, ErrCode: r.ErrCode, CreatedAt: r.CreatedAt, Detail: map[string]any{"keyPrefix": r.KeyPrefix, "userAgent": r.UserAgent, "payload": parseExportJSON(r.PayloadJSON)}})
	}
	return out, nil
}

func (s *ClientDistExportService) securityRiskRows(f ClientDistExportFilter, offset, limit int) ([]securityExportItem, error) {
	var rows []model.ClientSecurityRiskEvent
	q := applySecurityExportFilter(s.db.Where("created_at >= ? AND created_at < ?", f.From, f.To), "risk", f)
	if err := q.Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]securityExportItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, securityExportItem{ID: logID("risk", r.ID), Type: "risk", Title: r.RuleCode, ChannelID: r.ChannelID, MachineID: r.MachineID, InstallID: r.InstallID, PlayerName: r.PlayerName, IP: r.IP, Status: r.Severity, ErrCode: r.RuleCode, CreatedAt: r.CreatedAt, Detail: map[string]any{"subjectType": r.SubjectType, "subjectValue": maskExportSubject(r.SubjectType, r.SubjectValue), "keyPrefix": r.KeyPrefix, "reason": r.Reason, "detail": parseExportJSON(r.DetailJSON)}})
	}
	return out, nil
}

func (s *ClientDistExportService) securityActionRows(f ClientDistExportFilter, offset, limit int) ([]securityExportItem, error) {
	if f.MachineID != "" {
		return []securityExportItem{}, nil
	}
	var rows []model.ClientProtectionAction
	q := applySecurityExportFilter(s.db.Where("created_at >= ? AND created_at < ?", f.From, f.To), "action", f)
	if err := q.Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]securityExportItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, securityExportItem{ID: logID("action", r.ID), Type: "action", Title: r.Action, ChannelID: r.ChannelID, Status: r.Status, CreatedAt: r.CreatedAt, Detail: map[string]any{"targetType": r.TargetType, "targetValue": maskExportSubject(r.TargetType, r.TargetValue), "reason": r.Reason, "auto": r.Auto, "expiresAt": r.ExpiresAt, "createdBy": r.CreatedBy}})
	}
	return out, nil
}

func (s *ClientDistExportService) securityRequestRows(f ClientDistExportFilter, offset, limit int) ([]securityExportItem, error) {
	var rows []model.ClientDistEvent
	q := applySecurityExportFilter(s.db.Where("created_at >= ? AND created_at < ?", f.From, f.To), "request", f)
	if err := q.Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]securityExportItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, securityExportItem{ID: logID("request", r.ID), Type: "request", Title: r.Kind, ChannelID: r.ChannelID, MachineID: r.MachineID, IP: r.IP, Status: strconv.Itoa(r.Status), ErrCode: r.ErrCode, CreatedAt: r.CreatedAt, Detail: map[string]any{"version": r.Version, "artifactSha": r.ArtifactSHA, "bytes": r.Bytes, "method": r.Method, "path": strings.SplitN(r.Path, "?", 2)[0], "errReason": r.ErrReason, "durationMs": r.DurationMs, "etag": r.ETag}})
	}
	return out, nil
}

func (s *ClientDistExportService) securityRuntimeRows(f ClientDistExportFilter, offset, limit int) ([]securityExportItem, error) {
	var rows []model.ClientRuntimeState
	q := applySecurityExportFilter(s.db.Where("last_heartbeat_at >= ? AND last_heartbeat_at < ?", f.From, f.To), "runtime", f)
	if err := q.Order("last_heartbeat_at DESC, id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]securityExportItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, securityExportItem{ID: logID("runtime", r.ID), Type: "runtime", Title: "运行态心跳", ChannelID: r.ChannelID, MachineID: r.MachineID, PlayerName: r.PlayerName, IP: r.IP, Status: r.LastUpdateResult, CreatedAt: r.LastHeartbeatAt, Detail: map[string]any{"platform": r.Platform, "javaVersion": r.JavaVersion, "launcher": r.Launcher, "coreVersion": r.CoreVersion, "localVersion": r.LocalVersion, "firstSeenAt": r.FirstSeenAt, "lastUpdateAt": r.LastUpdateAt}})
	}
	return out, nil
}

func (s *ClientDistExportService) securityTelemetryRows(f ClientDistExportFilter, offset, limit int) ([]securityExportItem, error) {
	var rows []model.ClientTelemetry
	q := applySecurityExportFilter(s.db.Where("created_at >= ? AND created_at < ?", f.From, f.To), "telemetry", f)
	if err := q.Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]securityExportItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, securityExportItem{ID: logID("telemetry", r.ID), Type: "telemetry", Title: "更新遥测", ChannelID: r.ChannelID, MachineID: r.MachineID, PlayerName: r.PlayerName, IP: r.IP, Status: r.Result, ErrCode: r.Error, CreatedAt: r.CreatedAt, Detail: map[string]any{"fromVersion": r.FromVersion, "toVersion": r.ToVersion, "coreVersion": r.CoreVersion, "os": r.OS, "arch": r.Arch, "javaVersion": r.JavaVersion, "javaVendor": r.JavaVendor, "launcher": r.Launcher, "locale": r.Locale, "timezone": r.Timezone, "memoryTier": r.MemoryTier, "durationMs": r.DurationMs, "bootSuccess": r.BootSuccess}})
	}
	return out, nil
}

func securityCSVRow(row securityExportItem) []string {
	detail, _ := json.Marshal(maskExportValue(row.Detail, ""))
	return []string{row.ID, row.Type, row.Title, row.ChannelID, maskExportID(row.MachineID), maskExportID(row.InstallID), maskExportPlayer(row.PlayerName), row.IP, row.Status, row.ErrCode, formatExportTime(row.CreatedAt), string(detail)}
}

func securitySourceModel(source string) (any, string) {
	switch source {
	case "hello":
		return &model.ClientSecurityHello{}, "created_at"
	case "risk":
		return &model.ClientSecurityRiskEvent{}, "created_at"
	case "action":
		return &model.ClientProtectionAction{}, "created_at"
	case "request":
		return &model.ClientDistEvent{}, "created_at"
	case "runtime":
		return &model.ClientRuntimeState{}, "last_heartbeat_at"
	case "telemetry":
		return &model.ClientTelemetry{}, "created_at"
	default:
		return nil, ""
	}
}

func applySecurityExportFilter(q *gorm.DB, source string, f ClientDistExportFilter) *gorm.DB {
	if source == "action" {
		if f.MachineID != "" || f.PlayerName != "" {
			return q.Where("1 = 0")
		}
		if f.ChannelID != "" {
			q = q.Where("channel_id = ? OR target_value = ?", f.ChannelID, f.ChannelID)
		}
		if f.IP != "" {
			q = q.Where("target_type = ? AND target_value = ?", "ip", f.IP)
		}
		return q
	}
	q = applyExportCommon(q, f, "channel_id", "machine_id", "ip")
	if f.PlayerName != "" {
		switch source {
		case "hello", "risk", "runtime", "telemetry":
			q = q.Where("player_name = ?", f.PlayerName)
		default:
			q = q.Where("1 = 0")
		}
	}
	if f.ErrCode != "" {
		switch source {
		case "hello":
			q = q.Where("err_code = ?", f.ErrCode)
		case "risk":
			q = q.Where("rule_code = ?", f.ErrCode)
		case "request":
			q = q.Where("err_code = ?", f.ErrCode)
		case "telemetry":
			q = q.Where("error = ?", f.ErrCode)
		}
	}
	return q
}

func applyExportCommon(q *gorm.DB, f ClientDistExportFilter, channelCol, machineCol, ipCol string) *gorm.DB {
	if f.ChannelID != "" {
		q = q.Where(channelCol+" = ?", f.ChannelID)
	}
	if f.MachineID != "" {
		q = q.Where(machineCol+" = ?", f.MachineID)
	}
	if f.IP != "" {
		q = q.Where(ipCol+" = ?", f.IP)
	}
	return q
}

func maskExportSubject(subjectType, value string) string {
	switch strings.ToLower(subjectType) {
	case "machine", "machineid", "install", "installid", "client":
		return maskExportID(value)
	case "player", "playername":
		return maskExportPlayer(value)
	default:
		return value
	}
}

func maskExportPlayer(value string) string {
	runes := []rune(value)
	if len(runes) <= 16 {
		return value
	}
	return string(runes[:15]) + "…"
}

func maskExportID(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return ""
	}
	if len(runes) <= 10 {
		return "***"
	}
	return string(runes[:6]) + "…" + string(runes[len(runes)-4:])
}

func maskExportValue(value any, key string) any {
	lower := strings.ReplaceAll(strings.ToLower(key), "_", "")
	if lower == "playername" {
		return maskExportPlayer(fmt.Sprint(value))
	}
	if lower == "machineid" || lower == "installid" {
		return maskExportID(fmt.Sprint(value))
	}
	if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "authorization") || strings.Contains(lower, "clientkey") {
		return nil
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			if masked := maskExportValue(v, k); masked != nil {
				out[k] = masked
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, v := range typed {
			out = append(out, maskExportValue(v, ""))
		}
		return out
	default:
		return value
	}
}

func parseExportJSON(raw string) any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func formatExportTime(value time.Time) string { return value.UTC().Format(time.RFC3339) }
func i64(value int64) string                  { return strconv.FormatInt(value, 10) }
func rate(value float64) string               { return strconv.FormatFloat(value, 'f', 6, 64) }
