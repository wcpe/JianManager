package service

import (
	"errors"
	"log/slog"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 就地导入后的自动收养（ADR-069 补充）。
//
// ## 为什么要在导入里自动做，而不是让运维再去点“接管”
// 就地导入的定义就是「这个目录里已经有一份服，我现在要纳管它」。既然如此，
// “目录里有活进程”就不是异常而是**预期状态**——还要求运维再手动点一次接管，
// 等于把一件必然发生的事拆成两步，而且中间那段时间面板上写着 STOPPED、玩家却能进。
//
// ## 为什么不是直接 start
// 因为它已经在跑了。start 会撞端口（预检会拦，绕过预检就会拉起第二个实例——
// 真实机实测过：6 台服全部“崩溃×5→熔断”，而 tmux 里的原服未受任何影响）。
// 正确的动作是“承认它”：只登记、不重启。
//
// ## 失败一律不阻断导入
// 目录停着（ErrAdoptInPlaceNoRuntime）是正常结果；节点瞬时不可达、归属复核不通过等
// 也只是“没自动纳管”，实例仍以 STOPPED 存在，运维之后可手动接管或直接启动。
// 导入是个重操作（探测 + 建实例 + 登记 JDK），不能因为“顺便做的收养”失败而整体回滚。

// adoptInPlaceAfterImport 导入后尝试收养目录里已在运行的外部进程。
//
// 调用前 inst 必须已创建成功（已入库）。本方法**不返回错误**：收养失败只记日志，
// 不影响导入结果——理由见文件头注释。
func (s *ImportServerService) adoptInPlaceAfterImport(inst *model.Instance) {
	if inst == nil || s.instance == nil {
		return
	}
	pid, err := s.instance.AdoptInPlaceRuntime(inst.ID)
	switch {
	case err == nil:
		slog.Info("就地导入后已收养目录里的运行进程（未重启，状态已置 RUNNING）",
			"instanceId", inst.UUID, "name", inst.Name, "pid", pid)
	case errors.Is(err, ErrAdoptInPlaceNoRuntime):
		// 目录本来就是停着的：导入成 STOPPED 是正确结果。
		slog.Info("就地导入完成：目录下无运行中的进程，实例保持 STOPPED",
			"instanceId", inst.UUID, "name", inst.Name)
	default:
		// 节点不可达 / 归属复核不通过 / 进程命令行不匹配：都不阻断导入。
		// 实例已经在库里了，运维可以在面板上重试接管或直接启动。
		slog.Warn("就地导入后的自动收养未能完成（不阻断导入，实例保持 STOPPED，可手动接管）",
			"instanceId", inst.UUID, "name", inst.Name, "error", err)
	}

	// 收养完再下发探针（jar + config + 托管身份）。
	//
	// 顺序刻意如此：探针身份里的 credential 是实例级的，下发时机不影响它；
	// 但先收养能让“实例已 RUNNING”与“探针开始推送指标”在时间上接得上，
	// 不必等一轮心跳才发现监控起来了。
	//
	// 不下发也不阻断：目录里可能已有一套手工放好的探针（旧部署），
	// 而“能不能纳管”与“有没有探针”是两件事。
	s.deployProbeAfterImport(inst)
}

// deployProbeAfterImport 为导入的实例下发探针（含托管身份）。
//
// 抽出来单独一个方法是为了让主流程读起来不被探针细节淹没；错误只记日志不返回。
func (s *ImportServerService) deployProbeAfterImport(inst *model.Instance) {
	if s.probe == nil {
		slog.Debug("未注入探针下发器，跳过导入后下发（保持目录里的现有探针文件）",
			"instanceId", inst.UUID)
		return
	}
	var node model.Node
	if err := s.db.First(&node, inst.NodeID).Error; err != nil {
		slog.Warn("导入后下发探针：查节点失败（跳过）", "instanceId", inst.UUID, "error", err)
		return
	}
	if err := s.probe.DeployProbeForInstance(inst, &node, ""); err != nil {
		slog.Warn("导入后下发探针失败（不阻断导入：目录里现有探针仍可用）",
			"instanceId", inst.UUID, "name", inst.Name, "error", err)
		return
	}
	slog.Info("导入后已向实例下发探针（含托管身份）",
		"instanceId", inst.UUID, "name", inst.Name)
}
