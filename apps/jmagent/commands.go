package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// runWhoami GET /api/v1/agent/whoami
func runWhoami(c *Client, cfg Config, args []string) error {
	if err := requireToken(cfg); err != nil {
		return err
	}
	if len(args) > 0 {
		return fmt.Errorf("用法: jmagent whoami")
	}
	body, err := c.get("/api/v1/agent/whoami", nil)
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
		return fmt.Errorf("用法: jmagent list nodes | jmagent list instances [--node <id>]")
	}
	switch args[0] {
	case "nodes":
		if len(args) > 1 {
			return fmt.Errorf("用法: jmagent list nodes")
		}
		body, err := c.get("/api/v1/agent/nodes", nil)
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
		body, err := c.get("/api/v1/agent/instances", q)
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
			return "", fmt.Errorf("未知参数: %s；用法: jmagent list instances [--node <id>]", a)
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
		return fmt.Errorf("用法: jmagent instance status|metrics|start|stop|restart <id>")
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
		return fmt.Errorf("用法: jmagent instance %s <id>", action)
	}

	switch action {
	case "status":
		body, err := c.get("/api/v1/agent/instances/"+id, nil)
		if err != nil {
			return err
		}
		return writeOutput(cfg, body, formatInstanceDetail)
	case "metrics":
		body, err := c.get("/api/v1/agent/instances/"+id+"/metrics", nil)
		if err != nil {
			return err
		}
		// 指标结构因探针有无而异，text 用通用 pretty
		return writeOutput(cfg, body, nil)
	case "start", "stop", "restart":
		body, err := c.post("/api/v1/agent/instances/" + id + "/" + action)
		if err != nil {
			return err
		}
		return writeOutput(cfg, body, formatOK)
	default:
		return fmt.Errorf("未知 instance 动作 %q；支持 status/metrics/start/stop/restart", action)
	}
}

// runNode 分发 node maintenance enter|leave <id>
func runNode(c *Client, cfg Config, args []string) error {
	if err := requireToken(cfg); err != nil {
		return err
	}
	// node maintenance enter|leave <id>
	if len(args) < 3 || args[0] != "maintenance" {
		return fmt.Errorf("用法: jmagent node maintenance enter|leave <id>")
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
		return fmt.Errorf("用法: jmagent node maintenance enter|leave <id>")
	}

	var path string
	switch action {
	case "enter":
		path = "/api/v1/agent/nodes/" + id + "/maintenance/enter"
	case "leave":
		path = "/api/v1/agent/nodes/" + id + "/maintenance/leave"
	default:
		return fmt.Errorf("未知 maintenance 动作 %q；支持 enter / leave", action)
	}
	body, err := c.post(path)
	if err != nil {
		return err
	}
	return writeOutput(cfg, body, formatOK)
}
