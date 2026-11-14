package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	payload, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("序列化安全 hello 载荷失败: %w", err)
	}
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

func (s *ClientDistSecurityService) ResolveProfilePlayerName(channelID, machineID, installID string) string {
	if s == nil || channelID == "" || machineID == "" || installID == "" {
		return ""
	}
	var profile model.ClientSecurityProfile
	err := s.db.Select("player_name").Where("channel_id = ? AND machine_id = ? AND install_id = ?", channelID, machineID, installID).First(&profile).Error
	if err != nil {
		return ""
	}
	return profile.PlayerName
}

func (s *ClientDistSecurityService) RecordRiskEvent(code, channelID, machineID, installID, playerName, ip string, key *model.ClientPullKey, severity string, detail any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("序列化安全风险详情失败: %w", err)
	}
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

// RecordRiskEventSafe 记录不应阻断请求的安全风险事件；失败时保留告警供运维排查。
func (s *ClientDistSecurityService) RecordRiskEventSafe(code, channelID, machineID, installID, playerName, ip string, key *model.ClientPullKey, severity string, detail any) {
	if err := s.RecordRiskEvent(code, channelID, machineID, installID, playerName, ip, key, severity, detail); err != nil {
		slog.Warn("记录客户端分发风险事件失败", "code", code, "channelId", channelID, "error", err)
	}
}

func (s *ClientDistSecurityService) BlockIP(ip, channelID, reason string, ttl time.Duration, createdBy uint) (*model.ClientProtectionAction, error) {
	if net.ParseIP(ip) == nil {
		return nil, fmt.Errorf("非法 IP")
	}
	expires := time.Now().Add(ttl)
	if ttl <= 0 {
		expires = time.Now().Add(time.Hour)
	}
	a := &model.ClientProtectionAction{TargetType: "ip", TargetValue: ip, ChannelID: channelID, Action: "temp_block", Status: "active", Reason: reason, Auto: createdBy == 0, ExpiresAt: &expires, CreatedBy: createdBy}
	return a, s.db.Create(a).Error
}

// ActiveIPBlockForChannel 查询该 IP 在指定频道是否处于生效中的临时封禁（R28：封禁可限定频道）。
// 语义：channelID 非空时同时匹配「本频道封禁」与「全局封禁」（channel_id 为空 = 不限频道的通用封禁），
// 使 A 频道的封禁不再误伤同一 IP 访问 B（NAT/共享出口场景）；channelID 为空表示调用方此刻还无法确定频道
// （如内容寻址的制品下载在鉴权前），此时只认全局封禁——宁可不拦，也不跨频道误伤。
func (s *ClientDistSecurityService) ActiveIPBlockForChannel(ip, channelID string) (*model.ClientProtectionAction, bool) {
	var a model.ClientProtectionAction
	if s == nil || ip == "" {
		return &a, false
	}
	q := s.db.Where("target_type = ? AND target_value = ? AND action IN ? AND status = ? AND (expires_at IS NULL OR expires_at > ?)", "ip", ip, []string{"temp_block", "ip_temp_block"}, "active", time.Now())
	if channelID == "" {
		q = q.Where("channel_id = ?", "")
	} else {
		q = q.Where("channel_id = ? OR channel_id = ?", channelID, "")
	}
	err := q.Order("id DESC").First(&a).Error
	return &a, err == nil
}
func (s *ClientDistSecurityService) CancelAction(id uint) error {
	now := time.Now()
	return s.db.Model(&model.ClientProtectionAction{}).Where("id = ?", id).Updates(map[string]any{"status": "canceled", "canceled_at": &now}).Error
}
func (s *ClientDistSecurityService) ListActions() ([]model.ClientProtectionAction, error) {
	return s.ListActionsFiltered(ClientActionFilter{})
}

// ClientActionFilter 处置流水服务端筛选（缺省=近 200 条全量）。
type ClientActionFilter struct {
	TargetType string
	Status     string
	ChannelID  string
	Q          string
	Limit      int
}

func (s *ClientDistSecurityService) ListActionsFiltered(f ClientActionFilter) ([]model.ClientProtectionAction, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	} else if limit > 500 {
		limit = 500
	}
	q := s.db.Model(&model.ClientProtectionAction{})
	if f.TargetType != "" {
		q = q.Where("target_type = ?", f.TargetType)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.ChannelID != "" {
		q = q.Where("channel_id = ?", f.ChannelID)
	}
	if kw := strings.TrimSpace(f.Q); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("target_value LIKE ? OR action LIKE ? OR reason LIKE ?", like, like, like)
	}
	var a []model.ClientProtectionAction
	return a, q.Order("created_at DESC").Limit(limit).Find(&a).Error
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
	// 处置记录必须带上密钥所属频道（R37）。
	//
	// 此前没填 ChannelID：流水按频道筛选时（`channel_id = ? OR target_value = ?`），
	// 一条「改了 A 频道 key」的记录只有在 target_value 恰好等于频道 slug 时才出现——
	// 而 key 记录的 target_value 是密钥 ID，于是选 A 频道看不到刚做的动作，切「全部」才看得到。
	// 取不到密钥归属时留空（不阻断处置本身），流水退化为只在「全部」下可见。
	channelID := ""
	var key model.ClientPullKey
	if err := s.db.Select("channel_id").Where("id = ?", keyID).First(&key).Error; err == nil {
		channelID = key.ChannelID
	}
	return s.db.Create(&model.ClientProtectionAction{TargetType: "key", TargetValue: strconv.FormatUint(uint64(keyID), 10), ChannelID: channelID, Action: "key_state", Status: "active", Reason: note, CreatedAt: now, UpdatedAt: now}).Error
}

func (s *ClientDistSecurityService) SetChannelProtection(channelID, mode string) error {
	requestedMode := strings.TrimSpace(mode)
	if normalizedProtectionMode(requestedMode) == "" {
		return fmt.Errorf("非法频道保护模式")
	}
	if requestedMode == "" {
		requestedMode = ClientChannelModeNormal
	}
	now := time.Now()
	res := s.db.Model(&model.ClientChannel{}).Where("channel_id = ?", channelID).Updates(map[string]any{"protection_mode": requestedMode, "protection_updated_at": &now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrChannelNotFound
	}
	return s.db.Create(&model.ClientProtectionAction{TargetType: "channel", TargetValue: channelID, ChannelID: channelID, Action: "channel_protection", Status: "active", Reason: requestedMode, CreatedAt: now, UpdatedAt: now}).Error
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

// ClientChannelSecuritySummary 是频道工作台使用的近窗安全摘要。
type ClientChannelSecuritySummary struct {
	ChannelID          string `json:"channelId"`
	RiskLevel          string `json:"riskLevel"`
	AbnormalRequests   int64  `json:"abnormalRequests"`
	BlockedIPCount     int64  `json:"blockedIpCount"`
	RestrictedKeyCount int64  `json:"restrictedKeyCount"`
	ProtectionMode     string `json:"protectionMode"`
	WindowMinutes      int    `json:"windowMinutes"`
}

// ClientSecurityProfileDetail 在画像字段之外补充风险与保护动作时间线。
type ClientSecurityProfileDetail struct {
	model.ClientSecurityProfile
	RecentEvents      []model.ClientSecurityRiskEvent `json:"recentEvents"`
	ProtectionActions []model.ClientProtectionAction  `json:"protectionActions"`
}

// ipAnalysisRow 是 IP 剖析的聚合中间行（SQL 聚合结果，再补密钥数/封禁态后转成对外的 ClientDistIPAnalysis）。
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

// ClientSecurityProfileFilter 是客户端画像列表的筛选条件（R32）。
//
// 现实缺陷：handler 完全不读查询参数，页面把 channelId/playerName/machineId/ip 都发了过去
// 却始终拿到「全局最新 200 条」——想靠筛选定位某个旧目标根本查不出来。
type ClientSecurityProfileFilter struct {
	ChannelID  string
	MachineID  string
	InstallID  string
	PlayerName string
	IP         string
	Limit      int
}

// ListProfiles 按筛选条件列出客户端画像（缺省=全局最新 200 条）。
func (s *ClientDistSecurityService) ListProfiles(f ...ClientSecurityProfileFilter) ([]model.ClientSecurityProfile, error) {
	var filter ClientSecurityProfileFilter
	if len(f) > 0 {
		filter = f[0]
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 200
	} else if limit > 1000 {
		limit = 1000
	}
	var p []model.ClientSecurityProfile
	q := s.db.Model(&model.ClientSecurityProfile{})
	if filter.ChannelID != "" {
		q = q.Where("channel_id = ?", filter.ChannelID)
	}
	if filter.MachineID != "" {
		q = q.Where("machine_id = ?", filter.MachineID)
	}
	if filter.InstallID != "" {
		q = q.Where("install_id = ?", filter.InstallID)
	}
	if filter.PlayerName != "" {
		// 画像同时存了原始名与归一化名：按小写查归一化列，避免大小写不同查不到。
		q = q.Where("player_name_norm = ?", strings.ToLower(filter.PlayerName))
	}
	if filter.IP != "" {
		q = q.Where("last_ip = ?", filter.IP)
	}
	return p, q.Order("last_seen DESC").Limit(limit).Find(&p).Error
}

func (s *ClientDistSecurityService) ProfileDetail(id uint) (*ClientSecurityProfileDetail, error) {
	var profile model.ClientSecurityProfile
	if err := s.db.First(&profile, id).Error; err != nil {
		return nil, err
	}
	var events []model.ClientSecurityRiskEvent
	if err := s.db.Where("channel_id = ? AND (machine_id = ? OR install_id = ?)", profile.ChannelID, profile.MachineID, profile.InstallID).Order("created_at DESC").Limit(100).Find(&events).Error; err != nil {
		return nil, err
	}
	var actions []model.ClientProtectionAction
	keyID := strconv.FormatUint(uint64(profile.KeyID), 10)
	where := "channel_id = ? OR (target_type = ? AND target_value = ?) OR (target_type = ? AND target_value = ?) OR (target_type = ? AND target_value = ?)"
	args := []any{profile.ChannelID, "channel", profile.ChannelID, "key", keyID, "ip", profile.LastIP}
	if err := s.db.Where(where, args...).Order("created_at DESC").Limit(100).Find(&actions).Error; err != nil {
		return nil, err
	}
	return &ClientSecurityProfileDetail{ClientSecurityProfile: profile, RecentEvents: events, ProtectionActions: actions}, nil
}

func (s *ClientDistSecurityService) ChannelSummary(channelID string, window time.Duration) (*ClientChannelSecuritySummary, error) {
	if window <= 0 {
		window = time.Hour
	}
	var channel model.ClientChannel
	if err := s.db.Select("protection_mode").Where("channel_id = ?", channelID).First(&channel).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrChannelNotFound
		}
		return nil, err
	}
	mode := strings.TrimSpace(channel.ProtectionMode)
	if mode == "" {
		mode = ClientChannelModeNormal
	}
	out := &ClientChannelSecuritySummary{ChannelID: channelID, RiskLevel: "info", ProtectionMode: mode, WindowMinutes: int(window.Minutes())}
	if err := s.fillChannelSummary(out, time.Now().Add(-window)); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ClientDistSecurityService) fillChannelSummary(out *ClientChannelSecuritySummary, since time.Time) error {
	var events []model.ClientSecurityRiskEvent
	if err := s.db.Select("severity").Where("channel_id = ? AND created_at >= ?", out.ChannelID, since).Find(&events).Error; err != nil {
		return err
	}
	out.AbnormalRequests = int64(len(events))
	for _, event := range events {
		if securityLevelRank(event.Severity) > securityLevelRank(out.RiskLevel) {
			out.RiskLevel = event.Severity
		}
	}
	now := time.Now()
	if err := s.db.Model(&model.ClientProtectionAction{}).Where("channel_id = ? AND target_type = ? AND action IN ? AND status = ? AND (expires_at IS NULL OR expires_at > ?)", out.ChannelID, "ip", []string{"temp_block", "ip_temp_block"}, "active", now).Count(&out.BlockedIPCount).Error; err != nil {
		return err
	}
	states := []string{ClientKeyStateThrottled, ClientKeyStateSuspended, ClientKeyStateRevoked}
	return s.db.Model(&model.ClientPullKey{}).Where("channel_id = ? AND security_state IN ?", out.ChannelID, states).Count(&out.RestrictedKeyCount).Error
}

func securityLevelRank(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "critical":
		return 4
	case "high":
		return 3
	case "warn", "warning", "medium":
		return 2
	default:
		return 1
	}
}

// ClientSecurityRiskEventFilter 是风险事件列表的筛选条件（R32）。
//
// 与画像同源问题：handler 之前不读任何参数，前端把频道/IP/机器/玩家/规则都发过来，
// 却永远只能看到「全局最新 200 条」，旧目标的风险记录无法检出。
type ClientSecurityRiskEventFilter struct {
	ChannelID  string
	MachineID  string
	InstallID  string
	PlayerName string
	IP         string
	RuleCode   string
	KeyID      string
	Limit      int
}

// ListRiskEvents 按筛选条件列出风险事件（缺省=全局最新 200 条）。
func (s *ClientDistSecurityService) ListRiskEvents(f ...ClientSecurityRiskEventFilter) ([]model.ClientSecurityRiskEvent, error) {
	var filter ClientSecurityRiskEventFilter
	if len(f) > 0 {
		filter = f[0]
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 200
	} else if limit > 1000 {
		limit = 1000
	}
	var e []model.ClientSecurityRiskEvent
	q := s.db.Model(&model.ClientSecurityRiskEvent{})
	if filter.ChannelID != "" {
		q = q.Where("channel_id = ?", filter.ChannelID)
	}
	if filter.MachineID != "" {
		q = q.Where("machine_id = ?", filter.MachineID)
	}
	if filter.InstallID != "" {
		q = q.Where("install_id = ?", filter.InstallID)
	}
	if filter.PlayerName != "" {
		q = q.Where("player_name = ?", filter.PlayerName)
	}
	if filter.IP != "" {
		q = q.Where("ip = ?", filter.IP)
	}
	if filter.RuleCode != "" {
		q = q.Where("rule_code = ?", filter.RuleCode)
	}
	if filter.KeyID != "" {
		if id, err := strconv.ParseUint(filter.KeyID, 10, 64); err == nil {
			q = q.Where("key_id = ?", uint(id))
		} else {
			// 非法 keyId 直接给空集：不能静默忽略，否则用户以为是「没有记录」以外的筛选生效。
			return []model.ClientSecurityRiskEvent{}, nil
		}
	}
	return e, q.Order("created_at DESC").Limit(limit).Find(&e).Error
}

// ClientDistSecurityLogFilter 是客户端分发安全全量日志查询条件。
//
// From/To 是时间窗（R31）：列表与导出必须属于同一集合，而此前列表不带时间窗
// （返回全量历史）、导出却按 range 过滤，两边对不上。现在两者都从页头同一个
// 时间选择器派生参数（range 或 from/to）。
type ClientDistSecurityLogFilter struct {
	Type       string
	ChannelID  string
	MachineID  string
	PlayerName string
	IP         string
	// ErrCode 按语义错误码精确筛（R36）。前端一直在发 errCode，导出也确实按它过滤，
	// 只有列表端点没接——于是「筛 INVALID_CLIENT_KEY 的视图」里混进了其它错误码的行。
	ErrCode  string
	From     *time.Time
	To       *time.Time
	Page     int
	PageSize int
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
	sources := []struct {
		typ   string
		query func(ClientDistSecurityLogFilter) *gorm.DB
		fn    func(ClientDistSecurityLogFilter, int) ([]ClientDistSecurityLogItem, error)
	}{
		{"hello", s.securityHelloQuery, s.securityHelloLogs},
		{"risk", s.securityRiskQuery, s.securityRiskLogs},
		{"action", s.securityActionQuery, s.securityActionLogs},
		{"request", s.securityRequestQuery, s.securityRequestLogs},
		{"runtime", s.securityRuntimeQuery, s.securityRuntimeLogs},
		{"telemetry", s.securityTelemetryQuery, s.securityTelemetryLogs},
	}
	// total 必须是**真实命中数**（R38）。先逐源 COUNT，再按真实总数决定页边界，
	// 避免把不可信的 page 乘进分配容量或查询 LIMIT。
	counts := make([]int, len(sources))
	total := 0
	selectedSources := 0
	for i, src := range sources {
		if f.Type != "" && f.Type != src.typ {
			continue
		}
		var count int64
		if err := src.query(f).Count(&count).Error; err != nil {
			return nil, err
		}
		counts[i] = int(count)
		total += counts[i]
		selectedSources++
	}
	lastPage := total > 0 && f.Page <= 1+(total-1)/f.PageSize
	if !lastPage {
		return &ClientDistSecurityLogPage{
			Items: []ClientDistSecurityLogItem{}, Total: total, Page: f.Page, PageSize: f.PageSize,
		}, nil
	}

	// Page 已落在真实结果范围内，因此 start 与 limit 均不会溢出；limit 是合并排序
	// 所需的各源头部长度，而不是按用户页码直接预分配。
	start := (f.Page - 1) * f.PageSize
	pageLen := f.PageSize
	if remaining := total - start; remaining < pageLen {
		pageLen = remaining
	}
	limit := start + pageLen
	items := make([]ClientDistSecurityLogItem, 0, selectedSources*f.PageSize)
	for i, src := range sources {
		if f.Type != "" && f.Type != src.typ {
			continue
		}
		sourceLimit := limit
		if counts[i] < sourceLimit {
			sourceLimit = counts[i]
		}
		if sourceLimit == 0 {
			continue
		}
		rows, err := src.fn(f, sourceLimit)
		if err != nil {
			return nil, err
		}
		items = append(items, rows...)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })

	// COUNT 与取数之间可能有并发删改；再次按实际长度钳位，避免切片越界。
	if start > len(items) {
		start = len(items)
	}
	available := len(items) - start
	if pageLen > available {
		pageLen = available
	}
	end := start + pageLen
	return &ClientDistSecurityLogPage{Items: items[start:end], Total: total, Page: f.Page, PageSize: f.PageSize}, nil
}

// securityHelloQuery 是 hello 源的筛选后查询；取数与计数共用同一份条件（R38）。
func (s *ClientDistSecurityService) securityHelloQuery(f ClientDistSecurityLogFilter) *gorm.DB {
	db := s.db.Model(&model.ClientSecurityHello{})
	db = applySecurityErrCode(db, f, "err_code")
	db = applySecurityTimeWindow(db, f, "created_at")
	return applySecurityCommonFilter(db, f, "channel_id", "machine_id", "player_name", "ip")
}

func (s *ClientDistSecurityService) securityHelloLogs(f ClientDistSecurityLogFilter, limit int) ([]ClientDistSecurityLogItem, error) {
	var rows []model.ClientSecurityHello
	if err := s.securityHelloQuery(f).Order("created_at DESC").Limit(limit).Find(&rows).Error; err != nil {
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

// securityRiskQuery 是风险事件源的筛选后查询（R38：与计数共用）。
func (s *ClientDistSecurityService) securityRiskQuery(f ClientDistSecurityLogFilter) *gorm.DB {
	db := s.db.Model(&model.ClientSecurityRiskEvent{})
	// 风险事件的错误码维度就是规则码。
	db = applySecurityErrCode(db, f, "rule_code")
	db = applySecurityTimeWindow(db, f, "created_at")
	return applySecurityCommonFilter(db, f, "channel_id", "machine_id", "player_name", "ip")
}

func (s *ClientDistSecurityService) securityRiskLogs(f ClientDistSecurityLogFilter, limit int) ([]ClientDistSecurityLogItem, error) {
	var rows []model.ClientSecurityRiskEvent
	if err := s.securityRiskQuery(f).Order("created_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ClientDistSecurityLogItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, ClientDistSecurityLogItem{ID: logID("risk", r.ID), Type: "risk", Title: r.RuleCode, ChannelID: r.ChannelID, MachineID: r.MachineID, PlayerName: r.PlayerName, IP: r.IP, Status: r.Severity, ErrCode: r.RuleCode, CreatedAt: r.CreatedAt, Detail: map[string]any{"installId": r.InstallID, "subjectType": r.SubjectType, "subjectValue": r.SubjectValue, "keyPrefix": r.KeyPrefix, "reason": r.Reason, "detail": parseLogJSON(r.DetailJSON)}})
	}
	return out, nil
}

// securityActionQuery 是处置流水源的筛选后查询（R38：与计数共用）。
// 处置记录没有机器码/玩家名维度，命中这两项筛选时整源排除。
func (s *ClientDistSecurityService) securityActionQuery(f ClientDistSecurityLogFilter) *gorm.DB {
	db := s.db.Model(&model.ClientProtectionAction{})
	// 处置流水没有机器码/玩家名维度，命中这两项筛选时整源排除。
	if f.MachineID != "" || f.PlayerName != "" {
		return db.Where("1 = 0")
	}
	// 处置流水也没有错误码维度，必须显式排除（而不是静默忽略筛选）。
	db = applySecurityErrCode(db, f, "")
	db = applySecurityTimeWindow(db, f, "created_at")
	if f.ChannelID != "" {
		// 频道维度的语义是「本频道产生的处置，或目标值恰好是本频道的处置（频道保护记录）」。
		//
		// 这里显式写括号是**纵深防御，不是修一个正在发生的 bug**：GORM 会把每个 Where 子句
		// 各自包成一组（`WHERE (a) AND (b OR c)`），所以即使不写括号，AND/OR 优先级也不会被破坏——
		// 复审已实测确认这一点，原先「会退化成 channel_id = c OR (...)，IP 条件被整体绕过」的
		// 判断是误报。保留显式括号的理由只有一个：不让正确性依赖 ORM 的拼接实现细节
		// （升级 GORM、或有人把这些条件合并进同一个 Where 字符串时，语义就会静默改变）。
		db = db.Where("(channel_id = ? OR target_value = ?)", f.ChannelID, f.ChannelID)
	}
	if f.IP != "" {
		db = db.Where("(target_type = ? AND target_value = ?)", "ip", f.IP)
	}
	return db
}

func (s *ClientDistSecurityService) securityActionLogs(f ClientDistSecurityLogFilter, limit int) ([]ClientDistSecurityLogItem, error) {
	if f.MachineID != "" || f.PlayerName != "" {
		return []ClientDistSecurityLogItem{}, nil
	}
	var rows []model.ClientProtectionAction
	if err := s.securityActionQuery(f).Order("created_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ClientDistSecurityLogItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, ClientDistSecurityLogItem{ID: logID("action", r.ID), Type: "action", Title: r.Action, ChannelID: r.ChannelID, Status: r.Status, CreatedAt: r.CreatedAt, Detail: map[string]any{"targetType": r.TargetType, "targetValue": r.TargetValue, "reason": r.Reason, "auto": r.Auto, "expiresAt": r.ExpiresAt, "createdBy": r.CreatedBy}})
	}
	return out, nil
}

// securityRequestQuery 是请求明细源的筛选后查询（R38：与计数共用）。
func (s *ClientDistSecurityService) securityRequestQuery(f ClientDistSecurityLogFilter) *gorm.DB {
	db := s.db.Model(&model.ClientDistEvent{})
	db = applySecurityErrCode(db, f, "err_code")
	db = applySecurityTimeWindow(db, f, "created_at")
	return applySecurityCommonFilter(db, f, "channel_id", "machine_id", "", "ip")
}

func (s *ClientDistSecurityService) securityRequestLogs(f ClientDistSecurityLogFilter, limit int) ([]ClientDistSecurityLogItem, error) {
	var rows []model.ClientDistEvent
	if err := s.securityRequestQuery(f).Order("created_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ClientDistSecurityLogItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, ClientDistSecurityLogItem{ID: logID("request", r.ID), Type: "request", Title: r.Kind, ChannelID: r.ChannelID, MachineID: r.MachineID, IP: r.IP, Status: strconv.Itoa(r.Status), ErrCode: r.ErrCode, CreatedAt: r.CreatedAt, Detail: map[string]any{"version": r.Version, "artifactSha": r.ArtifactSHA, "bytes": r.Bytes, "method": r.Method, "path": r.Path, "errReason": r.ErrReason, "durationMs": r.DurationMs, "etag": r.ETag, "requestHeaders": parseLogJSON(r.RequestHeadersJSON), "responseHeaders": parseLogJSON(r.ResponseHeadersJSON)}})
	}
	return out, nil
}

// securityRuntimeQuery 是运行态快照源的筛选后查询（R38：与计数共用）。
func (s *ClientDistSecurityService) securityRuntimeQuery(f ClientDistSecurityLogFilter) *gorm.DB {
	db := s.db.Model(&model.ClientRuntimeState{})
	// 运行态快照没有错误码维度，必须显式排除（否则带 errCode 筛选时会把整片运行态混进结果）。
	db = applySecurityErrCode(db, f, "")
	// 运行态是快照表，时间列不是 created_at，时间窗按 last_heartbeat_at 下推（R31）。
	db = applySecurityTimeWindow(db, f, "last_heartbeat_at")
	return applySecurityCommonFilter(db, f, "channel_id", "machine_id", "player_name", "ip")
}

func (s *ClientDistSecurityService) securityRuntimeLogs(f ClientDistSecurityLogFilter, limit int) ([]ClientDistSecurityLogItem, error) {
	var rows []model.ClientRuntimeState
	if err := s.securityRuntimeQuery(f).Order("last_heartbeat_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ClientDistSecurityLogItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, ClientDistSecurityLogItem{ID: logID("runtime", r.ID), Type: "runtime", Title: "运行态心跳", ChannelID: r.ChannelID, MachineID: r.MachineID, PlayerName: r.PlayerName, IP: r.IP, Status: r.LastUpdateResult, CreatedAt: r.LastHeartbeatAt, Detail: map[string]any{"platform": r.Platform, "javaVersion": r.JavaVersion, "launcher": r.Launcher, "coreVersion": r.CoreVersion, "localVersion": r.LocalVersion, "firstSeenAt": r.FirstSeenAt, "lastUpdateAt": r.LastUpdateAt}})
	}
	return out, nil
}

// securityTelemetryQuery 是更新遥测源的筛选后查询（R38：与计数共用）。
func (s *ClientDistSecurityService) securityTelemetryQuery(f ClientDistSecurityLogFilter) *gorm.DB {
	db := s.db.Model(&model.ClientTelemetry{})
	// 遥测表的错误列叫 error。
	db = applySecurityErrCode(db, f, "error")
	db = applySecurityTimeWindow(db, f, "created_at")
	return applySecurityCommonFilter(db, f, "channel_id", "machine_id", "player_name", "ip")
}

func (s *ClientDistSecurityService) securityTelemetryLogs(f ClientDistSecurityLogFilter, limit int) ([]ClientDistSecurityLogItem, error) {
	var rows []model.ClientTelemetry
	if err := s.securityTelemetryQuery(f).Order("created_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ClientDistSecurityLogItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, ClientDistSecurityLogItem{ID: logID("telemetry", r.ID), Type: "telemetry", Title: "更新遥测", ChannelID: r.ChannelID, MachineID: r.MachineID, PlayerName: r.PlayerName, IP: r.IP, Status: r.Result, ErrCode: r.Error, CreatedAt: r.CreatedAt, Detail: map[string]any{
			"fromVersion": r.FromVersion, "toVersion": r.ToVersion, "coreVersion": r.CoreVersion,
			"os": r.OS, "arch": r.Arch, "javaVersion": r.JavaVersion, "javaVendor": r.JavaVendor,
			"launcher": r.Launcher, "locale": r.Locale, "timezone": r.Timezone, "memoryTier": r.MemoryTier,
			"durationMs": r.DurationMs, "bootSuccess": r.BootSuccess,
			// 增补：安装 ID 与脱敏后的完整启动参数（--username 即玩家登录名）。
			"installId": r.InstallID, "launchArgs": r.LaunchArgs,
		}})
	}
	return out, nil
}

// applySecurityCommonFilter 只做维度筛选；时间窗由各源按自己的时间列单独下推
// （运行态是快照表，时间列是 last_heartbeat_at，不能统一用 created_at）。
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

// applySecurityErrCode 把错误码筛下推到单个日志源（R36）。
//
// 各源的「错误码」列不同（hello/request 是 err_code，risk 是 rule_code，telemetry 是 error），
// 处置流水与运行态没有这一维度，由调用方传空列名表示「本类型不参与该筛」。
// 空列名 + 非空筛值 = 该源整体排除（返回 1=0）：静默忽略会让用户以为筛选生效却看到无关行。
func applySecurityErrCode(db *gorm.DB, f ClientDistSecurityLogFilter, errCol string) *gorm.DB {
	if f.ErrCode == "" {
		return db
	}
	if errCol == "" {
		return db.Where("1 = 0")
	}
	return db.Where(errCol+" = ?", f.ErrCode)
}

// applySecurityTimeWindow 把时间窗下推到单个日志源（R31）。
//
// 各源的时间列不同（明细/安全表用 created_at，运行态快照表用 last_heartbeat_at），
// 所以列名由调用方给。窗口为空即不约束（兼容不带时间参数的旧请求）。
func applySecurityTimeWindow(db *gorm.DB, f ClientDistSecurityLogFilter, timeCol string) *gorm.DB {
	if timeCol == "" {
		return db
	}
	if f.From != nil {
		db = db.Where(timeCol+" >= ?", *f.From)
	}
	if f.To != nil {
		db = db.Where(timeCol+" <= ?", *f.To)
	}
	return db
}

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
	// 关联密钥数（R39）：事件明细不含 key_id，权威来源是安全画像的 last_ip → key_id。
	// 此前 KeyCount 从未赋值，界面上永远显示 0——一个用了 5 把密钥的 IP 和只试过一次的 IP 看起来一样。
	keyCounts, err := s.ipKeyCounts(rawRows)
	if err != nil {
		return nil, err
	}
	rows := make([]ClientDistIPAnalysis, 0, len(rawRows))
	for _, row := range rawRows {
		out := ClientDistIPAnalysis{
			IP: row.IP, RequestCount: row.RequestCount, RejectCount: row.RejectCount,
			InvalidKeyCount: row.InvalidKeyCount, NotFoundCount: row.NotFoundCount, RangeCount: row.RangeCount,
			DownloadBytes: row.DownloadBytes, KeyCount: keyCounts[row.IP], ChannelCount: row.ChannelCount,
			LastSeen:  parseSecurityAggregateTime(row.LastSeen),
			RiskScore: row.RejectCount + row.RangeCount, Blocked: blocked[row.IP],
		}
		rows = append(rows, out)
	}
	return rows, nil
}

// ipKeyCounts 按 IP 统计「见过的不同密钥数」（R39）。只查本页涉及的 IP，不做全表聚合。
func (s *ClientDistSecurityService) ipKeyCounts(rawRows []ipAnalysisRow) (map[string]int64, error) {
	out := make(map[string]int64, len(rawRows))
	if len(rawRows) == 0 {
		return out, nil
	}
	ips := make([]string, 0, len(rawRows))
	for _, row := range rawRows {
		if row.IP != "" {
			ips = append(ips, row.IP)
		}
	}
	if len(ips) == 0 {
		return out, nil
	}
	type ipKeyRow struct {
		IP       string
		KeyCount int64
	}
	var raws []ipKeyRow
	if err := s.db.Model(&model.ClientSecurityProfile{}).
		Select("last_ip AS ip, COUNT(DISTINCT key_id) AS key_count").
		Where("last_ip IN ?", ips).Group("last_ip").Scan(&raws).Error; err != nil {
		return nil, err
	}
	for _, r := range raws {
		out[r.IP] = r.KeyCount
	}
	return out, nil
}
func (s *ClientDistSecurityService) ListPlayerAnalysis(limit int) ([]ClientDistPlayerAnalysis, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	var rawRows []playerAnalysisRow
	if err := s.db.Model(&model.ClientSecurityProfile{}).
		Select("player_name, COUNT(DISTINCT install_id) AS install_count, COUNT(DISTINCT machine_id) AS machine_count, COUNT(DISTINCT last_ip) AS ip_count, COUNT(DISTINCT key_id) AS key_count, COUNT(DISTINCT channel_id) AS channel_count, COALESCE(SUM(risk_score), 0) AS risk_score, MAX(last_seen) AS last_seen").
		Where("player_name != ''").Group("player_name").Order("install_count DESC").Limit(limit).Scan(&rawRows).Error; err != nil {
		return nil, err
	}
	// 下载量与异常请求数（R39）：此前从未赋值，界面上永远显示 0 与 0 B。
	// 画像表没有字节维度，权威来源是分发明细（按 player_name + 频道 + 机器码关联）：
	// 画像的 (channel_id, machine_id) 是稳定身份，明细每行的 bytes/status 就是该玩家的实际行为。
	downloads, abnormals, err := s.playerTraffic(rawRows)
	if err != nil {
		return nil, err
	}
	rows := make([]ClientDistPlayerAnalysis, 0, len(rawRows))
	for _, row := range rawRows {
		rows = append(rows, ClientDistPlayerAnalysis{
			PlayerName: row.PlayerName, InstallCount: row.InstallCount, MachineCount: row.MachineCount,
			IPCount: row.IPCount, KeyCount: row.KeyCount, ChannelCount: row.ChannelCount,
			DownloadBytes: downloads[row.PlayerName], AbnormalRequests: abnormals[row.PlayerName],
			RiskScore: row.RiskScore, LastSeen: parseSecurityAggregateTime(row.LastSeen),
		})
	}
	return rows, nil
}

// playerAnalysisRow 是玩家剖析的聚合中间行（画像表聚合结果，再补流量维度）。
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

// playerTraffic 按玩家名汇总下载字节数与异常请求数（R39）。
//
// 关联路径：画像给出该玩家名对应的 (channel_id, machine_id) 身份集合，再用同一套复合键
// 去分发明细里取 bytes 之和与 status>=400 的条数。只查本页玩家，不做全表聚合；
// 机器码属于同一玩家但跨频道重复计数是可接受的——那本来就是两条独立的客户端身份。
func (s *ClientDistSecurityService) playerTraffic(rawRows []playerAnalysisRow) (map[string]int64, map[string]int64, error) {
	downloads := make(map[string]int64, len(rawRows))
	abnormals := make(map[string]int64, len(rawRows))
	if len(rawRows) == 0 {
		return downloads, abnormals, nil
	}
	names := make([]string, 0, len(rawRows))
	for _, row := range rawRows {
		if row.PlayerName != "" {
			names = append(names, row.PlayerName)
		}
	}
	if len(names) == 0 {
		return downloads, abnormals, nil
	}
	var profiles []model.ClientSecurityProfile
	if err := s.db.Select("player_name, channel_id, machine_id").
		Where("player_name IN ? AND machine_id <> ''", names).Find(&profiles).Error; err != nil {
		return nil, nil, err
	}
	if len(profiles) == 0 {
		return downloads, abnormals, nil
	}
	// 同一 (channel, machine) 可能对应多个画像行（不同 installId），去重后作为身份集合。
	type identity struct {
		playerName string
		channelID  string
		machineID  string
	}
	seen := make(map[identity]struct{}, len(profiles))
	identities := make([]identity, 0, len(profiles))
	for _, p := range profiles {
		key := identity{playerName: p.PlayerName, channelID: p.ChannelID, machineID: p.MachineID}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		identities = append(identities, key)
	}

	type aggRow struct {
		ChannelID string
		MachineID string
		Bytes     int64
		Abnormals int64
	}
	var raws []aggRow
	q := s.db.Model(&model.ClientDistEvent{}).
		Select("channel_id, machine_id, COALESCE(SUM(bytes), 0) AS bytes, COALESCE(SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END), 0) AS abnormals")
	// 用 (channel_id, machine_id) 复合键收敛，避免同名机器码跨频道串味（与 R33 同口径）。
	conds := make([]string, 0, len(identities))
	args := make([]any, 0, len(identities)*2)
	for _, id := range identities {
		conds = append(conds, "(channel_id = ? AND machine_id = ?)")
		args = append(args, id.channelID, id.machineID)
	}
	q = q.Where(strings.Join(conds, " OR "), args...).Group("channel_id, machine_id")
	if err := q.Scan(&raws).Error; err != nil {
		return nil, nil, err
	}
	byIdentity := make(map[[2]string]aggRow, len(raws))
	for _, r := range raws {
		byIdentity[[2]string{r.ChannelID, r.MachineID}] = r
	}
	for _, id := range identities {
		row, ok := byIdentity[[2]string{id.channelID, id.machineID}]
		if !ok {
			continue
		}
		downloads[id.playerName] += row.Bytes
		abnormals[id.playerName] += row.Abnormals
	}
	return downloads, abnormals, nil
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
		if f.Patch != nil && f.Patch.Artifact.SHA256 == sha {
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
