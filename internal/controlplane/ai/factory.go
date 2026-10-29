package ai

import (
	"fmt"
	"net/http"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// NewStreamer 按协议构造流式适配器。
//
// client 必须由受保护出站工厂提供（internal/platform/httpclient.Guard），
// 这里不再自行构造 http.Client——准入与重定向策略只应有一处实现。
func NewStreamer(protocol string, client *http.Client, baseURL, apiKey string) (Streamer, error) {
	switch protocol {
	case model.AssistantProtocolOpenAIChat:
		return newOpenAIChatStreamer(client, baseURL, apiKey), nil
	case model.AssistantProtocolOpenAIResponses:
		return newOpenAIResponsesStreamer(client, baseURL, apiKey), nil
	case model.AssistantProtocolAnthropic:
		return newAnthropicStreamer(client, baseURL, apiKey), nil
	default:
		return nil, fmt.Errorf("不支持的协议: %q", protocol)
	}
}

// ProtocolSupported 判断协议是否被本批支持（供配置层校验，避免存进无法运行的值）。
func ProtocolSupported(protocol string) bool {
	switch protocol {
	case model.AssistantProtocolOpenAIChat,
		model.AssistantProtocolOpenAIResponses,
		model.AssistantProtocolAnthropic:
		return true
	}
	return false
}

// SupportedProtocols 返回受支持协议列表（供前端下拉使用）。
func SupportedProtocols() []string {
	return []string{
		model.AssistantProtocolOpenAIChat,
		model.AssistantProtocolOpenAIResponses,
		model.AssistantProtocolAnthropic,
	}
}
