package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 探针下发对“已存在实例”的公开入口（建服 / 导入共用）。
//
// ## 为什么要单独开一个方法
// `deployServerProbe` 是建服内部用的（带 ctx、需要 baseURL），而导入路径已经走完了 HTTP 请求、
// 手里只有实例与节点。两边的共同点是：都必须能拿到“跟建服一模一样的探针配置”。
// 所以这里只做一层薄封装（补 context / 补依赖），**不重新实现下发逻辑**——
// 两条路径必须产出同形的产物，否则同一台机在不同时机建/导会有两种行为。

// DeployProbeForInstance 为一个已存在的实例下发探针（jar + config + 托管身份）。
//
// ## 失败语义
// 返回 error 由调用方决定是否阻断。当前调用方（导入）一律**不阻断**：
// 探针是监控增强，不是“能不能纳管”的前提；而下发失败在导入这种重操作里
// 不该把已经建好的实例回滚掉。
//
// ## baseURL
// 探针下载地址的公开基址。空串时由制品服务自行推断（与建服同口径）。
func (p *ProvisionService) DeployProbeForInstance(inst *model.Instance, node *model.Node, baseURL string) error {
	if p == nil || inst == nil || node == nil {
		return nil
	}
	// baseURL 为空时回退平台公共基址。
	//
	// 导入路径拿不到 HTTP 请求上下文（它可能来自后台任务、也可能来自 API），
	// 而拼探针下载 URL **必须有**一个 Worker 能回拉的绝对基址——空串会直接报
	// “CP ServerProbe 下载基址或参数无效”（真机就是这个报错，导入后探针一直没下发）。
	// 与二进制分发同口径：请求基址 > platform.public_base_url 设置。
	if strings.TrimSpace(baseURL) == "" {
		baseURL = p.platformPublicBaseURL()
	}
	if strings.TrimSpace(baseURL) == "" {
		slog.Warn("未配置 CP 公共基址，跳过探针下发（请在平台设置里填 platform.public_base_url）",
			"instance", inst.UUID, "name", inst.Name)
		return errors.New("未配置 platform.public_base_url，无法拼探针下载地址")
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeDeployTimeout)
	defer cancel()
	if err := p.deployServerProbe(ctx, inst, node, baseURL); err != nil {
		slog.Warn("为已存在实例下发探针失败（不阻断主流程）",
			"instance", inst.UUID, "name", inst.Name, "err", err)
		return err
	}
	return nil
}
