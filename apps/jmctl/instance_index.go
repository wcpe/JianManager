package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/wcpe/JianManager/internal/worker/process"
)

// 读取 Worker 写下的实例清单快照（见 internal/worker/process/instance_index.go）。
//
// 定位：PID 文件只有寻址信息（uuid/pid/workdir），没有实例名与业务状态，
// 于是 jmctl 只能显示一串 UUID。快照补上了「这是什么服、现在什么状态」。
//
// 消费策略是**尽力而为**：旧版 Worker 不写这个文件、或文件正在被替换，
// 都应降级为「只显示 UUID」而不是让 list 失败——应急工具不能因为
// 一个可选的增强信息而不可用。

// instanceIndex 是快照的本地视图（字段与 process.InstanceIndex 对齐）。
type instanceIndex struct {
	GeneratedAt string           `json:"generatedAt"`
	Instances   []instanceRecord `json:"instances"`
}

// instanceRecord 对应快照里的单条记录。
type instanceRecord struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name,omitempty"`
	State       string `json:"state"`
	PID         int    `json:"pid,omitempty"`
	WorkDir     string `json:"workDir,omitempty"`
	ProcessType string `json:"processType,omitempty"`
	ServerPort  int    `json:"serverPort,omitempty"`
	QueryPort   int    `json:"queryPort,omitempty"`
}

// byUUID 建索引，供 list 与 PID 记录按 UUID 关联。
func (idx instanceIndex) byUUID() map[string]instanceRecord {
	out := make(map[string]instanceRecord, len(idx.Instances))
	for _, e := range idx.Instances {
		out[e.UUID] = e
	}
	return out
}

// loadInstanceIndex 从 PID 目录读取快照；任何失败都返回空索引。
//
// 返回空索引而非 error：调用方只关心「有没有更详细的信息」，
// 没有就退回基本展示，不需要区分「文件不存在」与「文件损坏」。
func loadInstanceIndex(pidDir string) instanceIndex {
	raw, err := os.ReadFile(filepath.Join(pidDir, process.InstanceIndexFilename))
	if err != nil {
		return instanceIndex{}
	}
	var idx instanceIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return instanceIndex{}
	}
	return idx
}
