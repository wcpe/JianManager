/**
 * Go 侧 Bot 管理层 IPC 类型定义。
 * 与 bot-worker/src/ipc/types.ts 保持同步。
 */

package bot

// IpcCommand Worker Node → Bot Worker 的命令。
type IpcCommand struct {
	Cmd string `json:"cmd"`
}

// CreateBotsCommand 批量创建 Bot 命令。
type CreateBotsCommand struct {
	Cmd  string      `json:"cmd"`
	Bots []BotConfig `json:"bots"`
}

// StopBotsCommand 批量停止 Bot 命令。
type StopBotsCommand struct {
	Cmd    string   `json:"cmd"`
	BotIds []string `json:"botIds"`
}

// SetBehaviorCommand 切换行为模式命令。
type SetBehaviorCommand struct {
	Cmd      string       `json:"cmd"`
	BotID    string       `json:"botId"`
	Behavior string       `json:"behavior"`
	Target   string       `json:"target,omitempty"`
	Config   interface{}  `json:"config,omitempty"`
}

// SendBotCommand 向 Bot 发送命令。
type SendBotCommand struct {
	Cmd     string `json:"cmd"`
	BotID   string `json:"botId"`
	Command string `json:"command"`
}

// RunScriptCommand 执行脚本命令。
type RunScriptCommand struct {
	Cmd      string        `json:"cmd"`
	ScriptID string        `json:"scriptId"`
	Steps    []ScriptStep  `json:"steps"`
	BotIds   []string      `json:"botIds"`
}

// StopScriptCommand 停止脚本命令。
type StopScriptCommand struct {
	Cmd      string `json:"cmd"`
	ScriptID string `json:"scriptId"`
}

// BotConfig Bot 配置（下发给 Bot Worker）。
type BotConfig struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	Host           string      `json:"host"`
	Port           int         `json:"port"`
	Username       string      `json:"username,omitempty"`
	Version        string      `json:"version,omitempty"`
	Auth           string      `json:"auth,omitempty"`
	Behavior       string      `json:"behavior,omitempty"`
	BehaviorConfig interface{} `json:"behaviorConfig,omitempty"`
	Server         string      `json:"server,omitempty"`
}

// ScriptStep 脚本步骤。
type ScriptStep struct {
	Action   string   `json:"action"`
	Message  string   `json:"message,omitempty"`
	Pos      *Vec3    `json:"pos,omitempty"`
	Duration int      `json:"duration,omitempty"`
	Command  string   `json:"command,omitempty"`
	Text     string   `json:"text,omitempty"`
}

// Vec3 坐标。
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}
