package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
)

// 负载曲线与阈值相关错误码。
const (
	BotLoadProfileInvalidCode    = "BOT_LOAD_PROFILE_INVALID"
	BotLoadThresholdsInvalidCode = "BOT_LOAD_THRESHOLDS_INVALID"
	BotLoadTemplateNameConflict  = "BOT_LOAD_TEMPLATE_NAME_CONFLICT"
	BotLoadReportNotReadyCode    = "BOT_LOAD_REPORT_NOT_READY"
)

var (
	// ErrBotLoadProfileInvalid 负载曲线校验失败。
	ErrBotLoadProfileInvalid = errors.New("Bot 负载曲线无效")
	// ErrBotLoadThresholdsInvalid 阈值校验失败。
	ErrBotLoadThresholdsInvalid = errors.New("Bot 负载阈值无效")
	// ErrBotLoadTemplateNameConflict 活跃模板名称冲突。
	ErrBotLoadTemplateNameConflict = errors.New("Bot 负载模板名称冲突")
	// ErrBotLoadReportNotReady 运行未终态，报告不可导出。
	ErrBotLoadReportNotReady = errors.New("Bot 负载报告尚未就绪")
	// ErrBotLoadTemplateNotFound 模板不存在或无权访问。
	ErrBotLoadTemplateNotFound = errors.New("Bot 负载模板不存在")
)

var botLoadBarrierKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// BotLoadProfileStable 固定并发负载。
type BotLoadProfileStable struct {
	Type            string `json:"type"`
	TargetBots      int    `json:"targetBots"`
	RampUpSeconds   int    `json:"rampUpSeconds"`
	DurationSeconds int    `json:"durationSeconds"`
}

// BotLoadProfileStepStage 阶梯升压的一级。
type BotLoadProfileStepStage struct {
	TargetBots  int `json:"targetBots"`
	HoldSeconds int `json:"holdSeconds"`
}

// BotLoadProfileStep 阶梯升压负载。
type BotLoadProfileStep struct {
	Type                   string                   `json:"type"`
	Stages                 []BotLoadProfileStepStage `json:"stages"`
	StopOnThresholdFailure bool                     `json:"stopOnThresholdFailure"`
}

// BotLoadProfileSpikeBarrier spike 可选屏障。
type BotLoadProfileSpikeBarrier struct {
	Key             string `json:"key"`
	ReleaseWindowMS int    `json:"releaseWindowMs"`
}

// BotLoadProfileSpike 突发洪峰负载。
type BotLoadProfileSpike struct {
	Type                 string                     `json:"type"`
	TargetBots           int                        `json:"targetBots"`
	ConnectWindowSeconds int                        `json:"connectWindowSeconds"`
	Barrier              *BotLoadProfileSpikeBarrier `json:"barrier,omitempty"`
	HoldSeconds          int                        `json:"holdSeconds"`
}

// BotLoadProfile 规范化后的负载曲线联合类型。
type BotLoadProfile struct {
	Type string `json:"type"`
	// 以下字段按 type 选择性填充；序列化时使用 MarshalJSON 输出规范联合。
	Stable *BotLoadProfileStable `json:"-"`
	Step   *BotLoadProfileStep   `json:"-"`
	Spike  *BotLoadProfileSpike  `json:"-"`
}

// MarshalJSON 输出 API 联合形态。
func (p BotLoadProfile) MarshalJSON() ([]byte, error) {
	switch p.Type {
	case "stable":
		if p.Stable == nil {
			return nil, fmt.Errorf("%w: stable 未填充", ErrBotLoadProfileInvalid)
		}
		return json.Marshal(p.Stable)
	case "step":
		if p.Step == nil {
			return nil, fmt.Errorf("%w: step 未填充", ErrBotLoadProfileInvalid)
		}
		return json.Marshal(p.Step)
	case "spike":
		if p.Spike == nil {
			return nil, fmt.Errorf("%w: spike 未填充", ErrBotLoadProfileInvalid)
		}
		return json.Marshal(p.Spike)
	default:
		return nil, fmt.Errorf("%w: 未知 type %q", ErrBotLoadProfileInvalid, p.Type)
	}
}

// UnmarshalJSON 解析 API 联合形态。
func (p *BotLoadProfile) UnmarshalJSON(data []byte) error {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("%w: %v", ErrBotLoadProfileInvalid, err)
	}
	switch head.Type {
	case "stable":
		var v BotLoadProfileStable
		if err := json.Unmarshal(data, &v); err != nil {
			return fmt.Errorf("%w: %v", ErrBotLoadProfileInvalid, err)
		}
		v.Type = "stable"
		*p = BotLoadProfile{Type: "stable", Stable: &v}
	case "step":
		var v BotLoadProfileStep
		if err := json.Unmarshal(data, &v); err != nil {
			return fmt.Errorf("%w: %v", ErrBotLoadProfileInvalid, err)
		}
		v.Type = "step"
		*p = BotLoadProfile{Type: "step", Step: &v}
	case "spike":
		var v BotLoadProfileSpike
		if err := json.Unmarshal(data, &v); err != nil {
			return fmt.Errorf("%w: %v", ErrBotLoadProfileInvalid, err)
		}
		v.Type = "spike"
		*p = BotLoadProfile{Type: "spike", Spike: &v}
	default:
		return fmt.Errorf("%w: type 必须为 stable|step|spike", ErrBotLoadProfileInvalid)
	}
	return nil
}

// BotLoadThresholdsSafety 安全停止阈值。
type BotLoadThresholdsSafety struct {
	MaxExecutorMemoryRate float64 `json:"maxExecutorMemoryRate"`
	MaxEventLoopP95MS     int     `json:"maxEventLoopP95Ms"`
	SustainSeconds        int     `json:"sustainSeconds"`
}

// BotLoadThresholdsLegacy 可选 legacy 附加判定。
type BotLoadThresholdsLegacy struct {
	Enabled                    bool     `json:"enabled"`
	MinTPS                     *float64 `json:"minTps,omitempty"`
	MaxMsptP95                 *float64 `json:"maxMsptP95,omitempty"`
	RequireBusinessObservation *bool    `json:"requireBusinessObservation,omitempty"`
}

// BotLoadThresholds 运行阈值。
type BotLoadThresholds struct {
	MinOnlineRate             float64                  `json:"minOnlineRate"`
	MinCommandSentRate        float64                  `json:"minCommandSentRate"`
	MinScheduleCompletionRate float64                  `json:"minScheduleCompletionRate"`
	MinWorkerHealthRate       float64                  `json:"minWorkerHealthRate"`
	MinBarrierArrivalRate     float64                  `json:"minBarrierArrivalRate"`
	MaxScheduleLagP95MS       int                      `json:"maxScheduleLagP95Ms"`
	MaxProcessCrashes         int                      `json:"maxProcessCrashes"`
	Safety                    *BotLoadThresholdsSafety `json:"safety,omitempty"`
	Legacy                    *BotLoadThresholdsLegacy `json:"legacy,omitempty"`
}

// DefaultBotLoadThresholds 返回规格默认阈值。
func DefaultBotLoadThresholds() BotLoadThresholds {
	return BotLoadThresholds{
		MinOnlineRate:             0.99,
		MinCommandSentRate:        0.99,
		MinScheduleCompletionRate: 0.99,
		MinWorkerHealthRate:       0.99,
		MinBarrierArrivalRate:     0.99,
		MaxScheduleLagP95MS:       1000,
		MaxProcessCrashes:         0,
		Safety: &BotLoadThresholdsSafety{
			MaxExecutorMemoryRate: 0.85,
			MaxEventLoopP95MS:     500,
			SustainSeconds:        30,
		},
	}
}

// NormalizeAndValidateLoadProfile 校验并规范化 load profile。
func NormalizeAndValidateLoadProfile(raw json.RawMessage) (*BotLoadProfile, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: loadProfile 必填", ErrBotLoadProfileInvalid)
	}
	var p BotLoadProfile
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if err := validateLoadProfile(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

func validateLoadProfile(p *BotLoadProfile) error {
	switch p.Type {
	case "stable":
		s := p.Stable
		if s == nil {
			return fmt.Errorf("%w: stable 内容缺失", ErrBotLoadProfileInvalid)
		}
		if !isIntInRange(s.TargetBots, 1, 12800) {
			return fmt.Errorf("%w: targetBots 必须为 1..12800 整数", ErrBotLoadProfileInvalid)
		}
		if !isIntInRange(s.RampUpSeconds, 0, 86400) {
			return fmt.Errorf("%w: rampUpSeconds 必须为 0..86400 整数", ErrBotLoadProfileInvalid)
		}
		if !isIntInRange(s.DurationSeconds, 10, 86400) {
			return fmt.Errorf("%w: durationSeconds 必须为 10..86400 整数", ErrBotLoadProfileInvalid)
		}
		if estimatedProfileDurationSeconds(p) > 604800 {
			return fmt.Errorf("%w: 预计总时长不得超过 604800 秒", ErrBotLoadProfileInvalid)
		}
	case "step":
		s := p.Step
		if s == nil {
			return fmt.Errorf("%w: step 内容缺失", ErrBotLoadProfileInvalid)
		}
		if len(s.Stages) < 1 || len(s.Stages) > 64 {
			return fmt.Errorf("%w: stages 必须为 1..64 项", ErrBotLoadProfileInvalid)
		}
		holdSum := 0
		prev := 0
		for i, st := range s.Stages {
			if !isIntInRange(st.TargetBots, 1, 12800) {
				return fmt.Errorf("%w: stages[%d].targetBots 必须为 1..12800 整数", ErrBotLoadProfileInvalid, i)
			}
			if st.TargetBots <= prev {
				return fmt.Errorf("%w: stages[%d].targetBots 必须严格递增", ErrBotLoadProfileInvalid, i)
			}
			if !isIntInRange(st.HoldSeconds, 10, 86400) {
				return fmt.Errorf("%w: stages[%d].holdSeconds 必须为 10..86400 整数", ErrBotLoadProfileInvalid, i)
			}
			holdSum += st.HoldSeconds
			prev = st.TargetBots
		}
		if holdSum > 604800 {
			return fmt.Errorf("%w: holdSeconds 总和不得超过 604800", ErrBotLoadProfileInvalid)
		}
	case "spike":
		s := p.Spike
		if s == nil {
			return fmt.Errorf("%w: spike 内容缺失", ErrBotLoadProfileInvalid)
		}
		if !isIntInRange(s.TargetBots, 1, 12800) {
			return fmt.Errorf("%w: targetBots 必须为 1..12800 整数", ErrBotLoadProfileInvalid)
		}
		if !isIntInRange(s.ConnectWindowSeconds, 1, 3600) {
			return fmt.Errorf("%w: connectWindowSeconds 必须为 1..3600 整数", ErrBotLoadProfileInvalid)
		}
		if !isIntInRange(s.HoldSeconds, 10, 86400) {
			return fmt.Errorf("%w: holdSeconds 必须为 10..86400 整数", ErrBotLoadProfileInvalid)
		}
		if s.Barrier != nil {
			key := strings.TrimSpace(s.Barrier.Key)
			if len(key) < 1 || len(key) > 64 || !botLoadBarrierKeyPattern.MatchString(key) {
				return fmt.Errorf("%w: barrier.key 长度 1..64 且匹配 [A-Za-z0-9._-]+", ErrBotLoadProfileInvalid)
			}
			s.Barrier.Key = key
			if !isIntInRange(s.Barrier.ReleaseWindowMS, 1, 60000) {
				return fmt.Errorf("%w: barrier.releaseWindowMs 必须为 1..60000 整数", ErrBotLoadProfileInvalid)
			}
		}
		if estimatedProfileDurationSeconds(p) > 604800 {
			return fmt.Errorf("%w: 预计总时长不得超过 604800 秒", ErrBotLoadProfileInvalid)
		}
	default:
		return fmt.Errorf("%w: type 必须为 stable|step|spike", ErrBotLoadProfileInvalid)
	}
	return nil
}

// ProfileMaxTargetBots 返回 profile 最大目标 Bot 数。
func ProfileMaxTargetBots(p *BotLoadProfile) int {
	if p == nil {
		return 0
	}
	switch p.Type {
	case "stable":
		if p.Stable != nil {
			return p.Stable.TargetBots
		}
	case "step":
		if p.Step != nil && len(p.Step.Stages) > 0 {
			return p.Step.Stages[len(p.Step.Stages)-1].TargetBots
		}
	case "spike":
		if p.Spike != nil {
			return p.Spike.TargetBots
		}
	}
	return 0
}

// ProfileHasBarrier 判断 profile 是否配置屏障。
func ProfileHasBarrier(p *BotLoadProfile) bool {
	return p != nil && p.Type == "spike" && p.Spike != nil && p.Spike.Barrier != nil
}

func estimatedProfileDurationSeconds(p *BotLoadProfile) int {
	switch p.Type {
	case "stable":
		if p.Stable == nil {
			return 0
		}
		return p.Stable.RampUpSeconds + p.Stable.DurationSeconds + 60
	case "step":
		if p.Step == nil {
			return 0
		}
		sum := 0
		for _, st := range p.Step.Stages {
			sum += st.HoldSeconds + 60
		}
		return sum
	case "spike":
		if p.Spike == nil {
			return 0
		}
		return p.Spike.ConnectWindowSeconds + 60 + p.Spike.HoldSeconds
	}
	return 0
}

// NormalizeAndValidateThresholds 校验阈值；raw 为空时返回默认。
func NormalizeAndValidateThresholds(raw json.RawMessage) (*BotLoadThresholds, error) {
	if len(raw) == 0 {
		t := DefaultBotLoadThresholds()
		return &t, nil
	}
	var t BotLoadThresholds
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		// 回退宽松解析以给出更清晰错误
		if err2 := json.Unmarshal(raw, &t); err2 != nil {
			return nil, fmt.Errorf("%w: %v", ErrBotLoadThresholdsInvalid, err2)
		}
	}
	if err := validateThresholds(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

func validateThresholds(t *BotLoadThresholds) error {
	rates := []struct {
		name string
		v    float64
	}{
		{"minOnlineRate", t.MinOnlineRate},
		{"minCommandSentRate", t.MinCommandSentRate},
		{"minScheduleCompletionRate", t.MinScheduleCompletionRate},
		{"minWorkerHealthRate", t.MinWorkerHealthRate},
		{"minBarrierArrivalRate", t.MinBarrierArrivalRate},
	}
	for _, r := range rates {
		if !isRate01(r.v) {
			return fmt.Errorf("%w: %s 必须为 0..1", ErrBotLoadThresholdsInvalid, r.name)
		}
	}
	if !isIntInRange(t.MaxScheduleLagP95MS, 0, 600000) {
		return fmt.Errorf("%w: maxScheduleLagP95Ms 必须为 0..600000 整数", ErrBotLoadThresholdsInvalid)
	}
	if !isIntInRange(t.MaxProcessCrashes, 0, 1000) {
		return fmt.Errorf("%w: maxProcessCrashes 必须为 0..1000 整数", ErrBotLoadThresholdsInvalid)
	}
	if t.Safety != nil {
		if t.Safety.MaxExecutorMemoryRate <= 0 || t.Safety.MaxExecutorMemoryRate > 1 || math.IsNaN(t.Safety.MaxExecutorMemoryRate) {
			return fmt.Errorf("%w: safety.maxExecutorMemoryRate 必须为 (0,1]", ErrBotLoadThresholdsInvalid)
		}
		if !isIntInRange(t.Safety.MaxEventLoopP95MS, 1, 60000) {
			return fmt.Errorf("%w: safety.maxEventLoopP95Ms 必须为 1..60000 整数", ErrBotLoadThresholdsInvalid)
		}
		if !isIntInRange(t.Safety.SustainSeconds, 1, 3600) {
			return fmt.Errorf("%w: safety.sustainSeconds 必须为 1..3600 整数", ErrBotLoadThresholdsInvalid)
		}
	}
	if t.Legacy != nil {
		if !t.Legacy.Enabled {
			if t.Legacy.MinTPS != nil || t.Legacy.MaxMsptP95 != nil || t.Legacy.RequireBusinessObservation != nil {
				return fmt.Errorf("%w: legacy.enabled=false 时不得提供其他 legacy 判据", ErrBotLoadThresholdsInvalid)
			}
		} else {
			if t.Legacy.MinTPS == nil && t.Legacy.MaxMsptP95 == nil && t.Legacy.RequireBusinessObservation == nil {
				return fmt.Errorf("%w: legacy.enabled=true 时至少提供一项判据", ErrBotLoadThresholdsInvalid)
			}
			if t.Legacy.MinTPS != nil && (*t.Legacy.MinTPS < 0 || *t.Legacy.MinTPS > 20 || math.IsNaN(*t.Legacy.MinTPS)) {
				return fmt.Errorf("%w: legacy.minTps 必须为 0..20", ErrBotLoadThresholdsInvalid)
			}
			if t.Legacy.MaxMsptP95 != nil && (*t.Legacy.MaxMsptP95 < 0 || *t.Legacy.MaxMsptP95 > 60000 || math.IsNaN(*t.Legacy.MaxMsptP95)) {
				return fmt.Errorf("%w: legacy.maxMsptP95 必须为 0..60000", ErrBotLoadThresholdsInvalid)
			}
		}
	}
	return nil
}

func isIntInRange(v, min, max int) bool {
	return v >= min && v <= max
}

func isRate01(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1
}

// EncodeJSON 将强类型 DTO 序列化为 JSON 字符串。
func EncodeJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
