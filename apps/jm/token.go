package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// `jm token` 管理 Agent 令牌（`jmat_`），面向「要让 agent 走 MCP」的场景。
//
// 为什么需要它：MCP 的能力最全（启动/重启/指标/日志），但上手成本高——
// 要先建 jmat_ 令牌、再手写 MCP 客户端配置。这一步卡住了几乎所有首次接入的 agent。
// 本命令把「建令牌 + 生成可粘贴的配置片段」压成一条命令。

// agentTokenCreateRequest 对应 POST /api/v1/agent/tokens 的请求体。
type agentTokenCreateRequest struct {
	Name              string   `json:"name"`
	ScopedInstanceIDs []uint   `json:"scopedInstanceIds,omitempty"`
	ScopedNodeIDs     []uint   `json:"scopedNodeIds,omitempty"`
	Capabilities      []string `json:"capabilities,omitempty"`
	PolicyVersion     *int     `json:"policyVersion,omitempty"`
	TTLDays           int      `json:"ttlDays,omitempty"`
}

// agentTokenCreateResult 是签发响应。
//
// 服务端返回 `{plaintext, token:{...}}`：明文单独一个字段（只此一次），
// 令牌本体是管理面投影（与列表项同构）。两者分开建模，避免把明文混进投影对象。
type agentTokenCreateResult struct {
	Plaintext string `json:"plaintext"`
	Token     struct {
		ID          uint   `json:"id"`
		Name        string `json:"name"`
		TokenPrefix string `json:"tokenPrefix"`
		ExpiresAt   string `json:"expiresAt"`
	} `json:"token"`
}

// runToken 分发 `jm token <sub>`。
func runToken(cfg Config, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(os.Stderr, `用法: jm token <子命令>

  create [选项]   签发 Agent 令牌（jmat_）并打印 MCP 配置片段
  list            列出当前令牌

create 选项:
  --name <名称>           令牌名称（必填，便于日后识别）
  --node <id>             限定可访问节点，可重复
  --instance <id>         限定可访问实例，可重复
  --capability <cap>      授予的能力，可重复（如 instance.life）
  --ttl-days <n>          有效天数（默认 90）
  --policy-version <n>    策略版本（1=V1 write allowlist，2=V2 能力发现）

示例:
  jm token create --name agent-ci --node 1 --capability instance.life
  jm token create --name agent-ro --capability instance.read --ttl-days 30

说明:
  - 明文令牌只在创建时返回一次，请立即保存
  - 生成后会打印可直接粘贴进 MCP 客户端的配置片段
  - 权限 = 令牌能力 ∩ 用户当前 RBAC，用户降权后令牌自动收窄
`)
		return nil
	}
	switch args[0] {
	case "create":
		return runTokenCreate(cfg, args[1:])
	case "list":
		return runTokenList(cfg)
	default:
		return fmt.Errorf("未知 token 子命令 %q；支持 create / list", args[0])
	}
}

// runTokenCreate 签发令牌并打印 MCP 配置片段。
func runTokenCreate(cfg Config, args []string) error {
	if err := requireLogin(cfg); err != nil {
		return err
	}

	req := agentTokenCreateRequest{TTLDays: 90}
	policyV2 := 2
	req.PolicyVersion = &policyV2

	for i := 0; i < len(args); i++ {
		needValue := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s 需要一个值", args[i])
			}
			i++
			return args[i], nil
		}
		switch args[i] {
		case "--name":
			v, err := needValue()
			if err != nil {
				return err
			}
			req.Name = strings.TrimSpace(v)
		case "--node":
			v, err := needValue()
			if err != nil {
				return err
			}
			id, err := parseUintArg("--node", v)
			if err != nil {
				return err
			}
			req.ScopedNodeIDs = append(req.ScopedNodeIDs, id)
		case "--instance":
			v, err := needValue()
			if err != nil {
				return err
			}
			id, err := parseUintArg("--instance", v)
			if err != nil {
				return err
			}
			req.ScopedInstanceIDs = append(req.ScopedInstanceIDs, id)
		case "--capability":
			v, err := needValue()
			if err != nil {
				return err
			}
			req.Capabilities = append(req.Capabilities, strings.TrimSpace(v))
		case "--ttl-days":
			v, err := needValue()
			if err != nil {
				return err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return fmt.Errorf("--ttl-days 需为正整数，收到 %q", v)
			}
			req.TTLDays = n
		case "--policy-version":
			v, err := needValue()
			if err != nil {
				return err
			}
			n, err := strconv.Atoi(v)
			if err != nil || (n != 1 && n != 2) {
				return fmt.Errorf("--policy-version 仅支持 1 或 2，收到 %q", v)
			}
			req.PolicyVersion = &n
		default:
			return fmt.Errorf("未知参数 %q；用法见 jm token create --help", args[i])
		}
	}

	if req.Name == "" {
		return fmt.Errorf("--name 必填：令牌名称用于日后识别，例如 --name agent-ci")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}
	resp, err := newClient(cfg).doRequest("POST", "/api/v1/agent/tokens", nil, body)
	if err != nil {
		return err
	}

	var res agentTokenCreateResult
	if err := json.Unmarshal(resp, &res); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}
	if res.Plaintext == "" {
		// 明文缺失说明服务端行为变了，直接报出来，别让用户以为创建成功却拿不到令牌。
		return fmt.Errorf("服务端未返回明文令牌，请用 jm api POST api/v1/agent/tokens 检查响应")
	}

	if cfg.Output == "json" {
		return printJSON(res)
	}

	fmt.Printf("已创建 Agent 令牌\n\n")
	fmt.Printf("  名称:   %s\n", res.Token.Name)
	fmt.Printf("  前缀:   %s\n", res.Token.TokenPrefix)
	if res.Token.ExpiresAt != "" {
		fmt.Printf("  有效期: 至 %s\n", res.Token.ExpiresAt)
	}
	fmt.Printf("\n令牌（仅显示一次，请立即保存）:\n  %s\n", res.Plaintext)
	fmt.Printf("\nMCP 客户端配置片段（粘贴到对应客户端即可）:\n")
	fmt.Printf("%s\n", mcpConfigSnippet(cfg.BaseURL, res.Plaintext))

	return nil
}

// runTokenList 列出已有令牌，便于确认「上一条建了什么」与吊销。
func runTokenList(cfg Config) error {
	if err := requireLogin(cfg); err != nil {
		return err
	}
	resp, err := newClient(cfg).get("/api/v1/agent/tokens", nil)
	if err != nil {
		return err
	}
	if cfg.Output == "json" {
		fmt.Println(strings.TrimRight(string(resp), "\n"))
		return nil
	}

	var tokens []struct {
		ID          uint   `json:"id"`
		Name        string `json:"name"`
		TokenPrefix string `json:"tokenPrefix"`
		ExpiresAt   string `json:"expiresAt"`
		Revoked     bool   `json:"revoked"`
	}
	if err := json.Unmarshal(resp, &tokens); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}
	if len(tokens) == 0 {
		fmt.Println("尚无 Agent 令牌。用 jm token create --name <名称> 创建。")
		return nil
	}

	fmt.Printf("%-6s  %-24s  %-14s  %-10s  %s\n", "ID", "名称", "前缀", "状态", "过期")
	for _, tk := range tokens {
		state := "有效"
		if tk.Revoked {
			state = "已吊销"
		}
		fmt.Printf("%-6d  %-24s  %-14s  %-10s  %s\n", tk.ID, tk.Name, tk.TokenPrefix, state, tk.ExpiresAt)
	}
	return nil
}

// mcpConfigSnippet 生成可直接粘贴的 MCP 客户端配置。
//
// 同时给 JSON 与命令行两种形态：内置 agent（如 Claude Code / Codex）多用 JSON，
// 而自建客户端常直接用 curl 验证连通性。让用户少查一次文档。
func mcpConfigSnippet(baseURL, token string) string {
	endpoint := strings.TrimRight(baseURL, "/") + "/api/v1/mcp"
	return fmt.Sprintf(`{
  "mcpServers": {
    "jianmanager": {
      "type": "http",
      "url": "%s",
      "headers": { "Authorization": "Bearer %s" }
    }
  }
}

验证连通性:
  curl -s %s -H "Authorization: Bearer %s" \
    -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | head -40
`, endpoint, token, endpoint, token)
}

// parseUintArg 解析正整数参数，给出带参数名的错误。
func parseUintArg(flag, v string) (uint, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("%s 需为正整数，收到 %q", flag, v)
	}
	return uint(n), nil
}
