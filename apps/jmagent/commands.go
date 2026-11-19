package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// 高频命令糖走**用户侧**路由（`/api/v1/...`），而不是 Agent 侧（`/api/v1/agent/...`）。
//
// 这两套端点的凭证体系是分离的：
//   - `/api/v1/agent/*` 只认 `jmat_` Agent Token（机器凭证，供 MCP / Agent 使用）
//   - `/api/v1/*`       认 `jmut_` 用户令牌与浏览器 JWT（人类凭证）
//
// `jm` 是人用的客户端，登录拿的是 `jmut_`，因此必须走后者。早先这里错走了
// agent 路由，导致 `jm whoami` 直接报「需要有效的 Agent Token（jmat_ 前缀）」——
// 而 `jm api` 因为直连任意路径反倒正常，掩盖了这个问题。

// runWhoami GET /api/v1/auth/me
func runWhoami(c *Client, cfg Config, args []string) error {
	if err := requireToken(cfg); err != nil {
		return err
	}
	if len(args) > 0 {
		return fmt.Errorf("用法: %s whoami", progName())
	}
	body, err := c.get("/api/v1/auth/me", nil)
	if err != nil {
		return err
	}
	return writeOutput(cfg, body, formatWhoami)
}

// runList 分发 list nodes / list instances。
func runList(c *Client, cfg Config, args []string) error {
	if err := requireToken(cfg); err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("用法: %s list nodes | %s list instances [--node <id>]", progName(), progName())
	}
	switch args[0] {
	case "nodes":
		if len(args) > 1 {
			return fmt.Errorf("用法: %s list nodes", progName())
		}
		body, err := c.get("/api/v1/nodes", nil)
		if err != nil {
			return err
		}
		return writeOutput(cfg, body, formatNodeList)
	case "instances":
		nodeID, err := parseListInstancesFlags(args[1:])
		if err != nil {
			return err
		}
		var q url.Values
		if nodeID != "" {
			q = url.Values{"nodeId": {nodeID}}
		}
		body, err := c.get("/api/v1/instances", q)
		if err != nil {
			return err
		}
		return writeOutput(cfg, body, formatInstanceList)
	default:
		return fmt.Errorf("未知 list 目标 %q；支持 nodes / instances", args[0])
	}
}

// parseListInstancesFlags 解析 list instances 的 --node。
func parseListInstancesFlags(args []string) (nodeID string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--node" || a == "-node":
			if i+1 >= len(args) {
				return "", fmt.Errorf("--node 需要参数")
			}
			i++
			nodeID = strings.TrimSpace(args[i])
		case strings.HasPrefix(a, "--node="):
			nodeID = strings.TrimSpace(strings.TrimPrefix(a, "--node="))
		default:
			return "", fmt.Errorf("未知参数: %s；用法: %s list instances [--node <id>]", a, progName())
		}
	}
	return nodeID, nil
}

// runInstance 分发 instance status|metrics|start|stop|restart <id>
func runInstance(c *Client, cfg Config, args []string) error {
	if err := requireToken(cfg); err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("用法: %s instance status|metrics|start|stop|restart <id>", progName())
	}
	action := args[0]
	id := strings.TrimSpace(args[1])
	if id == "" {
		return fmt.Errorf("实例 id 不能为空")
	}
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return fmt.Errorf("实例 id 须为正整数，收到 %q", id)
	}
	if len(args) > 2 {
		return fmt.Errorf("用法: %s instance %s <id>", progName(), action)
	}

	switch action {
	case "status":
		body, err := c.get("/api/v1/instances/"+id, nil)
		if err != nil {
			return err
		}
		return writeOutput(cfg, body, formatInstanceDetail)
	case "metrics":
		body, err := c.get("/api/v1/instances/"+id+"/metrics", nil)
		if err != nil {
			return err
		}
		// 指标结构因探针有无而异，text 用通用 pretty
		return writeOutput(cfg, body, nil)
	case "start", "stop", "restart":
		body, err := c.post("/api/v1/instances/" + id + "/" + action)
		if err != nil {
			return err
		}
		return writeOutput(cfg, body, formatOK)
	default:
		return fmt.Errorf("未知 instance 动作 %q；支持 status/metrics/start/stop/restart", action)
	}
}

// runNode 分发 node maintenance enter|leave <id>
//
// 用户侧是单个端点 `POST /nodes/:id/maintenance` + `{"enabled":bool}`，
// 与 Agent 侧的 `/maintenance/enter|leave` 两个子路径不同——糖的用法保持不变，
// 差异在内部消化。
func runNode(c *Client, cfg Config, args []string) error {
	if err := requireToken(cfg); err != nil {
		return err
	}
	if len(args) < 3 || args[0] != "maintenance" {
		return fmt.Errorf("用法: %s node maintenance enter|leave <id>", progName())
	}
	action := args[1]
	id := strings.TrimSpace(args[2])
	if id == "" {
		return fmt.Errorf("节点 id 不能为空")
	}
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return fmt.Errorf("节点 id 须为正整数，收到 %q", id)
	}
	if len(args) > 3 {
		return fmt.Errorf("用法: %s node maintenance enter|leave <id>", progName())
	}

	var enabled bool
	switch action {
	case "enter":
		enabled = true
	case "leave":
		enabled = false
	default:
		return fmt.Errorf("未知 maintenance 动作 %q；支持 enter / leave", action)
	}

	body, err := c.postJSON("/api/v1/nodes/"+id+"/maintenance", map[string]any{"enabled": enabled})
	if err != nil {
		return err
	}
	return writeOutput(cfg, body, formatOK)
}
