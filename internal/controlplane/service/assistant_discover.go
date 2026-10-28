package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/ai"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 从上游发现模型（FR-538 §2.2）。
//
// ## 为什么需要它
//
// 模型 ID 是**外部易变的标识**（`gpt-4o-2024-08-06` 这种带日期后缀的尤其），
// 手抄必错，而抄错的表现是「探测通过、聊天 404 model not found」——
// 排查方向会被引向凭据与网络。让上游自己报出来是唯一不会漂移的做法。
//
// ## 为什么只读、不自动入库
//
// 发现只能拿到 `id`。而 `contextWindow` / `tools` / `streaming` 这些能力声明
// **不能从名字猜**（同名模型在不同网关上的支持度不同）。自动入库会写进一批
// 「看起来能用、实际能力未声明」的模型，反而把配置错误藏起来。
// 因此发现结果只作候选，由管理员确认后走既有 `POST /admin/models` 落库。

// DiscoveredModel 一个候选模型。
type DiscoveredModel struct {
	ModelID string `json:"modelId"`
	// AlreadyConfigured 本地是否已配置同 ID 模型（前端据此默认不勾选）。
	AlreadyConfigured bool `json:"alreadyConfigured"`
}

// DiscoverModelsResult 发现结果。
type DiscoverModelsResult struct {
	Discovered []DiscoveredModel `json:"discovered"`
	Total      int               `json:"total"`
	// Truncated 上游返回的条目超过上限时为 true（只保留前 N 条）。
	Truncated bool `json:"truncated"`
	// Skipped 被跳过的条目及原因（如 id 超长）。静默丢弃会让
	// 「上游明明有 30 个模型，这里只有 29 个」无从解释。
	Skipped map[string]string `json:"skipped,omitempty"`
}

// assistantDiscoverMaxModels 单次发现的条数上限。
//
// 有的网关会返回数千条（含嵌入、图像等非对话模型）。全部回传既无意义也拖垮面板，
// 因此截断并显式告知 truncated——而不是让调用方以为上游只有这么多。
const assistantDiscoverMaxModels = 200

// assistantDiscoverTimeout 发现请求的超时。列表接口通常很快，给 20s 足够。
const assistantDiscoverTimeout = 20 * time.Second

// DiscoverModels 向供应商拉取可用模型列表。
func (s *AssistantService) DiscoverModels(ctx context.Context, providerID uint) (*DiscoverModelsResult, error) {
	var provider model.AssistantProvider
	if err := s.db.First(&provider, providerID).Error; err != nil {
		return nil, fmt.Errorf("%w: 供应商不存在", ErrAssistantNotFound)
	}
	apiKey, err := s.resolveCredential(&provider)
	if err != nil {
		return nil, err
	}
	client, err := s.egressClientFor(&provider)
	if err != nil {
		return nil, err
	}

	url := discoverModelsURL(provider)
	ctx, cancel := context.WithTimeout(ctx, assistantDiscoverTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: 构造请求失败: %v", ErrAssistantInvalid, err)
	}
	// 按协议带鉴权头：Anthropic 用 x-api-key + 版本头，OpenAI 系用 Bearer。
	applyDiscoverAuth(req, provider.Protocol, apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		// 网络类错误交给既有分类器，保持与探测一致的错误码口径。
		code, msg := classifyForProbe(err)
		return nil, fmt.Errorf("%w: %s: %s", ErrAssistantInvalid, code, msg)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		// **不返回空列表**：空列表会让管理员以为「这个供应商没有模型」，
		// 而真实原因是「列表端点不被支持／鉴权失败」。方向错了最难查。
		// 用既有的上游错误体脱敏（上游 401 常把收到的密钥原样回显）。
		return nil, fmt.Errorf("%w: 上游返回 HTTP %d：%s",
			ErrAssistantInvalid, resp.StatusCode, ai.RedactUpstreamBody(string(body)))
	}

	ids, err := parseDiscoveredModels(body)
	if err != nil {
		return nil, err
	}

	// 标记本地已配置项：前端据 alreadyConfigured 默认不勾选，避免重复导入。
	configured := map[string]struct{}{}
	var existing []model.AssistantModel
	if err := s.db.Where("provider_id = ?", provider.ID).Find(&existing).Error; err == nil {
		for _, m := range existing {
			configured[m.ModelID] = struct{}{}
		}
	}

	res := &DiscoverModelsResult{Skipped: map[string]string{}}
	seen := map[string]struct{}{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if len(id) > 256 {
			// 与 assistant_models.model_id 的列宽一致：超长会被 DB 截断/拒绝，
			// 在这里就挡掉并说明，而不是让入库阶段报一个难懂的错误。
			res.Skipped[id[:32]+"…"] = "模型 ID 超过 256 字符，已跳过"
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		if len(res.Discovered) >= assistantDiscoverMaxModels {
			res.Truncated = true
			continue
		}
		_, ok := configured[id]
		res.Discovered = append(res.Discovered, DiscoveredModel{ModelID: id, AlreadyConfigured: ok})
	}
	res.Total = len(ids)
	if len(res.Skipped) == 0 {
		res.Skipped = nil
	}
	return res, nil
}

// discoverModelsURL 按协议拼列表端点。
//
// baseURL 已含固定路径前缀（如 `https://gw/v1`），列表端点在它下面再加 `/models`：
// 这是 OpenAI 兼容网关的通用形态；Anthropic 官方是 `{base}/v1/models`，
// 而管理员填的 BaseURL 通常已经带了 `/v1`，故两者同一拼法。
func discoverModelsURL(p model.AssistantProvider) string {
	return strings.TrimRight(p.BaseURL, "/") + "/models"
}

// applyDiscoverAuth 按协议附加鉴权头。
func applyDiscoverAuth(req *http.Request, protocol, apiKey string) {
	if apiKey == "" {
		return
	}
	switch protocol {
	case model.AssistantProtocolAnthropic:
		req.Header.Set("x-api-key", apiKey)
		// Anthropic 要求版本头，缺了会 400。
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

// parseDiscoveredModels 解析列表响应里的模型 ID。
//
// 兼容两种形态：OpenAI 的 `{"data":[{"id":...}]}` 与少数网关直接给数组。
// 解析不出**报错而不是返回空**：空结果与「解析失败」的表现都是「没有模型」，
// 但处置完全不同（前者去配供应商、后者是协议不兼容）。
func parseDiscoveredModels(body []byte) ([]string, error) {
	var withData struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &withData); err == nil && len(withData.Data) > 0 {
		out := make([]string, 0, len(withData.Data))
		for _, d := range withData.Data {
			out = append(out, d.ID)
		}
		return out, nil
	}
	var arr []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
		out := make([]string, 0, len(arr))
		for _, d := range arr {
			out = append(out, d.ID)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: 上游返回的模型列表无法解析（期望 {data:[{id}]} 或 [{id}]）", ErrAssistantInvalid)
}
