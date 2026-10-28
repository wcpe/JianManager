package service

import (
	"context"
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/wcpe/JianManager/internal/controlplane/ai"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
)

// 真实供应商端到端验证（FR-514 验收 T9）。
//
// **默认跳过**。要跑它必须显式注入一组真实凭据，因为这组的价值恰恰在于
// 「对端确实这样答」——用 mock 或 httptest 验证不到任何东西（那些已被单测覆盖）。
//
// 为什么不写成普通单测然后 mock 上游：mock 只能证明「我们按自己理解的协议说话」，
// 而这正是本次要检验的假设本身。三个适配器的报文与 SSE 分帧都是照文档写的，
// 文档与实现有偏差时，只有真实上游能暴露出来。
//
// **验证范围必须说清楚**：本文件能证明的是「同一套生产代码路径（加密→落库→解密→
// 注入鉴权头→受保护出站→真实 HTTP→真实 SSE 分帧→事件解析）对着一个**真实监听端口**
// 的上游跑通」。用本地假供应商跑绿，只等于验证了「自托管 OpenAI 兼容端点」，
// **不等于**任何商业厂商已验证——真实厂商的鉴权差异、限流、字段微调仍未验证，
// 那需要真实凭据。没有凭据前，这条验收只能标注为「自托管兼容端点已验、真实厂商待验」。
//
// 用法（凭据不进仓库、不进 shell 历史）：
//
//	read -rs JIANMANAGER_ASSISTANT_LIVE_KEY && export JIANMANAGER_ASSISTANT_LIVE_KEY
//	JIANMANAGER_ASSISTANT_LIVE_PROTOCOL=openai-chat \
//	JIANMANAGER_ASSISTANT_LIVE_BASE_URL=https://api.example.com/v1 \
//	JIANMANAGER_ASSISTANT_LIVE_MODEL=some-model \
//	JIANMANAGER_ASSISTANT_LIVE_TOOLS=1 \
//	go test ./internal/controlplane/service/ -run 'TestAssistantLive' -v -count=1
//
// 用本地假供应商（.tmp/fake-sse-provider.mjs，真实 HTTP + 真实 SSE 分帧）自检时：
//
//	FAKE_PROBE_TOOL=assistant_probe_ping node .tmp/fake-sse-provider.mjs --port 5399
//	JIANMANAGER_ASSISTANT_LIVE_PROTOCOL=openai-chat \
//	JIANMANAGER_ASSISTANT_LIVE_BASE_URL=http://127.0.0.1:5399/v1 \
//	JIANMANAGER_ASSISTANT_LIVE_MODEL=fake-sse-1 \
//	JIANMANAGER_ASSISTANT_LIVE_KEY=fake-key \
//	JIANMANAGER_ASSISTANT_LIVE_TOOLS=1 \
//	JIANMANAGER_ASSISTANT_LIVE_PRIVATE=1 \
//	go test ./internal/controlplane/service/ -run 'TestAssistantLive' -v -count=1
//
// 需要逐个协议验证就换 PROTOCOL 与 BASE_URL 各跑一次（三个协议的 baseURL 路径前缀
// 不同：OpenAI 兼容通常带 /v1，Anthropic 通常不带）。
//
// 环境变量：
//
//	JIANMANAGER_ASSISTANT_LIVE_PROTOCOL  openai-chat | openai-responses | anthropic
//	JIANMANAGER_ASSISTANT_LIVE_BASE_URL  供应商 base URL（默认必须是 https）
//	JIANMANAGER_ASSISTANT_LIVE_MODEL     上游原始模型标识
//	JIANMANAGER_ASSISTANT_LIVE_KEY       密钥（走 stored 模式，验证解密路径）
//	JIANMANAGER_ASSISTANT_LIVE_TOOLS     设为 1 时额外验证工具调用参数解析
//	JIANMANAGER_ASSISTANT_LIVE_PRIVATE   设为 1 时放行私网目标（自托管网关）
//
// 关于 PRIVATE：它只影响本测试**自己构造**的那个 Guard（放行 127.0.0.1/32 与明文 http），
// 生产环境的出站准入（强制 https、禁私网、禁代理）不受任何影响。测试要连本地端点，
// 就必须由测试自己显式开口子，而不是把生产校验放宽。
const (
	liveEnvProtocol = "JIANMANAGER_ASSISTANT_LIVE_PROTOCOL"
	liveEnvBaseURL  = "JIANMANAGER_ASSISTANT_LIVE_BASE_URL"
	liveEnvModel    = "JIANMANAGER_ASSISTANT_LIVE_MODEL"
	liveEnvKey      = "JIANMANAGER_ASSISTANT_LIVE_KEY"
	liveEnvTools    = "JIANMANAGER_ASSISTANT_LIVE_TOOLS"
	liveEnvPrivate  = "JIANMANAGER_ASSISTANT_LIVE_PRIVATE"
)

// liveConfig 读活体测试配置；缺失即 skip。
//
// skip 信息刻意写全「缺哪个变量、怎么补」，否则跑一次看到 skip 却不知道怎么继续，
// 这条验收就会被无限期搁置。
func liveConfig(t *testing.T) (protocol, baseURL, modelID, key string, withTools bool) {
	t.Helper()
	protocol = strings.TrimSpace(os.Getenv(liveEnvProtocol))
	baseURL = strings.TrimSpace(os.Getenv(liveEnvBaseURL))
	modelID = strings.TrimSpace(os.Getenv(liveEnvModel))
	key = os.Getenv(liveEnvKey)
	withTools = os.Getenv(liveEnvTools) == "1"

	var missing []string
	if protocol == "" {
		missing = append(missing, liveEnvProtocol)
	}
	if baseURL == "" {
		missing = append(missing, liveEnvBaseURL)
	}
	if modelID == "" {
		missing = append(missing, liveEnvModel)
	}
	if strings.TrimSpace(key) == "" {
		missing = append(missing, liveEnvKey)
	}
	if len(missing) > 0 {
		t.Skipf("跳过真实供应商验证：未设置 %s。"+
			"这是**未验证**而非通过——FR-514 的 T9 验收仍待执行。", strings.Join(missing, " / "))
	}
	if !ai.ProtocolSupported(protocol) {
		// 协议写错是真错误，不是 skip：让它红，否则打错字会被当成「已验过」。
		t.Fatalf("%s=%q 不是受支持协议（%s）", liveEnvProtocol, protocol,
			strings.Join(ai.SupportedProtocols(), " / "))
	}
	return protocol, baseURL, modelID, key, withTools
}

// newLiveAssistantService 构造一个能出站到真实上游的服务。
//
// 用 stored 模式而不是 env 模式：这样活体测试同时覆盖「加密 → 落库 → 解密 → 注入请求」
// 这条最容易在部署后才暴露问题的链路（主密钥缺失/更换）。
func newLiveAssistantService(t *testing.T, enc *KeyEncryptor, allowPrivate bool) *AssistantService {
	t.Helper()
	db := newAssistantTestDB(t)
	s := NewAssistantService(db, enc)
	opts := httpclient.GuardOptions{Timeout: 60 * 1e9}
	if allowPrivate {
		opts.AllowPrivate = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
		opts.AllowInsecure = true
	}
	s.SetEgress(httpclient.NewGuard(opts))
	if err := s.EnsureSetting(); err != nil {
		t.Fatalf("初始化设置失败: %v", err)
	}
	return s
}

// TestAssistantLiveProvider 用真实上游跑通一次最小补全（含可选的工具解析）。
//
// 这是 FR-514 验收 T9 的可执行形态。断言刻意很窄：只要上游回了一段文本就算通，
// 因为这条测的是「协议对得上」，不是模型质量。
func TestAssistantLiveProvider(t *testing.T) {
	protocol, baseURL, modelID, key, withTools := liveConfig(t)
	allowPrivate := os.Getenv(liveEnvPrivate) == "1"
	s := newLiveAssistantService(t, testEncryptor(t), allowPrivate)

	p, err := s.CreateProvider(SaveProviderParams{
		Name:             "live-probe",
		Protocol:         protocol,
		BaseURL:          baseURL,
		Enabled:          true,
		CredentialMode:   model.AssistantCredentialStored,
		CredentialSecret: key,
	})
	if err != nil {
		t.Fatalf("建供应商失败（baseUrl 不合格会在这里被挡）: %v", err)
	}
	m, err := s.CreateModel(SaveModelParams{
		ProviderID: p.ID,
		ModelID:    modelID,
		Enabled:    true,
		Streaming:  true,
		Tools:      withTools,
	})
	if err != nil {
		t.Fatalf("建模型失败: %v", err)
	}

	res, err := s.TestModel(context.Background(), m.ID, withTools)
	if err != nil {
		t.Fatalf("测试请求失败（请求级错误，不是上游结论）: %v", err)
	}
	if !res.OK {
		t.Fatalf("真实上游返回失败: 分类=%q 说明=%s", res.ClassifiedError, res.Message)
	}
	hasText := strings.TrimSpace(res.TextPreview) != ""
	// 断言按「本轮想验证什么」分开，不能要求二者同时成立：
	// 一个遵循指令的上游在工具模式下往往**只**回 tool_calls、正文为空，
	// 硬要它同时给文本，等于要求上游做它不会被要求做的事。
	//
	// 不带工具：必须回出文本，否则多半是「流结束了但增量字段名与我们的解析不一致」，
	// 正是本测试存在的理由。
	// 带工具：必须解析出参数完整的调用；文本可有可无（空是正常形态）。
	if !withTools && !hasText {
		t.Fatalf("上游返回成功但文本为空——很可能分帧或增量字段解析与对端不一致")
	}
	if withTools && !res.ToolCallParsed {
		// 工具调用没解析出来：可能是上游没按 schema 调用，也可能是我们的参数累积有偏差。
		// 后者才是本测试要抓的，故此处判失败并要求人工看 message。
		t.Fatalf("要求了工具验证但未解析出完整调用（message=%s 文本=%q）；"+
			"若确认该上游本身不遵循 schema，请换一个支持工具的模型再验", res.Message, res.TextPreview)
	}
	if withTools && !hasText && !res.ToolCallParsed {
		// 既没有文本也没有工具调用 = 上游什么有效内容都没产出，不能算通过。
		t.Fatalf("上游返回成功但既无文本也无工具调用")
	}
	if res.PromptTokens == nil && res.OutputTokens == nil {
		// 允许上游不报用量，但要在日志里留痕，避免「以为验了用量」。
		t.Logf("注意：上游未报告 usage（协议允许），本次未验证用量解析")
	}

	t.Logf("真实上游通过：协议=%s 模型=%s 延迟=%dms 文本=%q 工具=%v 用量=%v/%v",
		protocol, modelID, res.LatencyMs, res.TextPreview, res.ToolCallParsed,
		ptrOrNil(res.PromptTokens), ptrOrNil(res.OutputTokens))
}

// TestAssistantLiveProviderProbe 验证供应商级探测（走「首个启用模型」分支）。
func TestAssistantLiveProviderProbe(t *testing.T) {
	_, baseURL, modelID, key, _ := liveConfig(t)
	allowPrivate := os.Getenv(liveEnvPrivate) == "1"
	// 探测固定用 openai-chat 会打错协议，因此复用环境里的 protocol。
	protocol := strings.TrimSpace(os.Getenv(liveEnvProtocol))
	s := newLiveAssistantService(t, testEncryptor(t), allowPrivate)

	p, err := s.CreateProvider(SaveProviderParams{
		Name:             "live-probe-svc",
		Protocol:         protocol,
		BaseURL:          baseURL,
		Enabled:          true,
		CredentialMode:   model.AssistantCredentialStored,
		CredentialSecret: key,
	})
	if err != nil {
		t.Fatalf("建供应商失败: %v", err)
	}
	if _, err := s.CreateModel(SaveModelParams{
		ProviderID: p.ID, ModelID: modelID, Enabled: true, Streaming: true,
	}); err != nil {
		t.Fatalf("建模型失败: %v", err)
	}

	res, err := s.TestProvider(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("探测请求失败: %v", err)
	}
	if !res.OK {
		t.Fatalf("真实上游探测失败: 分类=%q 说明=%s", res.ClassifiedError, res.Message)
	}
	t.Logf("真实上游探测通过：延迟=%dms", res.LatencyMs)
}

// ptrOrNil 便于日志输出可空用量。
func ptrOrNil(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}
