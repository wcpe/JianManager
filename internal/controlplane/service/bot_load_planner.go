package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	defaultBotLoadConnectRate = 5
	maxBotLoadBatchSize       = 50
	maxBotLoadExecutorNodes   = 256
)

// BotLoadPlanRequest 是确定性分片器的纯输入。
type BotLoadPlanRequest struct {
	RunID                       uint
	RunUUID                     string
	TargetBots                  int
	NodeCapacities              []BotLoadNodeCapacity
	ExecutorNodeIDs             []uint
	ConnectRatePerSecondPerNode int
	ConnectStartAt              time.Time
}

// BotLoadPlanningResult 是分片器的纯输出。
type BotLoadPlanningResult struct {
	Ready               bool
	TargetBots          int
	TotalAvailable      int
	Allocations         []BotLoadAllocation
	CapacityGenerations []BotLoadNodeGeneration
	Blockers            []BotLoadIssue
}

// BotLoadPlanner 按 ADR-074 生成稳定、轮转且单批不超过 50 的分片计划。
type BotLoadPlanner struct{}

// Plan 生成确定性分片；容量不足或显式节点不可用时不返回部分计划。
func (BotLoadPlanner) Plan(req BotLoadPlanRequest) (BotLoadPlanningResult, error) {
	if err := validateBotLoadPlanRequest(req); err != nil {
		return BotLoadPlanningResult{}, err
	}
	selected, blockers := selectBotLoadNodes(req.NodeCapacities, req.ExecutorNodeIDs)
	result := BotLoadPlanningResult{
		TargetBots: req.TargetBots, Allocations: []BotLoadAllocation{}, Blockers: blockers,
	}
	for _, node := range selected {
		result.TotalAvailable += node.AvailableBots
	}
	if len(blockers) > 0 {
		return result, nil
	}
	if result.TotalAvailable < req.TargetBots {
		result.Blockers = append(result.Blockers, BotLoadIssue{
			Code:    BotLoadCapacityInsufficientCode,
			Message: fmt.Sprintf("可用容量 %d 小于目标 Bot 数 %d", result.TotalAvailable, req.TargetBots),
		})
		return result, nil
	}
	result.Allocations = buildBotLoadAllocations(req, selected)
	result.CapacityGenerations = usedBotLoadGenerations(result.Allocations, selected)
	result.Ready = true
	return result, nil
}

func validateBotLoadPlanRequest(req BotLoadPlanRequest) error {
	if req.RunID == 0 || strings.TrimSpace(req.RunUUID) == "" || req.TargetBots < 1 {
		return ErrBotLoadPreflightInvalid
	}
	if len(req.ExecutorNodeIDs) > maxBotLoadExecutorNodes {
		return fmt.Errorf("%w: 执行节点最多 %d 个", ErrBotLoadPreflightInvalid, maxBotLoadExecutorNodes)
	}
	rate := req.ConnectRatePerSecondPerNode
	if rate != 0 && (rate < 1 || rate > 50) {
		return fmt.Errorf("%w: 每节点连接速率必须为 1..50", ErrBotLoadPreflightInvalid)
	}
	if req.ConnectStartAt.IsZero() {
		return fmt.Errorf("%w: 缺少连接起始时间", ErrBotLoadPreflightInvalid)
	}
	return nil
}

func selectBotLoadNodes(nodes []BotLoadNodeCapacity, requested []uint) ([]BotLoadNodeCapacity, []BotLoadIssue) {
	if len(requested) == 0 {
		return autoSelectBotLoadNodes(nodes), nil
	}
	byID := make(map[uint]BotLoadNodeCapacity, len(nodes))
	for _, node := range nodes {
		byID[node.NodeID] = node
	}
	selected := make([]BotLoadNodeCapacity, 0, len(requested))
	blockers := make([]BotLoadIssue, 0)
	for _, nodeID := range uniqueBotLoadNodeIDs(requested) {
		node, ok := byID[nodeID]
		if !ok || !botLoadNodeSelectable(node) {
			blockers = append(blockers, unavailableBotLoadNodeIssue(nodeID, node, ok))
			continue
		}
		selected = append(selected, node)
	}
	return selected, blockers
}

func autoSelectBotLoadNodes(nodes []BotLoadNodeCapacity) []BotLoadNodeCapacity {
	selected := make([]BotLoadNodeCapacity, 0, len(nodes))
	for _, node := range nodes {
		if botLoadNodeSelectable(node) {
			selected = append(selected, node)
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].AvailableBots != selected[j].AvailableBots {
			return selected[i].AvailableBots > selected[j].AvailableBots
		}
		return selected[i].NodeID < selected[j].NodeID
	})
	return selected
}

func botLoadNodeSelectable(node BotLoadNodeCapacity) bool {
	return node.Online && node.BotWorkerReady && !node.Legacy && node.UnavailableReason == "" && node.AvailableBots > 0
}

func unavailableBotLoadNodeIssue(nodeID uint, node BotLoadNodeCapacity, found bool) BotLoadIssue {
	reason := "指定的执行节点不存在"
	if found {
		reason = node.UnavailableReason
		if reason == "" {
			reason = "指定的执行节点当前不可用"
		}
	}
	id := nodeID
	return BotLoadIssue{Code: BotLoadNodeUnavailableCode, Message: reason, NodeID: &id}
}

func uniqueBotLoadNodeIDs(ids []uint) []uint {
	seen := make(map[uint]struct{}, len(ids))
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func buildBotLoadAllocations(req BotLoadPlanRequest, nodes []BotLoadNodeCapacity) []BotLoadAllocation {
	remainingByNode := make(map[uint]int, len(nodes))
	allocatedByNode := make(map[uint]int, len(nodes))
	for _, node := range nodes {
		remainingByNode[node.NodeID] = node.AvailableBots
	}
	remainingTarget := req.TargetBots
	allocations := make([]BotLoadAllocation, 0)
	for remainingTarget > 0 {
		for _, node := range nodes {
			count := botLoadBatchCount(remainingTarget, remainingByNode[node.NodeID])
			if count == 0 {
				continue
			}
			ordinal := len(allocations) + 1
			allocation := newBotLoadAllocation(req, node, ordinal, count, allocatedByNode[node.NodeID])
			allocations = append(allocations, allocation)
			remainingTarget -= count
			remainingByNode[node.NodeID] -= count
			allocatedByNode[node.NodeID] += count
			if remainingTarget == 0 {
				break
			}
		}
	}
	return allocations
}

func botLoadBatchCount(targetRemaining, nodeRemaining int) int {
	if targetRemaining < 1 || nodeRemaining < 1 {
		return 0
	}
	count := min(nodeRemaining, maxBotLoadBatchSize)
	return min(count, targetRemaining)
}

func newBotLoadAllocation(req BotLoadPlanRequest, node BotLoadNodeCapacity, ordinal, count, nodeOffset int) BotLoadAllocation {
	rate := req.ConnectRatePerSecondPerNode
	if rate == 0 {
		rate = defaultBotLoadConnectRate
	}
	intervalMS := (1000 + rate - 1) / rate
	start := req.ConnectStartAt.UTC().Add(time.Duration(nodeOffset*intervalMS) * time.Millisecond)
	identity := fmt.Sprintf("%d|%s|%d|%d|%d|%d|%s|%d", req.RunID, req.RunUUID, req.TargetBots, node.NodeID, node.CapacityGeneration, ordinal, start.Format(time.RFC3339Nano), count)
	return BotLoadAllocation{
		BatchID: stableBotLoadUUID(identity), Ordinal: ordinal,
		ExecutorNodeID: node.NodeID, ExecutorNodeUUID: node.NodeUUID, ExecutorNodeName: node.NodeName,
		PlannedCount: count, ConnectStartAt: start, ConnectIntervalMS: intervalMS,
		IdempotencyKey: "bot-load-" + stableBotLoadDigest(identity+"|apply"),
	}
}

func usedBotLoadGenerations(allocations []BotLoadAllocation, nodes []BotLoadNodeCapacity) []BotLoadNodeGeneration {
	used := make(map[uint]struct{}, len(allocations))
	for _, allocation := range allocations {
		used[allocation.ExecutorNodeID] = struct{}{}
	}
	out := make([]BotLoadNodeGeneration, 0, len(used))
	for _, node := range nodes {
		if _, ok := used[node.NodeID]; ok {
			out = append(out, BotLoadNodeGeneration{NodeID: node.NodeID, CapacityGeneration: node.CapacityGeneration})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// BotLoadAllocationHash 计算服务端计划正文的稳定 SHA-256，不信任客户端提供的 hash。
func BotLoadAllocationHash(runID uint, targetBots int, allocations []BotLoadAllocation) (string, error) {
	canonical := struct {
		RunID       uint                `json:"runId"`
		TargetBots  int                 `json:"targetBots"`
		Allocations []BotLoadAllocation `json:"allocations"`
	}{RunID: runID, TargetBots: targetBots, Allocations: allocations}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("序列化 Bot 负载计划失败: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func stableBotLoadDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func stableBotLoadUUID(value string) string {
	sum := sha256.Sum256([]byte(value))
	bytes := sum[:16]
	bytes[6] = (bytes[6] & 0x0f) | 0x50
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	hexValue := hex.EncodeToString(bytes)
	return hexValue[:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:]
}
