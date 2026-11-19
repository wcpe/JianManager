package service

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 设备授权流程常量（RFC 8628）。
const (
	// DeviceCodeTTL 设备码有效期：用户需要足够时间完成浏览器操作。
	DeviceCodeTTL = 10 * time.Minute
	// DevicePollInterval 建议轮询间隔；快于此值返回 slow_down（RFC 8628 §3.5）。
	DevicePollInterval = 5 * time.Second
)

// 设备授权错误（对外映射为 RFC 8628 的 error 字段）。
var (
	ErrDeviceCodeNotFound = errors.New("设备码不存在")
	ErrDeviceAuthorizationPending = errors.New("authorization_pending")
	ErrDeviceSlowDown             = errors.New("slow_down")
	ErrDeviceAccessDenied         = errors.New("access_denied")
	ErrDeviceCodeExpired          = errors.New("expired_token")
	ErrDeviceCodeConsumed         = errors.New("设备码已被使用")
)

// DeviceFlowService 设备授权流程服务。
type DeviceFlowService struct {
	db       *gorm.DB
	userTok  *UserTokenService
	// pollGuard 记录每个 device_code 的上次轮询时间，用于 slow_down 判定。
	// 进程内 map 足够：设备码生命周期只有 10 分钟，且 CP 单进程。
	pollGuard map[string]time.Time
}

// NewDeviceFlowService 创建设备授权服务。
func NewDeviceFlowService(db *gorm.DB, userTok *UserTokenService) *DeviceFlowService {
	return &DeviceFlowService{db: db, userTok: userTok, pollGuard: map[string]time.Time{}}
}

// DeviceCodeResult /oauth/device/code 的响应体。
type DeviceCodeResult struct {
	DeviceCode      string `json:"deviceCode"`
	UserCode        string `json:"userCode"`
	VerificationURI string `json:"verificationUri"`
	ExpiresIn       int    `json:"expiresIn"`
	Interval        int    `json:"interval"`
}

// Start 生成一组设备码与用户码。
//
// verificationURI 由调用方（router）按 CP 对外地址拼装，service 不假设部署形态。
func (s *DeviceFlowService) Start(deviceLabel, verificationURI string) (*DeviceCodeResult, error) {
	deviceCode, err := randomURLSafe(32)
	if err != nil {
		return nil, err
	}
	userCode, err := randomUserCode()
	if err != nil {
		return nil, err
	}

	rec := &model.DeviceCode{
		DeviceCode:  deviceCode,
		UserCode:    userCode,
		DeviceLabel: deviceLabel,
		Status:      model.DeviceCodePending,
		ExpiresAt:   time.Now().Add(DeviceCodeTTL),
	}
	if err := s.db.Create(rec).Error; err != nil {
		return nil, fmt.Errorf("保存设备码失败: %w", err)
	}

	return &DeviceCodeResult{
		DeviceCode:      deviceCode,
		UserCode:        userCode,
		VerificationURI: verificationURI,
		ExpiresIn:       int(DeviceCodeTTL.Seconds()),
		Interval:        int(DevicePollInterval.Seconds()),
	}, nil
}

// LookupByUserCode 按用户码查待确认记录（授权页用）。
func (s *DeviceFlowService) LookupByUserCode(userCode string) (*model.DeviceCode, error) {
	code := normalizeUserCode(userCode)
	var rec model.DeviceCode
	if err := s.db.Where("user_code = ?", code).First(&rec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDeviceCodeNotFound
		}
		return nil, fmt.Errorf("查询设备码失败: %w", err)
	}
	return &rec, nil
}

// Approve 用户在浏览器确认授权，签发 user_token 并把明文暂存待 CLI 取走。
func (s *DeviceFlowService) Approve(userCode string, userID uint) error {
	rec, err := s.LookupByUserCode(userCode)
	if err != nil {
		return err
	}
	if !time.Now().Before(rec.ExpiresAt) {
		s.markStatus(rec.ID, model.DeviceCodeExpired)
		return ErrDeviceCodeExpired
	}
	if rec.Status != model.DeviceCodePending {
		// 重复确认/已消费统一视为不可用。
		return ErrDeviceCodeConsumed
	}

	res, err := s.userTok.Create(userID, CreateUserTokenRequest{
		Name:        deviceLabelOrDefault(rec.DeviceLabel),
		DeviceLabel: rec.DeviceLabel,
	})
	if err != nil {
		return err
	}

	// 明文暂存于本记录，待 CLI 取走一次后清空。这是唯一允许短暂保留明文的位置。
	if err := s.db.Model(&model.DeviceCode{}).Where("id = ?", rec.ID).Updates(map[string]any{
		"status":    model.DeviceCodeApproved,
		"user_id":   userID,
		"token_id":  res.Token.ID,
		"plaintext": res.Plaintext,
	}).Error; err != nil {
		return fmt.Errorf("更新设备码状态失败: %w", err)
	}
	return nil
}

// Deny 用户拒绝授权。
func (s *DeviceFlowService) Deny(userCode string) error {
	rec, err := s.LookupByUserCode(userCode)
	if err != nil {
		return err
	}
	if rec.Status != model.DeviceCodePending {
		return nil
	}
	s.markStatus(rec.ID, model.DeviceCodeDenied)
	return nil
}

// Poll 是 CLI 的轮询入口：返回明文（一次性）或 RFC 8628 定义的状态错误。
//
// 返回的第二个值是明文；仅 approved 且首次取走时非空。
func (s *DeviceFlowService) Poll(deviceCode string) (string, error) {
	var rec model.DeviceCode
	if err := s.db.Where("device_code = ?", deviceCode).First(&rec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrDeviceCodeNotFound
		}
		return "", fmt.Errorf("查询设备码失败: %w", err)
	}

	// 轮询过频 → slow_down（RFC 8628 §3.5）。
	if last, ok := s.pollGuard[deviceCode]; ok && time.Since(last) < DevicePollInterval {
		return "", ErrDeviceSlowDown
	}
	s.pollGuard[deviceCode] = time.Now()

	switch rec.Status {
	case model.DeviceCodePending:
		if !time.Now().Before(rec.ExpiresAt) {
			s.markStatus(rec.ID, model.DeviceCodeExpired)
			return "", ErrDeviceCodeExpired
		}
		return "", ErrDeviceAuthorizationPending

	case model.DeviceCodeDenied:
		return "", ErrDeviceAccessDenied

	case model.DeviceCodeExpired:
		return "", ErrDeviceCodeExpired

	case model.DeviceCodeConsumed:
		return "", ErrDeviceCodeConsumed

	case model.DeviceCodeApproved:
		if !time.Now().Before(rec.ExpiresAt) {
			s.markStatus(rec.ID, model.DeviceCodeExpired)
			return "", ErrDeviceCodeExpired
		}
		if rec.Plaintext == "" {
			return "", ErrDeviceCodeConsumed
		}
		plaintext := rec.Plaintext
		// 取走即焚：状态转 consumed 并清空明文，保证只能取一次。
		now := time.Now()
		if err := s.db.Model(&model.DeviceCode{}).Where("id = ?", rec.ID).Updates(map[string]any{
			"status":      model.DeviceCodeConsumed,
			"plaintext":   "",
			"consumed_at": now,
		}).Error; err != nil {
			return "", fmt.Errorf("消费设备码失败: %w", err)
		}
		delete(s.pollGuard, deviceCode)
		return plaintext, nil

	default:
		return "", ErrDeviceCodeNotFound
	}
}

// markStatus 更新状态；失败忽略（后续轮询会再次纠正）。
func (s *DeviceFlowService) markStatus(id uint, status string) {
	_ = s.db.Model(&model.DeviceCode{}).Where("id = ?", id).Update("status", status).Error
}

// randomUserCode 生成 8 位易读码，格式 XXXX-XXXX。
// 去掉 0/O/1/I/L 等易混字符（RFC 8628 §6.1 建议）。
func randomUserCode() (string, error) {
	const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	var sb strings.Builder
	for i := 0; i < 8; i++ {
		if i == 4 {
			sb.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", fmt.Errorf("生成用户码失败: %w", err)
		}
		sb.WriteByte(alphabet[n.Int64()])
	}
	return sb.String(), nil
}

// normalizeUserCode 归一化用户输入：去空格、转大写、补连字符。
func normalizeUserCode(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	if len(s) == 8 {
		return s[:4] + "-" + s[4:]
	}
	return s
}

// randomURLSafe 生成 URL 安全的随机串。
func randomURLSafe(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成随机串失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// deviceLabelOrDefault 把设备标识转成令牌名称。
func deviceLabelOrDefault(label string) string {
	if strings.TrimSpace(label) == "" {
		return "命令行登录"
	}
	return "设备：" + label
}
