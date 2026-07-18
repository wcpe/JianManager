package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	botLoadPlanTokenTTL    = time.Minute
	botLoadPlanTokenDomain = "jianmanager/bot-load/plan-token/v1"
)

// DeriveBotLoadPlanTokenSecret 从稳定服务端主密钥域分离派生计划令牌专用密钥。
func DeriveBotLoadPlanTokenSecret(master []byte) []byte {
	if len(master) == 0 {
		return nil
	}
	mac := hmac.New(sha256.New, master)
	_, _ = mac.Write([]byte(botLoadPlanTokenDomain))
	return mac.Sum(nil)
}

type botLoadPlanTokenClaims struct {
	Version             int                     `json:"version"`
	RunID               uint                    `json:"runId"`
	AllocationHash      string                  `json:"allocationHash"`
	CapacityGenerations []BotLoadNodeGeneration `json:"capacityGenerations"`
	ExpiresAtUnixNano   int64                   `json:"expiresAtUnixNano"`
}

// BotLoadPlanTokenExpectation 是 start 层从服务端计划和即时容量世代构造的校验输入。
type BotLoadPlanTokenExpectation struct {
	RunID               uint
	AllocationHash      string
	CapacityGenerations []BotLoadNodeGeneration
}

// BotLoadPlanTokenSigner 使用注入密钥签发 HMAC-SHA256 短期计划标识。
type BotLoadPlanTokenSigner struct {
	secret []byte
	clock  BotLoadClock
	ttl    time.Duration
}

// NewBotLoadPlanTokenSigner 创建计划令牌签名器；生产密钥必须由装配层注入。
func NewBotLoadPlanTokenSigner(secret []byte, clock BotLoadClock) (*BotLoadPlanTokenSigner, error) {
	if len(secret) == 0 {
		return nil, fmt.Errorf("计划令牌签名密钥不能为空")
	}
	return &BotLoadPlanTokenSigner{
		secret: append([]byte(nil), secret...), clock: normalizeBotLoadClock(clock), ttl: botLoadPlanTokenTTL,
	}, nil
}

// Issue 签发默认 60 秒有效的令牌，正文只包含 run/hash/generation/expiry。
func (s *BotLoadPlanTokenSigner) Issue(runID uint, allocationHash string, generations []BotLoadNodeGeneration) (string, time.Time, error) {
	if runID == 0 || strings.TrimSpace(allocationHash) == "" {
		return "", time.Time{}, ErrBotLoadPreflightInvalid
	}
	expiresAt := s.clock.Now().UTC().Add(s.ttl)
	claims := botLoadPlanTokenClaims{
		Version: 1, RunID: runID, AllocationHash: allocationHash,
		CapacityGenerations: canonicalBotLoadGenerations(generations), ExpiresAtUnixNano: expiresAt.UnixNano(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("序列化计划令牌失败: %w", err)
	}
	signature := s.sign(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature), expiresAt, nil
}

// Verify 常量时间校验签名/hash，并把所有失效形态归一为容量变化类错误。
func (s *BotLoadPlanTokenSigner) Verify(token string, expected BotLoadPlanTokenExpectation) error {
	claims, err := s.parseAndAuthenticate(token)
	if err != nil {
		return err
	}
	if claims.Version != 1 || claims.RunID != expected.RunID {
		return newBotLoadCapacityChanged("计划令牌与当前运行不匹配")
	}
	if !constantTimeStringEqual(claims.AllocationHash, expected.AllocationHash) {
		return newBotLoadCapacityChanged("服务端分片计划已变化")
	}
	if !equalBotLoadGenerations(claims.CapacityGenerations, expected.CapacityGenerations) {
		return newBotLoadCapacityChanged("发压节点容量世代已变化")
	}
	expiresAt := time.Unix(0, claims.ExpiresAtUnixNano)
	if !s.clock.Now().Before(expiresAt) {
		return newBotLoadCapacityChanged("计划令牌已过期")
	}
	return nil
}

func (s *BotLoadPlanTokenSigner) parseAndAuthenticate(token string) (*botLoadPlanTokenClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, newBotLoadCapacityChanged("计划令牌格式无效")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, newBotLoadCapacityChanged("计划令牌格式无效")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, s.sign(payload)) {
		return nil, newBotLoadCapacityChanged("计划令牌签名无效")
	}
	var claims botLoadPlanTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, newBotLoadCapacityChanged("计划令牌正文无效")
	}
	return &claims, nil
}

func (s *BotLoadPlanTokenSigner) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func canonicalBotLoadGenerations(generations []BotLoadNodeGeneration) []BotLoadNodeGeneration {
	out := append([]BotLoadNodeGeneration(nil), generations...)
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

func equalBotLoadGenerations(left, right []BotLoadNodeGeneration) bool {
	leftJSON, leftErr := json.Marshal(canonicalBotLoadGenerations(left))
	rightJSON, rightErr := json.Marshal(canonicalBotLoadGenerations(right))
	if leftErr != nil || rightErr != nil {
		return false
	}
	return hmac.Equal(leftJSON, rightJSON)
}

func constantTimeStringEqual(left, right string) bool {
	return hmac.Equal([]byte(left), []byte(right))
}
