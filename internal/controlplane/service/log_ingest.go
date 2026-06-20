package service

import (
	"strings"
	"sync"
	"time"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// instanceRef 是实例 UUID 解析出的主键引用，缓存以避免每条日志反查数据库。
type instanceRef struct {
	instanceID uint
	nodeID     uint
}

// resolveCache 缓存 UUID→ID 映射。日志是高写入路径，逐条查库不可接受；
// 实例/节点的 UUID→ID 关系基本不变，故首次查得即缓存。容量未设上限：实例规模有限（受配额约束）。
type resolveCache struct {
	mu        sync.RWMutex
	instances map[string]instanceRef
	nodes     map[string]uint
}

func newResolveCache() *resolveCache {
	return &resolveCache{
		instances: make(map[string]instanceRef),
		nodes:     make(map[string]uint),
	}
}

// resolveInstance 解析实例 UUID 对应的 instanceID/nodeID（带缓存）。查不到返回零值引用。
func (s *LogService) resolveInstance(instanceUUID string) instanceRef {
	if instanceUUID == "" {
		return instanceRef{}
	}
	s.cache.mu.RLock()
	ref, ok := s.cache.instances[instanceUUID]
	s.cache.mu.RUnlock()
	if ok {
		return ref
	}

	var inst model.Instance
	if err := s.db.Select("id", "node_id").Where("uuid = ?", instanceUUID).First(&inst).Error; err != nil {
		// 查不到（实例可能已删除）：不缓存零值，留待下次重试。
		return instanceRef{}
	}
	ref = instanceRef{instanceID: inst.ID, nodeID: inst.NodeID}
	s.cache.mu.Lock()
	s.cache.instances[instanceUUID] = ref
	s.cache.mu.Unlock()
	return ref
}

// resolveNode 解析节点 UUID 对应的 nodeID（带缓存）。查不到返回 0。
func (s *LogService) resolveNode(nodeUUID string) uint {
	if nodeUUID == "" {
		return 0
	}
	s.cache.mu.RLock()
	id, ok := s.cache.nodes[nodeUUID]
	s.cache.mu.RUnlock()
	if ok {
		return id
	}
	var node model.Node
	if err := s.db.Select("id").Where("uuid = ?", nodeUUID).First(&node).Error; err != nil {
		return 0
	}
	s.cache.mu.Lock()
	s.cache.nodes[nodeUUID] = node.ID
	s.cache.mu.Unlock()
	return node.ID
}

// IngestInstanceOutput 实现 EventService.LogSink：把实例进程输出落库（FR-049）。
// 多行 chunk 拆成每行一条记录，便于按行检索；空行跳过。stderr 归为 error 级，stdout 归为 info 级。
func (s *LogService) IngestInstanceOutput(nodeUUID, instanceUUID, stream, message string, ts int64) {
	if !s.cfg.Enabled || message == "" {
		return
	}
	ref := s.resolveInstance(instanceUUID)
	nodeID := ref.nodeID
	if nodeID == 0 {
		nodeID = s.resolveNode(nodeUUID)
	}

	level := model.LogLevelInfo
	if stream == "stderr" {
		level = model.LogLevelError
	}
	t := time.Now()
	if ts > 0 {
		t = time.Unix(ts, 0)
	}

	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		s.Ingest(IngestEntry{
			Source:       model.LogSourceInstance,
			Level:        level,
			InstanceID:   ref.instanceID,
			InstanceUUID: instanceUUID,
			NodeID:       nodeID,
			Stream:       stream,
			Message:      line,
			Time:         t,
		})
	}
}
