package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

const (
	ClientKeyStateNormal       = "normal"
	ClientKeyStateObserve      = "observe"
	ClientKeyStateThrottled    = "throttled"
	ClientKeyStateSuspended    = "suspended"
	ClientKeyStateRevoked      = "revoked"
	ClientChannelModeNormal    = "normal"
	ClientChannelModeProtected = "protected"
)

var (
	ErrClientKeySuspended         = errors.New("拉取密钥已暂停")
	ErrClientKeyThrottled         = errors.New("拉取密钥已限速")
	ErrIPTempBlocked              = errors.New("IP 已临时封禁")
	ErrChannelProtected           = errors.New("频道保护中")
	ErrArtifactNotAllowed         = errors.New("制品不属于密钥频道")
	ErrDownloadConcurrencyLimited = errors.New("下载并发受限")
	ErrBandwidthLimited           = errors.New("带宽配额受限")
	ErrRateLimited                = errors.New("请求过于频繁")
)

var validSecurityPlayerName = regexp.MustCompile(`^[A-Za-z0-9_]{3,16}$`)

type ClientDistSecurityService struct {
	db       *gorm.DB
	channel  *ClientChannelService
	version  *ClientVersionService
	mu       sync.Mutex
	buckets  map[string]*securityBucket
	active   map[string]int
	limits   SecurityLimits
	retrySec int
}

type SecurityLimits struct {
	RatePerMinute             int
	ArtifactGlobalConcurrent  int
	ArtifactIPConcurrent      int
	ArtifactKeyConcurrent     int
	ArtifactChannelConcurrent int
	HourlyBandwidthBytes      int64
}

type securityBucket struct {
	count int
	reset time.Time
	bytes int64
}

type ClientSecurityHelloInput struct {
	Channel         string `json:"channel"`
	PlayerName      string `json:"playerName"`
	MachineID       string `json:"machineId"`
	InstallID       string `json:"installId"`
	CoreVersion     string `json:"coreVersion"`
	WedgeVersion    string `json:"wedgeVersion"`
	ManifestVersion string `json:"manifestVersion"`
	OS              string `json:"os"`
	OSVersion       string `json:"osVersion"`
	Arch            string `json:"arch"`
	JavaVendor      string `json:"javaVendor"`
	JavaVersion     string `json:"javaVersion"`
	JavaArch        string `json:"javaArch"`
	Launcher        string `json:"launcher"`
	Locale          string `json:"locale"`
	Timezone        string `json:"timezone"`
	MemoryTier      string `json:"memoryTier"`
}

type SecurityKeyCheck struct {
	Key       *model.ClientPullKey
	ChannelID string
}
type DownloadLease struct {
	svc  *ClientDistSecurityService
	keys []string
}

func NewClientDistSecurityService(db *gorm.DB, channel *ClientChannelService, version *ClientVersionService) *ClientDistSecurityService {
	return &ClientDistSecurityService{db: db, channel: channel, version: version, buckets: map[string]*securityBucket{}, active: map[string]int{}, retrySec: 60, limits: SecurityLimits{RatePerMinute: 120, ArtifactGlobalConcurrent: 256, ArtifactIPConcurrent: 32, ArtifactKeyConcurrent: 16, ArtifactChannelConcurrent: 64}}
}

func (s *ClientDistSecurityService) SetLimitsForTest(l SecurityLimits) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limits = l
}
func (s *ClientDistSecurityService) RetryAfter() int {
	if s == nil || s.retrySec <= 0 {
		return 60
	}
	return s.retrySec
}

func (s *ClientDistSecurityService) VerifyChannelKey(channelID, plaintext string) (*SecurityKeyCheck, error) {
	key, err := s.channel.VerifyKey(channelID, plaintext)
	if err != nil {
		return nil, err
	}
	if err := s.checkKeyState(key); err != nil {
		return nil, err
	}
	return &SecurityKeyCheck{Key: key, ChannelID: key.ChannelID}, nil
}

func (s *ClientDistSecurityService) VerifyAnyKey(plaintext string) (*SecurityKeyCheck, error) {
	key, err := s.channel.VerifyAnyKey(plaintext)
	if err != nil {
		return nil, err
	}
	if err := s.checkKeyState(key); err != nil {
		return nil, err
	}
	return &SecurityKeyCheck{Key: key, ChannelID: key.ChannelID}, nil
}

func (s *ClientDistSecurityService) checkKeyState(key *model.ClientPullKey) error {
	switch normalizedKeyState(key.SecurityState) {
	case ClientKeyStateSuspended:
		return ErrClientKeySuspended
	case ClientKeyStateThrottled:
		return ErrClientKeyThrottled
	}
	return nil
}

func (s *ClientDistSecurityService) RecordHello(in ClientSecurityHelloInput, key *model.ClientPullKey, ip, userAgent string) error {
	now := time.Now()
	accepted := key != nil && in.Channel != "" && in.MachineID != "" && in.InstallID != ""
	errCode := ""
	if !accepted {
		errCode = "INVALID_REQUEST"
	}
	payload, _ := json.Marshal(in)
	hello := &model.ClientSecurityHello{ChannelID: in.Channel, MachineID: in.MachineID, InstallID: in.InstallID, PlayerName: in.PlayerName, Accepted: accepted, ErrCode: errCode, IP: ip, UserAgent: truncateSecurity(userAgent, 255), PayloadJSON: string(payload), CreatedAt: now}
	if key != nil {
		hello.KeyID = key.ID
		hello.KeyPrefix = key.KeyPrefix
	}
	if err := s.db.Create(hello).Error; err != nil {
		return fmt.Errorf("写入安全 hello 失败: %w", err)
	}
	if !accepted {
		return nil
	}
	profile := model.ClientSecurityProfile{ChannelID: in.Channel, MachineID: in.MachineID, InstallID: in.InstallID, PlayerName: in.PlayerName, PlayerNameNorm: strings.ToLower(in.PlayerName), KeyID: key.ID, KeyPrefix: key.KeyPrefix, FirstSeen: now, LastSeen: now, LastIP: ip, UserAgent: truncateSecurity(userAgent, 255), CoreVersion: in.CoreVersion, WedgeVersion: in.WedgeVersion, ManifestVersion: in.ManifestVersion, OS: in.OS, OSVersion: in.OSVersion, Arch: in.Arch, JavaVendor: in.JavaVendor, JavaVersion: in.JavaVersion, JavaArch: in.JavaArch, Launcher: in.Launcher, Locale: in.Locale, Timezone: in.Timezone, MemoryTier: in.MemoryTier, RiskLevel: "normal", ProtectionState: "normal"}
	updates := map[string]any{"player_name": in.PlayerName, "player_name_norm": strings.ToLower(in.PlayerName), "key_id": key.ID, "key_prefix": key.KeyPrefix, "last_seen": now, "last_ip": ip, "user_agent": truncateSecurity(userAgent, 255), "core_version": in.CoreVersion, "wedge_version": in.WedgeVersion, "manifest_version": in.ManifestVersion, "os": in.OS, "os_version": in.OSVersion, "arch": in.Arch, "java_vendor": in.JavaVendor, "java_version": in.JavaVersion, "java_arch": in.JavaArch, "launcher": in.Launcher, "locale": in.Locale, "timezone": in.Timezone, "memory_tier": in.MemoryTier}
	if err := s.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "channel_id"}, {Name: "machine_id"}, {Name: "install_id"}}, DoUpdates: clause.Assignments(updates)}).Create(&profile).Error; err != nil {
		return fmt.Errorf("写入安全画像失败: %w", err)
	}
	if !validSecurityPlayerName.MatchString(in.PlayerName) {
		return s.RecordRiskEvent("INVALID_PLAYER_NAME", in.Channel, in.MachineID, in.InstallID, in.PlayerName, ip, key, "low", nil)
	}
	return nil
}

func (s *ClientDistSecurityService) RecordRiskEvent(code, channelID, machineID, installID, playerName, ip string, key *model.ClientPullKey, severity string, detail any) error {
	raw, _ := json.Marshal(detail)
	if severity == "" {
		severity = "info"
	}
	ev := &model.ClientSecurityRiskEvent{SubjectType: "client", SubjectValue: installID, ChannelID: channelID, MachineID: machineID, InstallID: installID, PlayerName: playerName, IP: ip, RuleCode: code, Severity: severity, Reason: code, DetailJSON: string(raw), CreatedAt: time.Now()}
	if key != nil {
		ev.KeyID = key.ID
		ev.KeyPrefix = key.KeyPrefix
	}
	return s.db.Create(ev).Error
}

func (s *ClientDistSecurityService) BlockIP(ip, reason string, ttl time.Duration, createdBy uint) (*model.ClientProtectionAction, error) {
	if net.ParseIP(ip) == nil {
		return nil, fmt.Errorf("非法 IP")
	}
	expires := time.Now().Add(ttl)
	if ttl <= 0 {
		expires = time.Now().Add(time.Hour)
	}
	a := &model.ClientProtectionAction{TargetType: "ip", TargetValue: ip, Action: "temp_block", Status: "active", Reason: reason, Auto: createdBy == 0, ExpiresAt: &expires, CreatedBy: createdBy}
	return a, s.db.Create(a).Error
}

func (s *ClientDistSecurityService) ActiveIPBlock(ip string) (*model.ClientProtectionAction, bool) {
	var a model.ClientProtectionAction
	err := s.db.Where("target_type = ? AND target_value = ? AND action IN ? AND status = ? AND (expires_at IS NULL OR expires_at > ?)", "ip", ip, []string{"temp_block", "ip_temp_block"}, "active", time.Now()).Order("id DESC").First(&a).Error
	return &a, err == nil
}
func (s *ClientDistSecurityService) CancelAction(id uint) error {
	now := time.Now()
	return s.db.Model(&model.ClientProtectionAction{}).Where("id = ?", id).Updates(map[string]any{"status": "canceled", "canceled_at": &now}).Error
}
func (s *ClientDistSecurityService) ListActions() ([]model.ClientProtectionAction, error) {
	var a []model.ClientProtectionAction
	return a, s.db.Order("created_at DESC").Limit(200).Find(&a).Error
}

func (s *ClientDistSecurityService) SetKeyState(keyID uint, state, note string) error {
	state = normalizedKeyState(state)
	if state == "" {
		return fmt.Errorf("非法 key 状态")
	}
	now := time.Now()
	updates := map[string]any{"security_state": state, "security_note": note, "security_updated_at": &now}
	if state == ClientKeyStateRevoked {
		updates["revoked"] = true
		updates["revoked_at"] = &now
	} else {
		updates["revoked"] = false
		updates["revoked_at"] = nil
	}
	if err := s.db.Model(&model.ClientPullKey{}).Where("id = ?", keyID).Updates(updates).Error; err != nil {
		return err
	}
	return s.db.Create(&model.ClientProtectionAction{TargetType: "key", TargetValue: strconv.FormatUint(uint64(keyID), 10), Action: "key_state", Status: "active", Reason: note, CreatedAt: now, UpdatedAt: now}).Error
}

func (s *ClientDistSecurityService) SetChannelProtection(channelID, mode string) error {
	mode = normalizedProtectionMode(mode)
	if mode == "" {
		return fmt.Errorf("非法频道保护模式")
	}
	now := time.Now()
	res := s.db.Model(&model.ClientChannel{}).Where("channel_id = ?", channelID).Updates(map[string]any{"protection_mode": mode, "protection_updated_at": &now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrChannelNotFound
	}
	return s.db.Create(&model.ClientProtectionAction{TargetType: "channel", TargetValue: channelID, ChannelID: channelID, Action: "channel_protection", Status: "active", Reason: mode, CreatedAt: now, UpdatedAt: now}).Error
}

func (s *ClientDistSecurityService) ChannelProtectionMode(channelID string) (string, error) {
	var ch model.ClientChannel
	err := s.db.Select("protection_mode").Where("channel_id = ?", channelID).First(&ch).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ErrChannelNotFound
	}
	if err != nil {
		return "", err
	}
	return normalizedProtectionMode(ch.ProtectionMode), nil
}

func (s *ClientDistSecurityService) CheckRate(scope, key string) error {
	if key == "" || s.limits.RatePerMinute <= 0 {
		return nil
	}
	now := time.Now()
	bucketKey := "rate:" + scope + ":" + key
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.buckets[bucketKey]
	if b == nil || now.After(b.reset) {
		b = &securityBucket{reset: now.Add(time.Minute)}
		s.buckets[bucketKey] = b
	}
	b.count++
	if b.count > s.limits.RatePerMinute {
		return ErrRateLimited
	}
	return nil
}

func (s *ClientDistSecurityService) AcquireArtifact(ip string, keyID uint, channelID string) (*DownloadLease, error) {
	keys := []string{"global", "ip:" + ip, "key:" + strconv.FormatUint(uint64(keyID), 10), "channel:" + channelID}
	s.mu.Lock()
	defer s.mu.Unlock()
	if overSecurityLimit(s.active["global"], s.limits.ArtifactGlobalConcurrent) || overSecurityLimit(s.active["ip:"+ip], s.limits.ArtifactIPConcurrent) || overSecurityLimit(s.active["key:"+strconv.FormatUint(uint64(keyID), 10)], s.limits.ArtifactKeyConcurrent) || overSecurityLimit(s.active["channel:"+channelID], s.limits.ArtifactChannelConcurrent) {
		return nil, ErrDownloadConcurrencyLimited
	}
	for _, k := range keys {
		s.active[k]++
	}
	return &DownloadLease{svc: s, keys: keys}, nil
}
func (l *DownloadLease) Release() {
	if l == nil || l.svc == nil {
		return
	}
	l.svc.mu.Lock()
	defer l.svc.mu.Unlock()
	for _, k := range l.keys {
		if l.svc.active[k] > 0 {
			l.svc.active[k]--
		}
	}
}

func (s *ClientDistSecurityService) CheckBandwidth(ip string, keyID uint, channelID string, size int64) error {
	if s.limits.HourlyBandwidthBytes <= 0 || size <= 0 {
		return nil
	}
	hour := time.Now().UTC().Format("2006010215")
	for _, item := range []struct{ scope, key string }{{"ip", ip}, {"key", strconv.FormatUint(uint64(keyID), 10)}, {"channel", channelID}} {
		if item.key == "" || item.key == "0" {
			continue
		}
		if err := s.addBandwidth(item.scope, item.key, hour, size); err != nil {
			return err
		}
	}
	return nil
}
func (s *ClientDistSecurityService) addBandwidth(scope, key, bucket string, size int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	bk := "bytes:" + scope + ":" + key + ":" + bucket
	b := s.buckets[bk]
	if b == nil {
		b = &securityBucket{reset: time.Now().Add(time.Hour)}
		s.buckets[bk] = b
	}
	if b.bytes+size > s.limits.HourlyBandwidthBytes {
		return ErrBandwidthLimited
	}
	b.bytes += size
	_ = s.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "scope"}, {Name: "key"}, {Name: "bucket"}}, DoUpdates: clause.Assignments(map[string]any{"value": gorm.Expr("value + ?", size)})}).Create(&model.ClientSecurityCounter{Scope: scope, Key: key, Bucket: bucket, Value: size}).Error
	return nil
}

func (s *ClientDistSecurityService) IsArtifactAllowedForChannel(channelID, sha string) (bool, error) {
	var ch model.ClientChannel
	if err := s.db.Select("selected_core_sha256").Where("channel_id = ?", channelID).First(&ch).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if ch.SelectedCoreSHA256 != "" && ch.SelectedCoreSHA256 == sha {
		var cnt int64
		if err := s.db.Model(&model.Asset{}).Where("type = ? AND sha256 = ?", model.AssetTypeClientUpdaterCore, sha).Count(&cnt).Error; err != nil {
			return false, err
		}
		return cnt > 0, nil
	}
	if s.version != nil {
		info, err := s.version.GetCoreEndpointInfo(channelID)
		if err != nil && !errors.Is(err, ErrNoCoreVersion) && !errors.Is(err, ErrChannelNotFound) {
			return false, err
		}
		if info != nil && info.SHA256 == sha {
			return true, nil
		}
	}
	var versions []model.ClientVersion
	if err := s.db.Where("channel_id = ?", channelID).Order("version DESC").Limit(3).Find(&versions).Error; err != nil {
		return false, err
	}
	if len(versions) == 0 {
		return false, nil
	}
	for i := range versions {
		files, _, _, _, err := decodeVersionSnapshot(&versions[i])
		if err != nil {
			return false, err
		}
		if manifestFilesContainSHA(files, sha) {
			return true, nil
		}
	}
	return false, nil
}

type SecurityRankItem struct {
	Subject   string `json:"subject"`
	Count     int64  `json:"count"`
	Bytes     int64  `json:"bytes,omitempty"`
	RiskScore int64  `json:"riskScore,omitempty"`
}

type ClientDistIPAnalysis struct {
	IP              string    `json:"ip"`
	RequestCount    int64     `json:"requestCount"`
	RejectCount     int64     `json:"rejectCount"`
	InvalidKeyCount int64     `json:"invalidKeyCount"`
	NotFoundCount   int64     `json:"notFoundCount"`
	RangeCount      int64     `json:"rangeCount"`
	DownloadBytes   int64     `json:"downloadBytes"`
	KeyCount        int64     `json:"keyCount"`
	ChannelCount    int64     `json:"channelCount"`
	RiskScore       int64     `json:"riskScore"`
	Blocked         bool      `json:"blocked"`
	LastSeen        time.Time `json:"lastSeen"`
}

type ClientDistPlayerAnalysis struct {
	PlayerName       string    `json:"playerName"`
	InstallCount     int64     `json:"installCount"`
	MachineCount     int64     `json:"machineCount"`
	IPCount          int64     `json:"ipCount"`
	KeyCount         int64     `json:"keyCount"`
	ChannelCount     int64     `json:"channelCount"`
	DownloadBytes    int64     `json:"downloadBytes"`
	AbnormalRequests int64     `json:"abnormalRequests"`
	RiskScore        int64     `json:"riskScore"`
	LastSeen         time.Time `json:"lastSeen"`
}

func (s *ClientDistSecurityService) Overview() (map[string]any, error) {
	out := map[string]any{
		"activeDownloads":        s.activeDownloads(),
		"downloadBytesPerSecond": 0,
		"abnormalRequests":       int64(0),
		"unauthorizedRequests":   int64(0),
		"forbiddenRequests":      int64(0),
		"rateLimitedRequests":    int64(0),
		"blockedIpCount":         int64(0),
		"throttledKeyCount":      int64(0),
		"protectedChannelCount":  int64(0),
		"topIps":                 []SecurityRankItem{},
		"topKeys":                []SecurityRankItem{},
		"topChannels":            []SecurityRankItem{},
		"topPlayers":             []SecurityRankItem{},
	}
	var abnormal int64
	if err := s.db.Model(&model.ClientSecurityRiskEvent{}).Count(&abnormal).Error; err != nil {
		return nil, err
	}
	out["abnormalRequests"] = abnormal
	for _, item := range []struct {
		key   string
		where string
	}{
		{"unauthorizedRequests", "status = 401"},
		{"forbiddenRequests", "status = 403"},
		{"rateLimitedRequests", "status = 429"},
	} {
		var n int64
		if err := s.db.Model(&model.ClientDistEvent{}).Where(item.where).Count(&n).Error; err != nil {
			return nil, err
		}
		out[item.key] = n
	}
	now := time.Now()
	var blocked int64
	if err := s.db.Model(&model.ClientProtectionAction{}).Where("target_type = ? AND action IN ? AND status = ? AND (expires_at IS NULL OR expires_at > ?)", "ip", []string{"temp_block", "ip_temp_block"}, "active", now).Count(&blocked).Error; err != nil {
		return nil, err
	}
	out["blockedIpCount"] = blocked
	var throttled int64
	if err := s.db.Model(&model.ClientPullKey{}).Where("security_state IN ?", []string{ClientKeyStateThrottled, ClientKeyStateSuspended}).Count(&throttled).Error; err != nil {
		return nil, err
	}
	out["throttledKeyCount"] = throttled
	var protected int64
	if err := s.db.Model(&model.ClientChannel{}).Where("protection_mode <> ?", ClientChannelModeNormal).Count(&protected).Error; err != nil {
		return nil, err
	}
	out["protectedChannelCount"] = protected
	if ranks, err := s.rankClientDist("ip", "ip != ''", 8); err == nil {
		out["topIps"] = ranks
	} else {
		return nil, err
	}
	if ranks, err := s.rankClientDist("channel_id", "channel_id != ''", 8); err == nil {
		out["topChannels"] = ranks
	} else {
		return nil, err
	}
	if ranks, err := s.rankProfiles("key_prefix", "key_prefix != ''", 8); err == nil {
		out["topKeys"] = ranks
	} else {
		return nil, err
	}
	if ranks, err := s.rankProfiles("player_name", "player_name != ''", 8); err == nil {
		out["topPlayers"] = ranks
	} else {
		return nil, err
	}
	return out, nil
}

func (s *ClientDistSecurityService) activeDownloads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active["global"]
}

func (s *ClientDistSecurityService) rankClientDist(column, where string, limit int) ([]SecurityRankItem, error) {
	var rows []SecurityRankItem
	err := s.db.Model(&model.ClientDistEvent{}).
		Select(column + " AS subject, COUNT(*) AS count, COALESCE(SUM(bytes), 0) AS bytes").
		Where(where).Group(column).Order("count DESC").Limit(limit).Scan(&rows).Error
	return rows, err
}

func (s *ClientDistSecurityService) rankProfiles(column, where string, limit int) ([]SecurityRankItem, error) {
	var rows []SecurityRankItem
	err := s.db.Model(&model.ClientSecurityProfile{}).
		Select(column + " AS subject, COUNT(*) AS count, COALESCE(SUM(risk_score), 0) AS risk_score").
		Where(where).Group(column).Order("count DESC").Limit(limit).Scan(&rows).Error
	return rows, err
}

func (s *ClientDistSecurityService) ListProfiles() ([]model.ClientSecurityProfile, error) {
	var p []model.ClientSecurityProfile
	return p, s.db.Order("last_seen DESC").Limit(200).Find(&p).Error
}
func (s *ClientDistSecurityService) ListRiskEvents() ([]model.ClientSecurityRiskEvent, error) {
	var e []model.ClientSecurityRiskEvent
	return e, s.db.Order("created_at DESC").Limit(200).Find(&e).Error
}

// ClientDistSecurityLogFilter 是客户端分发安全全量日志查询条件。
type ClientDistSecurityLogFilter struct {
	Type       string
	ChannelID  string
	MachineID  string
	PlayerName string
	IP         string
	Page       int
	PageSize   int
}

// ClientDistSecurityLogItem 是安全日志页统一列表项。
type ClientDistSecurityLogItem struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Title      string         `json:"title"`
	ChannelID  string         `json:"channelId,omitempty"`
	MachineID  string         `json:"machineId,omitempty"`
	PlayerName string         `json:"playerName,omitempty"`
	IP         string         `json:"ip,omitempty"`
	Status     string         `json:"status,omitempty"`
	ErrCode    string         `json:"errCode,omitempty"`
	CreatedAt  time.Time      `json:"createdAt"`
	Detail     map[string]any `json:"detail"`
}

// ClientDistSecurityLogPage 是安全日志分页结果。
type ClientDistSecurityLogPage struct {
	Items    []ClientDistSecurityLogItem `json:"items"`
	Total    int                         `json:"total"`
	Page     int                         `json:"page"`
	PageSize int                         `json:"pageSize"`
}

// SearchLogs 合并查询 security hello、风险事件、保护动作、请求日志、运行态心跳和更新遥测。
func (s *ClientDistSecurityService) SearchLogs(f ClientDistSecurityLogFilter) (*ClientDistSecurityLogPage, error) {
	if f.Type == "all" {
		f.Type = ""
	}
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 200 {
		f.PageSize = 50
	}
	items := make([]ClientDistSecurityLogItem, 0, f.PageSize*2)
	appenders := []struct {
		typ string
		fn  func(ClientDistSecurityLogFilter, int) ([]ClientDistSecurityLogItem, error)
	}{
		{"hello", s.securityHelloLogs},
		{"risk", s.securityRiskLogs},
		{"action", s.securityActionLogs},
		{"request", s.securityRequestLogs},
		{"runtime", s.securityRuntimeLogs},
		{"telemetry", s.securityTelemetryLogs},
	}
	limit := f.Page * f.PageSize
	for _, app := range appenders {
		if f.Type != "" && f.Type != app.typ {
			continue
		}
		rows, err := app.fn(f, limit)
		if err != nil {
			return nil, err
		}
		items = append(items, rows...)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	total := len(items)
	start := (f.Page - 1) * f.PageSize
	if start > total {
		start = total
	}
	end := start + f.PageSize
	if end > total {
		end = total
	}
	return &ClientDistSecurityLogPage{Items: items[start:end], Total: total, Page: f.Page, PageSize: f.PageSize}, nil
}

func (s *ClientDistSecurityService) securityHelloLogs(f ClientDistSecurityLogFilter, limit int) ([]ClientDistSecurityLogItem, error) {
	var rows []model.ClientSecurityHello
	db := s.db.Order("created_at DESC").Limit(limit)
	db = applySecurityCommonFilter(db, f, "channel_id", "machine_id", "player_name", "ip")
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ClientDistSecurityLogItem, 0, len(rows))
	for _, r := range rows {
		status := "rejected"
		if r.Accepted {
			status = "accepted"
		}
		out = append(out, ClientDistSecurityLogItem{ID: logID("hello", r.ID), Type: "hello", Title: "安全画像上报", ChannelID: r.ChannelID, MachineID: r.MachineID, PlayerName: r.PlayerName, IP: r.IP, Status: status, ErrCode: r.ErrCode, CreatedAt: r.CreatedAt, Detail: map[string]any{"installId": r.InstallID, "keyPrefix": r.KeyPrefix, "userAgent": r.UserAgent, "payload": parseLogJSON(r.PayloadJSON)}})
	}
	return out, nil
}

func (s *ClientDistSecurityService) securityRiskLogs(f ClientDistSecurityLogFilter, limit int) ([]ClientDistSecurityLogItem, error) {
	var rows []model.ClientSecurityRiskEvent
	db := s.db.Order("created_at DESC").Limit(limit)
	db = applySecurityCommonFilter(db, f, "channel_id", "machine_id", "player_name", "ip")
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ClientDistSecurityLogItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, ClientDistSecurityLogItem{ID: logID("risk", r.ID), Type: "risk", Title: r.RuleCode, ChannelID: r.ChannelID, MachineID: r.MachineID, PlayerName: r.PlayerName, IP: r.IP, Status: r.Severity, ErrCode: r.RuleCode, CreatedAt: r.CreatedAt, Detail: map[string]any{"installId": r.InstallID, "subjectType": r.SubjectType, "subjectValue": r.SubjectValue, "keyPrefix": r.KeyPrefix, "reason": r.Reason, "detail": parseLogJSON(r.DetailJSON)}})
	}
	return out, nil
}

func (s *ClientDistSecurityService) securityActionLogs(f ClientDistSecurityLogFilter, limit int) ([]ClientDistSecurityLogItem, error) {
	var rows []model.ClientProtectionAction
	if f.MachineID != "" || f.PlayerName != "" {
		return []ClientDistSecurityLogItem{}, nil
	}
	db := s.db.Order("created_at DESC").Limit(limit)
	if f.ChannelID != "" {
		db = db.Where("channel_id = ? OR target_value = ?", f.ChannelID, f.ChannelID)
	}
	if f.IP != "" {
		db = db.Where("target_type = ? AND target_value = ?", "ip", f.IP)
	}
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ClientDistSecurityLogItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, ClientDistSecurityLogItem{ID: logID("action", r.ID), Type: "action", Title: r.Action, ChannelID: r.ChannelID, Status: r.Status, CreatedAt: r.CreatedAt, Detail: map[string]any{"targetType": r.TargetType, "targetValue": r.TargetValue, "reason": r.Reason, "auto": r.Auto, "expiresAt": r.ExpiresAt, "createdBy": r.CreatedBy}})
	}
	return out, nil
}

func (s *ClientDistSecurityService) securityRequestLogs(f ClientDistSecurityLogFilter, limit int) ([]ClientDistSecurityLogItem, error) {
	var rows []model.ClientDistEvent
	db := s.db.Order("created_at DESC").Limit(limit)
	db = applySecurityCommonFilter(db, f, "channel_id", "machine_id", "", "ip")
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ClientDistSecurityLogItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, ClientDistSecurityLogItem{ID: logID("request", r.ID), Type: "request", Title: r.Kind, ChannelID: r.ChannelID, MachineID: r.MachineID, IP: r.IP, Status: strconv.Itoa(r.Status), ErrCode: r.ErrCode, CreatedAt: r.CreatedAt, Detail: map[string]any{"version": r.Version, "artifactSha": r.ArtifactSHA, "bytes": r.Bytes, "method": r.Method, "path": r.Path, "errReason": r.ErrReason, "durationMs": r.DurationMs, "etag": r.ETag, "requestHeaders": parseLogJSON(r.RequestHeadersJSON), "responseHeaders": parseLogJSON(r.ResponseHeadersJSON)}})
	}
	return out, nil
}

func (s *ClientDistSecurityService) securityRuntimeLogs(f ClientDistSecurityLogFilter, limit int) ([]ClientDistSecurityLogItem, error) {
	var rows []model.ClientRuntimeState
	db := s.db.Order("last_heartbeat_at DESC").Limit(limit)
	db = applySecurityCommonFilter(db, f, "channel_id", "machine_id", "player_name", "ip")
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ClientDistSecurityLogItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, ClientDistSecurityLogItem{ID: logID("runtime", r.ID), Type: "runtime", Title: "运行态心跳", ChannelID: r.ChannelID, MachineID: r.MachineID, PlayerName: r.PlayerName, IP: r.IP, Status: r.LastUpdateResult, CreatedAt: r.LastHeartbeatAt, Detail: map[string]any{"platform": r.Platform, "javaVersion": r.JavaVersion, "launcher": r.Launcher, "coreVersion": r.CoreVersion, "localVersion": r.LocalVersion, "firstSeenAt": r.FirstSeenAt, "lastUpdateAt": r.LastUpdateAt}})
	}
	return out, nil
}

func (s *ClientDistSecurityService) securityTelemetryLogs(f ClientDistSecurityLogFilter, limit int) ([]ClientDistSecurityLogItem, error) {
	var rows []model.ClientTelemetry
	db := s.db.Order("created_at DESC").Limit(limit)
	db = applySecurityCommonFilter(db, f, "channel_id", "machine_id", "player_name", "ip")
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ClientDistSecurityLogItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, ClientDistSecurityLogItem{ID: logID("telemetry", r.ID), Type: "telemetry", Title: "更新遥测", ChannelID: r.ChannelID, MachineID: r.MachineID, PlayerName: r.PlayerName, IP: r.IP, Status: r.Result, ErrCode: r.Error, CreatedAt: r.CreatedAt, Detail: map[string]any{"fromVersion": r.FromVersion, "toVersion": r.ToVersion, "os": r.OS, "javaVersion": r.JavaVersion, "launcher": r.Launcher, "durationMs": r.DurationMs, "bootSuccess": r.BootSuccess}})
	}
	return out, nil
}

func applySecurityCommonFilter(db *gorm.DB, f ClientDistSecurityLogFilter, channelCol, machineCol, playerCol, ipCol string) *gorm.DB {
	if f.ChannelID != "" {
		if channelCol == "" {
			return db.Where("1 = 0")
		}
		db = db.Where(channelCol+" = ?", f.ChannelID)
	}
	if f.MachineID != "" {
		if machineCol == "" {
			return db.Where("1 = 0")
		}
		db = db.Where(machineCol+" = ?", f.MachineID)
	}
	if f.PlayerName != "" {
		if playerCol == "" {
			return db.Where("1 = 0")
		}
		db = db.Where(playerCol+" = ?", f.PlayerName)
	}
	if f.IP != "" {
		if ipCol == "" {
			return db.Where("1 = 0")
		}
		db = db.Where(ipCol+" = ?", f.IP)
	}
	return db
}

func logID(prefix string, id uint) string { return prefix + ":" + strconv.FormatUint(uint64(id), 10) }

func parseLogJSON(raw string) any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return raw
	}
	return out
}

func (s *ClientDistSecurityService) ListGroups() ([]model.ClientSecurityGroup, error) {
	var groups []model.ClientSecurityGroup
	return groups, s.db.Order("updated_at DESC, id DESC").Limit(200).Find(&groups).Error
}
func (s *ClientDistSecurityService) CreateGroup(group *model.ClientSecurityGroup) error {
	if err := validateSecurityGroup(group); err != nil {
		return err
	}
	return s.db.Create(group).Error
}
func (s *ClientDistSecurityService) UpdateGroup(id uint, group *model.ClientSecurityGroup) error {
	if err := validateSecurityGroup(group); err != nil {
		return err
	}
	return s.db.Model(&model.ClientSecurityGroup{}).Where("id = ?", id).Updates(map[string]any{"name": group.Name, "kind": group.Kind, "target_type": group.TargetType, "rule_json": group.RuleJSON, "action_policy_json": group.ActionPolicyJSON, "enabled": group.Enabled}).Error
}
func (s *ClientDistSecurityService) DeleteGroup(id uint) error {
	return s.db.Delete(&model.ClientSecurityGroup{}, id).Error
}
func validateSecurityGroup(group *model.ClientSecurityGroup) error {
	if strings.TrimSpace(group.Name) == "" || strings.TrimSpace(group.TargetType) == "" {
		return fmt.Errorf("分组名和目标类型必填")
	}
	if group.Kind == "" {
		group.Kind = "manual"
	}
	return nil
}
func (s *ClientDistSecurityService) ListIPAnalysis(limit int) ([]ClientDistIPAnalysis, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	type ipAnalysisRow struct {
		IP              string
		RequestCount    int64
		RejectCount     int64
		InvalidKeyCount int64
		NotFoundCount   int64
		RangeCount      int64
		DownloadBytes   int64
		ChannelCount    int64
		LastSeen        string
	}
	var rawRows []ipAnalysisRow
	if err := s.db.Model(&model.ClientDistEvent{}).
		Select("ip, COUNT(*) AS request_count, COALESCE(SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END), 0) AS reject_count, COALESCE(SUM(CASE WHEN err_code = 'INVALID_CLIENT_KEY' THEN 1 ELSE 0 END), 0) AS invalid_key_count, COALESCE(SUM(CASE WHEN err_code = 'ARTIFACT_NOT_FOUND' THEN 1 ELSE 0 END), 0) AS not_found_count, COALESCE(SUM(CASE WHEN err_code IN ('INVALID_RANGE','RANGE_SMALL') THEN 1 ELSE 0 END), 0) AS range_count, COALESCE(SUM(bytes), 0) AS download_bytes, COUNT(DISTINCT channel_id) AS channel_count, MAX(created_at) AS last_seen").
		Where("ip != ''").Group("ip").Order("request_count DESC").Limit(limit).Scan(&rawRows).Error; err != nil {
		return nil, err
	}
	blocked := map[string]bool{}
	var actions []model.ClientProtectionAction
	if err := s.db.Where("target_type = ? AND action IN ? AND status = ? AND (expires_at IS NULL OR expires_at > ?)", "ip", []string{"temp_block", "ip_temp_block"}, "active", time.Now()).Find(&actions).Error; err != nil {
		return nil, err
	}
	for _, a := range actions {
		blocked[a.TargetValue] = true
	}
	rows := make([]ClientDistIPAnalysis, 0, len(rawRows))
	for _, row := range rawRows {
		out := ClientDistIPAnalysis{
			IP: row.IP, RequestCount: row.RequestCount, RejectCount: row.RejectCount,
			InvalidKeyCount: row.InvalidKeyCount, NotFoundCount: row.NotFoundCount, RangeCount: row.RangeCount,
			DownloadBytes: row.DownloadBytes, ChannelCount: row.ChannelCount, LastSeen: parseSecurityAggregateTime(row.LastSeen),
			RiskScore: row.RejectCount + row.RangeCount, Blocked: blocked[row.IP],
		}
		rows = append(rows, out)
	}
	return rows, nil
}
func (s *ClientDistSecurityService) ListPlayerAnalysis(limit int) ([]ClientDistPlayerAnalysis, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	type playerAnalysisRow struct {
		PlayerName   string
		InstallCount int64
		MachineCount int64
		IPCount      int64
		KeyCount     int64
		ChannelCount int64
		RiskScore    int64
		LastSeen     string
	}
	var rawRows []playerAnalysisRow
	if err := s.db.Model(&model.ClientSecurityProfile{}).
		Select("player_name, COUNT(DISTINCT install_id) AS install_count, COUNT(DISTINCT machine_id) AS machine_count, COUNT(DISTINCT last_ip) AS ip_count, COUNT(DISTINCT key_id) AS key_count, COUNT(DISTINCT channel_id) AS channel_count, COALESCE(SUM(risk_score), 0) AS risk_score, MAX(last_seen) AS last_seen").
		Where("player_name != ''").Group("player_name").Order("install_count DESC").Limit(limit).Scan(&rawRows).Error; err != nil {
		return nil, err
	}
	rows := make([]ClientDistPlayerAnalysis, 0, len(rawRows))
	for _, row := range rawRows {
		rows = append(rows, ClientDistPlayerAnalysis{
			PlayerName: row.PlayerName, InstallCount: row.InstallCount, MachineCount: row.MachineCount,
			IPCount: row.IPCount, KeyCount: row.KeyCount, ChannelCount: row.ChannelCount,
			RiskScore: row.RiskScore, LastSeen: parseSecurityAggregateTime(row.LastSeen),
		})
	}
	return rows, nil
}

func parseSecurityAggregateTime(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	layouts := []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999"}
	for _, layout := range layouts {
		if ts, err := time.Parse(layout, v); err == nil {
			return ts.UTC()
		}
	}
	return time.Time{}
}

func manifestFilesContainSHA(files []ManifestFile, sha string) bool {
	for _, f := range files {
		if f.Artifact.SHA256 == sha {
			return true
		}
	}
	return false
}
func normalizedKeyState(state string) string {
	switch strings.TrimSpace(state) {
	case "", ClientKeyStateNormal:
		return ClientKeyStateNormal
	case ClientKeyStateObserve, ClientKeyStateThrottled, ClientKeyStateSuspended, ClientKeyStateRevoked:
		return state
	}
	return ""
}
func normalizedProtectionMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case "", ClientChannelModeNormal:
		return ClientChannelModeNormal
	case ClientChannelModeProtected, "throttle", "concurrency", "queue", "retry_after":
		return ClientChannelModeProtected
	}
	return ""
}
func overSecurityLimit(current, limit int) bool { return limit > 0 && current >= limit }
func truncateSecurity(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
