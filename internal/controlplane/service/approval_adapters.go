package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 审批执行器与领域动作的接线。
//
// ## 为什么集中在这里而不是散在 main.go
//
// 19 个高危 action 的参数映射各自不同，逐个散装会出现「某个 action 忘了注册」——
// 而那种遗漏的表现是「批准后失败关闭」，看起来像执行器坏了，实际是装配漏了。
// 集中一处 + 一个覆盖度测试（`TestAdaptersCoverAllRequiredActions`）能把这类遗漏
// 变成编译期可见。
//
// ## 参数映射原则
//
// adapter 只吃票据里**冻结的那份参数**（`payload`），不去读请求上下文。
// 这是「批准的」与「执行的」必须是同一件事的落点。
// 参数缺失时返回明确错误——不要猜默认值，猜错的默认值比报错更危险。

// ApprovalAdapterDeps 注册 adapter 所需的领域服务。
//
// 用依赖结构而不是让 executor 直接持有全部服务：executor 的职责是「认领+收尾」，
// 领域知识应当只在装配层出现。
type ApprovalAdapterDeps struct {
	Instance      *InstanceService
	Node          *NodeService
	File          *FileService
	FileVersion   *FileVersionService
	Config        *ConfigService
	Bot           *BotService
	InstanceGroup *InstanceGroupService
	Network       *NetworkService
	Registration  *RegistrationService
	Snapshot      *SnapshotService
	BinaryVersion *BinaryVersionService
	// Provision 一键搭建（instance_provision_server）。
	Provision *ProvisionService
	// Transfer Agent 传输票据签发（file_issue_transfer_ticket）。
	Transfer *AgentTransferTicketService
	// Agent 用于按票据里的 tokenId 重建原始 Agent 主体。
	Agent *AgentTokenService
}

// loadTicketPrincipal 按票据记录的 tokenId 重建 Agent 主体。
//
// 为什么需要重建：executor 拿到的只有票据（含 tokenId 字符串），而有些领域方法
// （如传输票据签发）会**再次做授权判定**。缺主体时不能凭空造一个——
// 那等于用假身份去撞授权，要么必然被拒、要么（更糟）在宽松判据下真的放行。
// 人类直接提单没有 tokenId，这类动作本就不该由人代签，故明确拒绝。
func (d ApprovalAdapterDeps) loadTicketPrincipal(t *model.ApprovalTicket) (*AgentPrincipal, error) {
	tokenID := strings.TrimSpace(t.TokenID)
	if tokenID == "" {
		return nil, fmt.Errorf("该操作需要 Agent 身份（票据 %d 由人类提交，无 tokenId）", t.ID)
	}
	if d.Agent == nil {
		return nil, fmt.Errorf("Agent 服务未装配，无法重建票据 %d 的调用主体", t.ID)
	}
	var id uint
	if _, err := fmt.Sscanf(tokenID, "%d", &id); err != nil || id == 0 {
		return nil, fmt.Errorf("票据 %d 的 tokenId 非法: %q", t.ID, tokenID)
	}
	p, err := d.Agent.PrincipalByID(id)
	if err != nil {
		return nil, fmt.Errorf("重建票据 %d 的 Agent 主体失败: %w", t.ID, err)
	}
	return p, nil
}

// RegisterApprovalAdapters 把已登记的高危 action 接到领域动作。
//
// 返回未注册的 action 列表（供装配层日志/断言）：未注册的那些在执行时会失败关闭，
// 因此装配层应当把非空列表当成需要处理的信号，而不是静默忽略。
func RegisterApprovalAdapters(ex *ApprovalExecutor, d ApprovalAdapterDeps) []string {
	if ex == nil {
		return ApprovalRequiredActionNames()
	}

	// ── 实例生命周期 ──
	if d.Instance != nil {
		reg(ex, "instance_stop", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			return d.Instance.Stop(id)
		})
		// 批量启停：等价 N 次 instance_stop/start，因此与单实例同审批。
		//
		// 只允许 start/stop/restart 三个 op——与 MCP 工具层的白名单保持一致
		// （破坏性操作不进批量）。逐台执行、**不静默吞失败**：第一台失败即返回，
		// 让批准人看到「批量只做了一部分」，而不是被一个 ok 掩盖。
		reg(ex, "instance_batch", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			op, err := payloadString(p, "op")
			if err != nil {
				return err
			}
			ids, err := payloadUintSlice(p, "ids")
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				return fmt.Errorf("参数 ids 为空")
			}
			for _, id := range ids {
				var opErr error
				switch op {
				case "start":
					opErr = d.Instance.Start(id)
				case "stop":
					opErr = d.Instance.Stop(id)
				case "restart":
					opErr = d.Instance.Restart(id)
				default:
					return fmt.Errorf("op 仅支持 start / stop / restart，实际 %q", op)
				}
				if opErr != nil {
					return fmt.Errorf("实例 %d %s 失败: %w", id, op, opErr)
				}
			}
			return nil
		})
		reg(ex, "instance_delete", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			return d.Instance.Delete(id)
		})
		reg(ex, "instance_send_command", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			cmd, err := payloadString(p, "command")
			if err != nil {
				return err
			}
			return d.Instance.SendCommand(id, cmd)
		})
		reg(ex, "instance_update_config", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			fields, err := updateFieldsFromPayload(p)
			if err != nil {
				return err
			}
			_, err = d.Instance.Update(id, fields)
			return err
		})
	}

	// ── 节点 ──
	if d.Node != nil {
		reg(ex, "node_purge_archived", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			// 归档域的永久删除以归档行的 uuid 标识；这里用 target 里的 ID
			//（票据目标是审批时冻结的，执行时以它为准）。
			id, err := strconv.ParseUint(t.TargetID, 10, 64)
			if err != nil || id == 0 {
				return fmt.Errorf("目标 ID 不合法: %q", t.TargetID)
			}
			_, err = d.Node.Purge(uint(id), true)
			return err
		})
	}

	// ── 文件 ──
	if d.File != nil {
		reg(ex, "file_write_text", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			path, err := payloadString(p, "path")
			if err != nil {
				return err
			}
			content, err := payloadString(p, "content")
			if err != nil {
				return err
			}
			// 改前快照必须在这里补：MCP 正常路径在 callFileWriteText 里有这一步，
			// 审批执行是**绕开工具函数**直接调领域的，漏了就会出现
			// 「批准写入不经快照」——那条文件从此没有可回滚的历史版本，
			// 而 file_rollback 是靠这些版本工作的。两条路径对同一动作必须同义。
			if d.FileVersion != nil {
				if serr := d.FileVersion.SnapshotBeforeWrite(id, path, 0); serr != nil {
					return serr
				}
			}
			return d.File.WriteFile(id, path, []byte(content))
		})
		reg(ex, "file_delete", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			path, err := payloadString(p, "path")
			if err != nil {
				return err
			}
			return d.File.DeleteFile(id, path)
		})
	}

	if d.FileVersion != nil {
		reg(ex, "file_rollback", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			path, err := payloadString(p, "path")
			if err != nil {
				return err
			}
			ver, err := payloadUint(p, "versionId")
			if err != nil {
				return err
			}
			// authorID 用申请人：审计要能追到「谁批准的这次回滚」的起点。
			_, err = d.FileVersion.Rollback(id, path, ver, t.ApplicantUserID)
			return err
		})
	}

	// ── 配置 ──
	if d.Config != nil {
		reg(ex, "config_write_text", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			path, err := payloadString(p, "path")
			if err != nil {
				return err
			}
			content, err := payloadString(p, "content")
			if err != nil {
				return err
			}
			msg, _ := payloadString(p, "message")
			_, _, err = d.Config.Write(id, path, content, msg, t.ApplicantUserID, nil)
			return err
		})
		reg(ex, "config_rollback", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			path, err := payloadString(p, "path")
			if err != nil {
				return err
			}
			ver, err := payloadUint(p, "versionId")
			if err != nil {
				return err
			}
			// 用领域现成的 Rollback，不要自己查版本再拼 Write：
			// 那样会漏掉「版本归属校验」（versionID 必须属于该实例该文件）。
			_, err = d.Config.Rollback(id, path, ver, "审批回滚", t.ApplicantUserID)
			return err
		})
	}

	// ── 节点级聚合操作（等价批量停服，因此与 instance_stop 同审批）──
	if d.Node != nil {
		reg(ex, "node_drain", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			_, err = d.Node.Drain(id)
			return err
		})
		// 进入维护：`SetMaintenance(id, true)`。离开维护是恢复操作，不在审批名单里。
		reg(ex, "node_maintenance_enter", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			_, err = d.Node.SetMaintenance(id, true)
			return err
		})
	}

	// ── Bot ──
	if d.Bot != nil {
		reg(ex, "bot_send_command", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			cmd, err := payloadString(p, "command")
			if err != nil {
				return err
			}
			return d.Bot.SendCommand(id, cmd)
		})
	}

	// ── 分组 / 群组 / 注册关系 ──
	if d.InstanceGroup != nil {
		reg(ex, "instance_group_remove_members", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			ids, err := payloadUintSlice(p, "instanceIds")
			if err != nil {
				return err
			}
			return d.InstanceGroup.RemoveMembers(id, ids)
		})
	}
	if d.Network != nil {
		reg(ex, "network_remove_member", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			instID, err := payloadUint(p, "instanceId")
			if err != nil {
				return err
			}
			return d.Network.RemoveMember(id, instID)
		})
	}
	if d.Registration != nil {
		reg(ex, "registration_delete", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			rid, err := payloadUint(p, "registrationId")
			if err != nil {
				return err
			}
			return d.Registration.Delete(id, rid)
		})
	}

	// ── 一键搭建 ──
	if d.Provision != nil {
		reg(ex, "instance_provision_server", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			req, err := provisionRequestFromPayload(p)
			if err != nil {
				return err
			}
			// 走异步搭建：与面板/工具同一条路径（不在审批里另造一条同步搭建）。
			_, _, err = d.Provision.ProvisionBinaryAsync(ctx, req, t.ApplicantUserID, "")
			return err
		})
	}

	// ── 传输票据签发 ──
	if d.Transfer != nil {
		reg(ex, "file_issue_transfer_ticket", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			direction, err := payloadString(p, "direction")
			if err != nil {
				return err
			}
			path, err := payloadString(p, "path")
			if err != nil {
				return err
			}
			// 必须按**原始 Token** 重建真实主体，不能塞一个空壳：
			// Issue 内部会再跑一次 AuthorizeInstanceAction（并把 p.TokenID 写进票据声明），
			// 空壳 principal 的 TokenID=0 且无任何 scope，必然被判越权——
			// 表现为「批准了却执行失败：agent 操作被拒绝」，看起来像审批坏了。
			//
			// 从票据的 tokenId 加载：人类直接提单时该字段为空，此时无 Agent 主体可重建，
			// 退化为拒绝并给出明确原因（而不是用一个假主体去撞授权）。
			principal, perr := d.loadTicketPrincipal(t)
			if perr != nil {
				return perr
			}
			_, _, err = d.Transfer.Issue(principal, id, direction, path)
			return err
		})
	}

	// ── 快照 / 二进制 ──
	if d.Snapshot != nil {
		reg(ex, "instance_snapshot_rollback", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			snapID, err := payloadUint(p, "snapshotId")
			if err != nil {
				return err
			}
			_, err = d.Snapshot.Rollback(ctx, snapID, t.ApplicantUserID)
			return err
		})
	}
	if d.BinaryVersion != nil {
		reg(ex, "instance_binary_upgrade", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			assetID, err := payloadUint(p, "assetId")
			if err != nil {
				return err
			}
			_, err = d.BinaryVersion.Upgrade(ctx, id, assetID, t.ApplicantUserID)
			return err
		})
		reg(ex, "instance_binary_rollback", func(ctx context.Context, t *model.ApprovalTicket, p map[string]any) error {
			id, err := payloadUint(p, "id")
			if err != nil {
				return err
			}
			_, err = d.BinaryVersion.Rollback(ctx, id, t.ApplicantUserID)
			return err
		})
	}

	// 返回仍未注册的 action：装配层应把它当成待办信号。
	var missing []string
	for _, action := range ApprovalRequiredActionNames() {
		if _, ok := ex.adapters[action]; !ok {
			missing = append(missing, action)
		}
	}
	return missing
}

// reg 注册一个 adapter（nil 安全）。
func reg(ex *ApprovalExecutor, action string, fn ApprovalAdapter) {
	ex.RegisterAdapter(action, fn)
}

// ── 参数取值（缺失即报错，不猜默认值）──────────────────────────────────

func payloadUint(p map[string]any, key string) (uint, error) {
	v, ok := p[key]
	if !ok {
		return 0, fmt.Errorf("缺少参数 %s", key)
	}
	switch n := v.(type) {
	case float64:
		if n < 0 || n != float64(uint64(n)) {
			return 0, fmt.Errorf("参数 %s 须为正整数", key)
		}
		return uint(n), nil
	case int:
		if n < 0 {
			return 0, fmt.Errorf("参数 %s 须为正整数", key)
		}
		return uint(n), nil
	case string:
		id, err := strconv.ParseUint(n, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("参数 %s 不合法", key)
		}
		return uint(id), nil
	default:
		return 0, fmt.Errorf("参数 %s 类型不支持", key)
	}
}

func payloadString(p map[string]any, key string) (string, error) {
	v, ok := p[key]
	if !ok {
		return "", fmt.Errorf("缺少参数 %s", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("参数 %s 须为字符串", key)
	}
	return s, nil
}

func payloadUintSlice(p map[string]any, key string) ([]uint, error) {
	v, ok := p[key]
	if !ok {
		return nil, fmt.Errorf("缺少参数 %s", key)
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("参数 %s 须为数组", key)
	}
	out := make([]uint, 0, len(arr))
	for _, item := range arr {
		n, ok := item.(float64)
		if !ok || n < 0 || n != float64(uint64(n)) {
			return nil, fmt.Errorf("参数 %s 含非整数元素", key)
		}
		out = append(out, uint(n))
	}
	return out, nil
}

// updateFieldsFromPayload 把 MCP 参数映射成 UpdateInstanceFields。
//
// 只映射**明确出现过**的键：未出现的字段保持 nil（= 不变）。
// 不能用零值填充——那会把「没打算改的字段」清空。
func updateFieldsFromPayload(p map[string]any) (UpdateInstanceFields, error) {
	var f UpdateInstanceFields
	if v, ok := p["name"]; ok {
		s, ok := v.(string)
		if !ok {
			return f, fmt.Errorf("参数 name 须为字符串")
		}
		f.Name = &s
	}
	if v, ok := p["startCommand"]; ok {
		s, ok := v.(string)
		if !ok {
			return f, fmt.Errorf("参数 startCommand 须为字符串")
		}
		f.StartCommand = &s
	}
	if v, ok := p["autoStart"]; ok {
		b, ok := v.(bool)
		if !ok {
			return f, fmt.Errorf("参数 autoStart 须为布尔")
		}
		f.AutoStart = &b
	}
	if v, ok := p["autoRestart"]; ok {
		b, ok := v.(bool)
		if !ok {
			return f, fmt.Errorf("参数 autoRestart 须为布尔")
		}
		f.AutoRestart = &b
	}
	if v, ok := p["jdkId"]; ok {
		n, err := payloadUint(map[string]any{"jdkId": v}, "jdkId")
		if err != nil {
			return f, err
		}
		f.JDKID = &n
	}
	return f, nil
}

// provisionRequestFromPayload 从冻结参数重建一键搭建请求。
// 这里走「先 marshal 再 Unmarshal」而非逐字段取值：搭建参数面宽（含嵌套 binarySource），
// 手取既冗长又容易漏字段，复用 json tag 最稳妥。
// 与 file_issue_transfer_ticket 的逐字段取值不同是有意的：票据参数只有 3 个且都是标量，
// 手取更直观；搭建参数嵌套且字段多，交给 json 更安全。
func provisionRequestFromPayload(p map[string]any) (ProvisionServerRequest, error) {
	var req ProvisionServerRequest
	raw, err := json.Marshal(p)
	if err != nil {
		return req, fmt.Errorf("序列化搭建参数失败: %w", err)
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return req, fmt.Errorf("解析搭建参数失败: %w", err)
	}
	return req, nil
}
