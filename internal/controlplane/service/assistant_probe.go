package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/ai"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
)

// 探测与真实测试（spec §8）。
//
// 两个动作都由**显式请求**触发，绝不在保存后、列表刷新时或输入框失焦时自动发请求：
// 模型调用按次计费，把「保存」与「调用上游」绑定等于让每一次手滑都产生费用。

// 探测/测试的超时预算。
//
// 取 30s 而不是与运行时同样的 60s：这是管理员在页面上等着看的同步动作，
// 超过半分钟没有结论，与其继续等不如明确失败——真正的长耗时探测应当改成异步任务，
// 而不是把 HTTP 请求拖到客户端先超时。
const (
	assistantProbeTimeout = 30 * time.Second
	// assistantProbeMaxText 测试返回的文本预览上限（spec §8：≤200 字符）。
	assistantProbeMaxText = 200
	// assistantProbeMaxOutputTokens 测试请求的输出上限：只验证连通性，不需要长回复。
	assistantProbeMaxOutputTokens = 64
)

// AssistantProbeResult 供应商探测结论。
//
// Ok=false 是**结论**而不是请求失败：探测失败时仍返回 200，让前端能把
// 「连不通」和「面板自己的请求出错」区分开。
type AssistantProbeResult struct {
	OK        bool  `json:"ok"`
	LatencyMs int64 `json:"latencyMs"`
	// ModelCount 上游报告可用模型数；仅在探测走到「列模型」路径时有意义。
	ModelCount int `json:"modelCount,omitempty"`
	// ClassifiedError 分类码，失败时非空（见 ai.ErrorCode）。
	ClassifiedError string `json:"classifiedError,omitempty"`
	// Message 已脱敏的人类可读说明。
	Message string `json:"message,omitempty"`
}

// ModelTestResult 单模型真实测试结论。
type ModelTestResult struct {
	OK          bool   `json:"ok"`
	LatencyMs   int64  `json:"latencyMs"`
	TextPreview string `json:"textPreview,omitempty"`
	// PromptTokens / OutputTokens 只记上游真实报告值；未报告为 nil，不补 0。
	PromptTokens *int `json:"promptTokens"`
	OutputTokens *int `json:"outputTokens"`
	// ToolCallParsed 是否成功解析出**参数完整**的工具调用（仅带工具时判定）。
	ToolCallParsed  bool   `json:"toolCallParsed"`
	ClassifiedError string `json:"classifiedError,omitempty"`
	Message         string `json:"message,omitempty"`
}

// TestProvider 探测供应商连通性（spec §8）。
//
// 只用**一个模型**跑最小补全，不走「列模型端点」：后者各协议路径不同
// （OpenAI 兼容 /models、Anthropic /v1/models），且部分网关只实现了推理端点，
// 列模型反而更容易误报不通。真正要验证的是「这个 baseUrl + 这个密钥能不能说话」。
// 供应商下没有任何启用模型时明确报错，而不是假装通过——无法探测就是无法探测。
func (s *AssistantService) TestProvider(ctx context.Context, id uint) (*AssistantProbeResult, error) {
	var p model.AssistantProvider
	if err := s.db.First(&p, id).Error; err != nil {
		return nil, assistantLookupErr(err)
	}
	var m model.AssistantModel
	// 优先用启用的模型；没有则取任意一个（探测的是供应商，不是那个模型是否该对外可用）。
	err := s.db.Where("provider_id = ? AND enabled = ?", p.ID, true).Order("id asc").First(&m).Error
	if err != nil {
		err = s.db.Where("provider_id = ?", p.ID).Order("id asc").First(&m).Error
		if err != nil {
			return nil, fmt.Errorf("%w: 该供应商下还没有模型，无法探测（请先添加一个模型）", ErrAssistantInvalid)
		}
	}

	start := time.Now()
	res := s.runProbe(ctx, &p, m.ModelID)
	res.LatencyMs = time.Since(start).Milliseconds()
	return res, nil
}

// TestModel 用指定模型跑一次最小补全（spec §8）。
//
// withTools=true 时额外验证工具调用能否解析出完整参数——这是「该模型能不能用于
// 工具循环」的唯一直接证据，但**不执行**任何工具。
func (s *AssistantService) TestModel(ctx context.Context, id uint, withTools bool) (*ModelTestResult, error) {
	var m model.AssistantModel
	if err := s.db.First(&m, id).Error; err != nil {
		return nil, assistantLookupErr(err)
	}
	var p model.AssistantProvider
	if err := s.db.First(&p, m.ProviderID).Error; err != nil {
		return nil, fmt.Errorf("%w: 模型所属供应商不存在", ErrAssistantInvalid)
	}
	if withTools && !m.Tools {
		// 显式拒绝而不是「静默去掉工具再测」：后者会给出一个 ok=true 的假绿——
		// 管理员以为「工具验证通过」，实际根本没带工具。
		return nil, fmt.Errorf("%w: 该模型未声明 tools 能力，无法做工具调用验证", ErrAssistantInvalid)
	}

	start := time.Now()
	res := s.runModelTest(ctx, &p, &m, withTools)
	res.LatencyMs = time.Since(start).Milliseconds()
	return res, nil
}

// runProbe 探测的执行体（供应商维度，只关心「能不能连上说话」）。
func (s *AssistantService) runProbe(ctx context.Context, p *model.AssistantProvider, modelID string) *AssistantProbeResult {
	streamer, err := s.buildStreamer(p)
	if err != nil {
		return probeFailure(err)
	}
	ctx, cancel := context.WithTimeout(ctx, assistantProbeTimeout)
	defer cancel()

	got := false
	streamErr := streamer.Stream(ctx, ai.Request{
		Model:           modelID,
		System:          "只回一个字。",
		Messages:        []ai.Message{{Role: "user", Content: "ping"}},
		MaxOutputTokens: 1,
	}, func(ev ai.Event) error {
		// 收到任何文本或结束事件即视为「连上了」——探测不需要完整回复。
		if ev.Kind == ai.EventText || ev.Kind == ai.EventEnd {
			got = true
		}
		return nil
	})
	if streamErr != nil && !got {
		return probeFailure(streamErr)
	}
	return &AssistantProbeResult{OK: true}
}

// runModelTest 单模型测试的执行体。
func (s *AssistantService) runModelTest(ctx context.Context, p *model.AssistantProvider, m *model.AssistantModel, withTools bool) *ModelTestResult {
	streamer, err := s.buildStreamer(p)
	if err != nil {
		return testFailure(err)
	}
	ctx, cancel := context.WithTimeout(ctx, assistantProbeTimeout)
	defer cancel()

	var text strings.Builder
	var usage *ai.Usage
	toolCallOK := false
	req := ai.Request{
		Model:           m.ModelID,
		System:          "你是连通性测试助手，只回答被问的内容。",
		Messages:        []ai.Message{{Role: "user", Content: "回复 pong"}},
		MaxOutputTokens: assistantProbeMaxOutputTokens,
	}
	if withTools {
		// 带工具的验证必须**换一套指令**：不带工具那套是「回复 pong」+「只回答被问的内容」，
		// 模型会理所当然地用文本作答，于是一个完全支持工具调用的模型也会得到
		// toolCallParsed=false ——把「没被要求调用」误判成「协议不支持工具」，
		// 让管理员去换模型。这里显式要求调用并给出具体参数值：
		// 既让「模型愿不愿意调用」成为可观测事实，也让参数累积链路有真实内容可校验。
		req.System = "你是连通性测试助手。本轮提供了工具时，必须直接调用工具，不要用自然语言作答。"
		req.Messages = []ai.Message{{
			Role:    "user",
			Content: "请调用 assistant_probe_ping 工具，nonce 传 \"probe\"。",
		}}
		req.Tools = []ai.ToolSpec{{
			Name:        "assistant_probe_ping",
			Description: "连通性测试用工具。收到请求时必须调用它。",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"nonce":{"type":"string"}},"required":["nonce"]}`),
		}}
	}

	streamErr := streamer.Stream(ctx, req, func(ev ai.Event) error {
		switch ev.Kind {
		case ai.EventText:
			// 只留前 N 个字符：预览是给人看的，攒满就没有继续拼的意义。
			if text.Len() < assistantProbeMaxText {
				text.WriteString(ev.Text)
			}
		case ai.EventUsage:
			usage = ev.Usage
		case ai.EventToolCallEnd:
			// 能走到 End 说明参数已通过「是 JSON 对象」校验（适配器内保证）。
			toolCallOK = true
		}
		return nil
	})
	if streamErr != nil {
		return testFailure(streamErr)
	}

	res := &ModelTestResult{OK: true, ToolCallParsed: toolCallOK}
	preview := text.String()
	if runes := []rune(preview); len(runes) > assistantProbeMaxText {
		preview = string(runes[:assistantProbeMaxText]) + "…"
	}
	res.TextPreview = preview
	if usage != nil {
		res.PromptTokens = usage.InputTokens
		res.OutputTokens = usage.OutputTokens
	}
	return res
}

// buildStreamer 按供应商配置构造适配器（含凭据解析、出站准入与代理）。
func (s *AssistantService) buildStreamer(p *model.AssistantProvider) (ai.Streamer, error) {
	if s.egress == nil {
		return nil, fmt.Errorf("%w: 出站准入未初始化", ErrAssistantInvalid)
	}
	apiKey, err := s.resolveCredential(p)
	if err != nil {
		return nil, err
	}
	client, err := s.egressClientFor(p)
	if err != nil {
		return nil, err
	}
	return ai.NewStreamer(p.Protocol, client, p.BaseURL, apiKey)
}

// egressClientFor 按供应商的代理模式产出出站客户端。
//
// ## 为什么默认必须是直连
//
// `Guard.Client()` 本身不读环境变量代理（受保护出站的准入不能被环境代理绕过）。
// 若这里「留空就回退 ProxyFromEnvironment」，那么本机为别的用途设的全局 HTTPS_PROXY
// 会悄悄把模型流量改道——而管理员在面板上看到的是「直连」，出问题时无从判断流量去了哪。
// 因此三种模式是**显式**的：direct 直连、env 才跟随环境、custom 用填写的地址。
//
// ## 代理与准入的关系
//
// 代理只决定「连到哪里」，**不放松**准入判定：仍走同一个 Guard（只 https、
// 拒明文/私网未放行目标），只是把拨号换成经代理拨号。这样「配了代理」不会变成
// 「可以打任意内网地址」的旁路。
func (s *AssistantService) egressClientFor(p *model.AssistantProvider) (*http.Client, error) {
	mode := strings.TrimSpace(p.ProxyMode)
	switch mode {
	case "", model.AssistantProxyDirect:
		return s.egress.Client(), nil
	case model.AssistantProxyEnv:
		// env 模式沿用 Guard 的准入，但把 Transport.Proxy 换成标准库的环境解析。
		return s.egress.ClientWithProxyEnv(), nil
	case model.AssistantProxyCustom:
		raw := strings.TrimSpace(p.ProxyURL)
		if raw == "" {
			return nil, fmt.Errorf("%w: 代理模式为 custom 但未填写代理地址", ErrAssistantInvalid)
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("%w: 代理地址不合法（需形如 http://host:port 或 socks5://host:port）", ErrAssistantInvalid)
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "socks5":
		default:
			return nil, fmt.Errorf("%w: 不支持的代理协议 %q（仅 http / https / socks5）", ErrAssistantInvalid, u.Scheme)
		}
		return s.egress.ClientWithProxy(u), nil
	default:
		return nil, fmt.Errorf("%w: 未知代理模式 %q", ErrAssistantInvalid, mode)
	}
}

// resolveCredential 解析供应商的实际密钥。
//
// 三种模式的语义差异必须在这里落地：
//   - none：不发送密钥头（自建网关常无鉴权）。
//   - env：**只读进程环境**，不读文件、不做模板求值——限制变量名前缀的意义就在此，
//     否则「一个环境变量名」会退化成「可读取数据根下任意文件」。
//   - stored：解密；解密失败时**不静默降级为无密钥**，否则会得到一个更难查的 401。
func (s *AssistantService) resolveCredential(p *model.AssistantProvider) (string, error) {
	switch p.CredentialMode {
	case model.AssistantCredentialNone, "":
		return "", nil
	case model.AssistantCredentialEnv:
		val := os.Getenv(p.CredentialEnv)
		if strings.TrimSpace(val) == "" {
			// 指向了变量名但环境里没有：明确报错而不是当作「无凭据」继续请求。
			// 后者会以 401 的形式返回，让管理员去查密钥对不对，方向完全错了。
			return "", fmt.Errorf("%w: 环境变量 %s 未设置或为空", ErrAssistantInvalid, p.CredentialEnv)
		}
		return val, nil
	case model.AssistantCredentialStored:
		if p.CredentialEnc == "" {
			return "", fmt.Errorf("%w: 该供应商标记为密文凭据但未保存密钥", ErrAssistantInvalid)
		}
		if s.enc == nil {
			return "", ErrAssistantEncryptorMissing
		}
		plain, err := s.enc.Decrypt(p.CredentialEnc)
		if err != nil {
			// 不在这里回「密钥可能变了」以外的细节：解密失败可能是主密钥更换或密文损坏，
			// 两者对管理员的处置相同（重新录入密钥）。
			return "", fmt.Errorf("%w: 凭据解密失败（主密钥可能已更换，请重新录入密钥）", ErrAssistantInvalid)
		}
		return plain, nil
	default:
		return "", fmt.Errorf("%w: 未知凭据模式 %q", ErrAssistantInvalid, p.CredentialMode)
	}
}

// probeFailure 把错误转成探测结论（失败不是 5xx，而是 ok=false）。
func probeFailure(err error) *AssistantProbeResult {
	code, msg := classifyForProbe(err)
	return &AssistantProbeResult{OK: false, ClassifiedError: code, Message: msg}
}

// testFailure 把错误转成测试结论。
func testFailure(err error) *ModelTestResult {
	code, msg := classifyForProbe(err)
	return &ModelTestResult{OK: false, ClassifiedError: code, Message: msg}
}

// classifyForProbe 归一化分类码与消息。
//
// 出站准入错误在这里被翻译成 egress_*：它是「面板自己不让请求出去」，
// 与上游故障是两件事，混在 upstream_error 里会让管理员去查供应商状态。
func classifyForProbe(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	if errors.Is(err, httpclient.ErrProxyUnsupported) {
		return string(ai.CodeEgressProxyUnsupported), err.Error()
	}
	if errors.Is(err, httpclient.ErrBlockedTarget) {
		return string(ai.CodeEgressDenied), err.Error()
	}
	// 配置类错误（凭据缺失、出站未初始化）没有分类码可言：它们不是上游故障，
	// 也还没发出请求。用空码 + 原文，让管理员直接看到配置问题。
	if errors.Is(err, ErrAssistantInvalid) || errors.Is(err, ErrAssistantEncryptorMissing) {
		return "", err.Error()
	}
	return string(ai.Classify(err)), err.Error()
}

// assistantLookupErr 把「记录不存在」与「查询失败」分开。
func assistantLookupErr(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrAssistantNotFound
	}
	return fmt.Errorf("查询失败: %w", err)
}
