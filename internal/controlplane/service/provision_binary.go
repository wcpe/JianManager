package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 通用二进制运行时搭建（FR-441，见 ADR-090）。
//
// 与 MC 核心搭建的分工：paper/sponge/velocity 走 CoreService.ResolveBuild 的「版本 → 构建 →
// 下载 URL」三段解析；binary 跳过全部三段，由请求直供制品来源（BinarySource），
// 因而 CoreService 对 binary 直接短路（ErrBinaryCoreNotResolvable）。
//
// 强制异步：二进制体积大（Beacon 约 30MB）、慢链路下载可达分钟级，且需下载进度与终态站内信。
// 故沿用 FR-319 已验证的两段式——同步段只做「校验来源 + 建实例 + 登记任务」，取件在 CP 后台
// goroutine 经 Worker FetchBinary 推进。绝不提供同步搭建入口。
//
// FR-442 在本文件之上叠一层 Beacon 预设（另见 core.go 的 ResolveBeaconRelease）：
// coreType=beacon 是语法糖，展开为 binary + 固定参数（落盘名/启动命令/角色），
// 制品来源按「制品库 > GitHub Releases」自动解析，见 resolveBeaconPresetSource。

// BinarySourceKind 是二进制制品的来源类别（spec §3.2）。
type BinarySourceKind string

const (
	// BinarySourceAsset 复用制品库既有 asset：CP 经制品库校验后签发短期分发 token，Worker 从 CP 拉取。
	// 优点是入库即经过 sha256 校验，且复用既有签名分发通道。
	BinarySourceAsset BinarySourceKind = "asset"
	// BinarySourceURL 远程地址，由 Worker 直连下载（避免 CP 中转大文件）+ 可选 sha256 校验。
	BinarySourceURL BinarySourceKind = "url"
	// BinarySourceNodeFile 节点本地文件，Worker 就地复制到工作目录。
	// 路径须过受控放行校验（见 validateBinaryNodePath），防越权读任意文件。
	BinarySourceNodeFile BinarySourceKind = "node_file"
)

const (
	// binaryFetchTimeout 是一次二进制取件的自限超时。二进制可达数十 MB，取 30 分钟口径与
	// ServerProbe 缓存下载/备份等长耗时操作一致，覆盖慢链路又不无限悬挂。
	binaryFetchTimeout = 30 * time.Minute
	// binaryDiskDigestTimeout 是版本视图里磁盘 sha256 比对的超时（FR-468 §2.5）。
	// 比对要流式读整个二进制（上限 binaryDiskDigestMaxBytes），故比普通读文件宽；
	// 但版本视图是交互式读接口，仍需比取件短得多，避免详情页被一次哈希拖住。
	binaryDiskDigestTimeout = 2 * time.Minute
)

// 阶段进度常量（spec §3.3 的表格）。
const (
	binaryStageResolve   = 5
	binaryStageFetchFrom = 20
	binaryStageFetchTo   = 80
	binaryStageVerify    = 85
	binaryStageInstall   = 90
	binaryStageDeriveCmd = 95
)

// BinarySource 描述 binary 搭建的制品来源（spec §3.1）。
// 三类来源互斥：由 Kind 决定使用哪组字段，其余字段被忽略。
type BinarySource struct {
	Kind BinarySourceKind `json:"kind"`
	// AssetID 是制品库既有 asset 的 ID（kind=asset 必填）。
	// 指向内容寻址的制品记录，CP 据此解析 sha256 / 文件名并签发短期分发 URL。
	AssetID uint `json:"assetId,omitempty"`
	// URL 是远程下载地址（kind=url 必填；须 https）。
	URL string `json:"url,omitempty"`
	// SHA256 是内容校验（kind=url / node_file 可选；kind=asset 忽略——摘要由制品库接管）。
	SHA256 string `json:"sha256,omitempty"`
	// NodePath 是节点上的源文件绝对路径（kind=node_file 必填）。
	NodePath string `json:"nodePath,omitempty"`
	// Filename 是落盘文件名（相对实例工作目录），如 beacon-1.1.0-linux-amd64。
	// 该名同时是 startCommand 的派生依据（spec §3.4：默认 ./<filename>）。
	Filename string `json:"filename"`
	// Executable 是否置可执行位；nil 视为 true（二进制默认需要可执行）。
	Executable *bool `json:"executable,omitempty"`
}

// ExecutableOrDefault 返回是否置可执行位（未显式指定时为 true）。
func (s BinarySource) ExecutableOrDefault() bool { return s.Executable == nil || *s.Executable }

// binaryFetchPlan 是来源解析后的「Worker 侧可执行取件计划」。
// 把三类来源归一为 Worker 能理解的两类（url / node_file），使 Worker 无需感知制品库语义。
type binaryFetchPlan struct {
	SourceKind  string // url / node_file，对齐 workerpb.FetchBinaryRequest.source_kind
	DownloadURL string
	SHA256      string
	NodePath    string
	Filename    string
	Executable  bool
	// AssetID 制品库来源的资产 ID（FR-468）：非 0 时该 plan 可登记为版本绑定，
	// 使重建能冻结版本、并提供受控升级/回滚。url / node_file 来源为 0。
	AssetID uint
}

// binarySourceOf 取出请求里的制品来源（nil 视为未提供，由来源校验给出「缺少 kind」错误）。
func binarySourceOf(req ProvisionServerRequest) BinarySource {
	if req.BinarySource == nil {
		return BinarySource{}
	}
	return *req.BinarySource
}

// ---- Beacon 预设（FR-442，见 ADR-090）----
//
// coreType=beacon 是 coreType=binary 之上的语法糖：把「Beacon 该用哪个二进制、叫什么名字、
// 怎么启动、什么角色」这几件固定事实内置，用户不必知道这些细节就能一键搭出可用实例。
//
// 展开后与 binary 完全同路：同样经 Worker FetchBinary 取件、同样落 startCommand、
// 同样不绑 JDK、同样异步 + 损毁可重建。差异只有三处：
//  1. 制品来源自动解析（resolveBeaconPresetSource）而非请求直供；
//  2. 角色为 beacon（FR-433）而非 universal；
//  3. 落盘名固定为 beacon-{version}-linux-amd64（不可由请求覆盖），启动命令由其派生。

// beaconPlaceholderPlan 是来源解析失败时的占位取件计划。
//
// 为什么需要它：FR-441 的既有语义是「来源不合法 → 立刻失败且不建实例」，因为那类错误
// （kind 缺失、url 非 https、node_file 越界）用户改一下请求就能修好。Beacon 预设的来源是
// **系统自动解析**的，失败原因多为「内网访问不了 GitHub 且制品库没有 beacon 制品」这类
// 需要运维介入的环境问题——此时若按「同步段立即失败」处理，用户拿不到任何可观察痕迹
// （没有实例、没有任务、没有站内信），只能反复重试。故此处建实例 + 登记任务，
// 把失败原因作为**任务终态 + 实例 DAMAGED 原因**呈现，保留 FR-441 的「取件失败 → 损毁可重建」闭环。
//
// 该 plan 永不真正下发：后台段在取件前先用 resolveErr 短路，不会发起下载。
func beaconPlaceholderPlan() *binaryFetchPlan {
	return &binaryFetchPlan{
		SourceKind: string(BinarySourceURL),
		Executable: true,
	}
}

// resolveBeaconPresetSource 按 spec §4.1 的优先级解析 Beacon 制品来源：
//
//  1. 制品库已有 beacon 制品（最新版本）→ 优先；
//  2. 回落 GitHub Releases（wcpe/Beacon）→ 取最新正式发布的 linux-amd64 产物。
//
// 优先制品库的两个理由（spec §4.1）：内网环境往往访问不了 GitHub；且已入库的字节
// **经过 sha256 校验**，可信度高于「刚从一个外部地址拉下来的东西」。
//
// 返回的 plan 直接交付 Worker（asset 归一到签名 URL 形态，与 binary 路径同构），
// 以及人工可读的来源描述（进任务标题与阶段文案，让运维一眼看出这次用的是哪条来源）。
func (p *ProvisionService) resolveBeaconPresetSource(ctx context.Context, requestBaseURL string) (*binaryFetchPlan, string, error) {
	// 第一顺位：制品库里的 beacon 制品。
	if plan, sourceText, err := p.resolveBeaconArtifactFromLibrary(requestBaseURL); err != nil {
		return nil, "", err
	} else if plan != nil {
		return plan, sourceText, nil
	}
	// 第二顺位：GitHub Releases。
	if p.core == nil {
		return nil, "", errors.New("核心解析服务未装配，无法回落 GitHub 获取 Beacon 二进制")
	}
	release, err := p.core.ResolveBeaconRelease(ctx)
	if err != nil {
		return nil, "", err
	}
	filename := beaconBinaryFilename(release.Version)
	return &binaryFetchPlan{
		SourceKind:  string(BinarySourceURL),
		DownloadURL: release.DownloadURL,
		SHA256:      release.SHA256,
		Filename:    filename,
		Executable:  true,
	}, fmt.Sprintf("GitHub Releases %s（%s）", beaconReleaseRepo, release.Tag), nil
}

// resolveBeaconArtifactFromLibrary 在制品库中找最新的 Beacon 二进制制品。
//
// 找不到时返回 (nil, "", nil) 而非错误——「制品库没有」是预期内的常态（首次部署往往
// 直接走 GitHub），由调用方继续回落，不该把它当成失败。
//
// 检索口径：制品库中文件名形如 beacon-*-linux-amd64 的制品，取最新一条。
// 判据与 GitHub 产物筛选共用 isBeaconLinuxBinaryName——同一条规则若在两处各写一套，
// 同一份文件可能在「制品库命中」与「发布不命中」之间得出相反结论，排查时无从解释。
func (p *ProvisionService) resolveBeaconArtifactFromLibrary(requestBaseURL string) (*binaryFetchPlan, string, error) {
	asset, err := p.latestBeaconAsset()
	if err != nil {
		return nil, "", err
	}
	if asset == nil {
		return nil, "", nil
	}
	if p.artifactVersions == nil {
		return nil, "", errors.New("制品版本库服务未装配，无法以制品库为来源搭建 Beacon 实例")
	}
	token, err := p.artifactVersions.IssueBinaryDownloadToken(BinaryDownloadTokenScope{AssetID: asset.ID})
	if err != nil {
		return nil, "", fmt.Errorf("签发制品分发 token 失败: %w", err)
	}
	downloadURL, err := p.BuildBinaryDownloadURLForRequest(asset.ID, token, requestBaseURL)
	if err != nil {
		return nil, "", err
	}
	// 摘要以制品库为准（内容寻址）：入库时已校验过，比请求或外部声明更可信。
	return &binaryFetchPlan{
		SourceKind:  string(BinarySourceURL),
		DownloadURL: downloadURL,
		SHA256:      strings.ToLower(strings.TrimSpace(asset.SHA256)),
		Filename:    beaconAssetFilename(asset),
		Executable:  true,
		AssetID:     asset.ID,
	}, fmt.Sprintf("制品库 asset#%d（%s）", asset.ID, asset.Filename), nil
}

// beaconAssetFilename 决定制品库来源的落盘名（同时决定启动命令）。
//
// 优先沿用资产原始名：运维上传 `beacon-1.1.0-linux-amd64` 时，落盘名与启动命令
// （`./beacon-1.1.0-linux-amd64`）就与生产实例既有运行方式完全一致，运维不必二次核对。
// 原始名不合预设形态时（如不带版本号的 `beacon-linux-amd64`）改用
// `beacon-{asset.Version}-linux-amd64`；版本也缺失时用资产 ID 兜底，保证名始终合法可区分。
func beaconAssetFilename(asset *model.Asset) string {
	name := strings.TrimSpace(asset.Filename)
	if isBeaconLinuxBinaryName(name) && validBinaryFilename(name) {
		return name
	}
	version := strings.TrimSpace(asset.Version)
	if version == "" || !validBinaryFilename(beaconBinaryFilename(version)) {
		version = fmt.Sprintf("asset%d", asset.ID)
	}
	return beaconBinaryFilename(version)
}

// latestBeaconAsset 在制品库中挑出最新的 Beacon Linux 二进制制品。
//
// 「最新」= id 最大（入库时间新→旧，与 ListVersions 的 id 倒序同口径）。
// 制品库是内容寻址的（同 (type, sha256) 复用同一条记录），故同一份文件天然只有一条记录，
// 不存在「同版本多份」需要进一步排序的情况。
//
// 只接受可交付的制品：尺寸 >0 且非 lost（外置对象缺失）。选中一个空文件或失效记录，
// 只会让搭建在触发下载后才发现不可用，不如直接继续回落 GitHub。
func (p *ProvisionService) latestBeaconAsset() (*model.Asset, error) {
	if p.db == nil {
		return nil, nil
	}
	// LIKE 只是粗筛（走不上索引也无妨：制品库规模有界），精确判定交给 isBeaconLinuxBinaryName。
	var assets []model.Asset
	if err := p.db.Where("filename LIKE ?", "beacon%linux-amd64%").
		Order("id DESC").Limit(50).Find(&assets).Error; err != nil {
		return nil, fmt.Errorf("查询 Beacon 制品失败: %w", err)
	}
	for i := range assets {
		if !isBeaconLinuxBinaryName(assets[i].Filename) {
			continue
		}
		if assets[i].Size <= 0 || assets[i].StorageState == model.AssetStorageLost {
			continue
		}
		asset := assets[i]
		return &asset, nil
	}
	return nil, nil
}

// ProvisionBinaryAsync 通用二进制异步搭建入口（FR-441）；coreType=beacon 时为 Beacon 预设（FR-442）。
//
// 两段式（沿用 FR-319 已验证形态）：
//   - 同步段：解析/校验制品来源 → 建实例（STOPPED）→ 存 provisionSpec →
//     登记 binary_provision 任务 → 立即返回 {instance, taskId}；
//   - 后台段：CP 后台 goroutine 经 Worker FetchBinary 取件，按字节进度上报阶段，
//     失败进 DAMAGED 且保留 provisionSpec 供重建。
//
// 绝不提供同步路径：二进制体积大、下载可达分钟级，同步会阻塞请求且前端无法区分
// 「卡住」与「在下载」（spec §3.3）。requestBaseURL 为请求可见的 CP 公共基址（asset 来源用）。
//
// 两条路径在**来源解析**上刻意不同（这是唯一的实质差异）：
//   - binary：来源由请求直供，不合法立即失败且不建实例（用户改请求即可修好）；
//   - beacon：来源由系统按优先级自动解析，失败不阻塞建实例/登记任务，
//     而是进任务终态与实例 DAMAGED 原因（原因多为环境问题，需运维介入，需要可观察痕迹）。
func (p *ProvisionService) ProvisionBinaryAsync(ctx context.Context, req ProvisionServerRequest, createdBy uint, requestBaseURL string) (*model.Instance, string, error) {
	if IsBeaconCore(req.CoreType) {
		return p.provisionBeaconPresetAsync(ctx, req, createdBy, requestBaseURL)
	}
	// 先校验用户输入（无副作用），再查运行环境：这样「来源写错了」永远得到可操作的输入错误，
	// 而不会因为环境未就绪被遮蔽成「需要任务中心」这类与用户无关的提示。
	plan, sourceText, err := p.resolveBinarySource(binarySourceOf(req), requestBaseURL)
	if err != nil {
		slog.Error("二进制搭建失败：解析制品来源", "name", req.Name, "error", err)
		return nil, "", err
	}
	if p.tasks == nil {
		// 与 MC 路径的取舍不同：彼处无任务中心时回退同步（保持旧行为），此处 binary 是新增能力，
		// 没有需要兼容的旧行为，而「同步下载 30MB」正是本 FR 要消除的形态，故直接拒绝而非降级。
		return nil, "", errors.New("二进制搭建需要任务中心支撑（强制异步），当前未装配任务服务")
	}
	inst, err := p.createBinaryInstance(req, plan)
	if err != nil {
		slog.Error("二进制搭建失败：创建实例", "name", req.Name, "error", err)
		return nil, "", err
	}

	// 存搭建参数供损毁后「重建」复用（FR-342 同语义：修好网络/换好文件点重建即复用重跑）。
	// 注意不落私有基址——重建时基址重新解析，冻结旧基址会在面板搬迁/换域名后失效。
	if specJSON, mErr := json.Marshal(req); mErr == nil {
		_ = p.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
			Update("provision_spec", string(specJSON)).Error
	}
	// FR-468：登记版本绑定（asset 来源记 assetID；url/node_file 记落盘名+摘要）。
	p.recordBinaryBindingOnProvision(inst, plan, plan.AssetID)
	p.markBinaryProvisioning(inst.ID)

	title := fmt.Sprintf("二进制搭建 %s（%s）", inst.Name, sourceText)
	taskID := p.tasks.RunAsync(RunSpec{
		NodeID: req.NodeID, InstanceID: inst.ID, Kind: model.TaskKindBinaryProvision,
		Title: title, CreatedBy: createdBy,
		// 体积大、慢链路可数分钟；超时取比取件自限略宽的口径，使取件自身先超时并给出具体原因
		// （下载超时/连接中断），而不是任务被外层截断成笼统的 context deadline。
		Timeout: binaryFetchTimeout + 5*time.Minute,
	}, func(ctx context.Context, stage func(int, string)) (string, error) {
		stage(binaryStageResolve, "解析制品来源…")
		if err := p.provisionBinaryOnWorker(ctx, inst, plan, stage); err != nil {
			// 取件失败 → 损毁态：statusReason 让实例卡片/详情可见「为什么不可用」，
			// 启动前一目了然（损毁态亦拦启动）；provisionSpec 已在库可重建。
			p.markBinaryInstanceDamaged(inst.ID, "搭建未完成："+err.Error())
			return "", err
		}
		stage(binaryStageDeriveCmd, "派生启动命令…")
		_ = p.db.Model(&model.Instance{}).Where("id = ?", inst.ID).Update("status_reason", "").Error
		slog.Info("二进制搭建完成", "instance", inst.Name, "instanceId", inst.ID, "source", plan.SourceKind)
		return "", nil
	})
	if taskID == "" {
		return inst, "", errors.New("登记二进制搭建任务失败")
	}
	return inst, taskID, nil
}

// provisionBeaconPresetAsync 是 Beacon 预设的异步搭建（FR-442）。
//
// 与 binary 路径的差异（除来源自动解析外）：
//   - 角色落 beacon（FR-433）：不参与 MC 群组拓扑，避免被当成后端子服；
//   - 落盘名固定 beacon-{version}-linux-amd64，启动命令由其派生（用户无需知道这些细节）。
//
// 来源解析在同步段完成，但**解析失败不在此中断**（见 beaconPlaceholderPlan 的说明）：
// 建实例 + 登记任务后把原因作为任务终态呈现，用户能看到「为什么没搭起来」而不是面对一个静默失败。
func (p *ProvisionService) provisionBeaconPresetAsync(ctx context.Context, req ProvisionServerRequest, createdBy uint, requestBaseURL string) (*model.Instance, string, error) {
	plan, sourceText, resolveErr := p.resolveBeaconPresetSource(ctx, requestBaseURL)
	if resolveErr != nil {
		// 来源解析失败：仍建实例 + 登记任务，把原因落到任务终态与实例原因（可观察、可重建）。
		slog.Warn("Beacon 预设搭建：解析制品来源失败，转由任务终态呈现", "name", req.Name, "error", resolveErr)
		plan = beaconPlaceholderPlan()
		sourceText = "来源解析失败"
	}
	if p.tasks == nil {
		// 与 binary 同口径：二进制搭建拒绝降级为同步（「同步下 30MB」正是本 FR 要消除的形态）。
		// 此处的额外好处是——解析失败也必须走任务才能保留原因，没有任务中心就无从呈现。
		if resolveErr != nil {
			return nil, "", resolveErr
		}
		return nil, "", errors.New("Beacon 搭建需要任务中心支撑（强制异步），当前未装配任务服务")
	}
	inst, err := p.createBeaconInstance(req, plan)
	if err != nil {
		slog.Error("Beacon 预设搭建失败：创建实例", "name", req.Name, "error", err)
		return nil, "", err
	}
	// 存搭建参数供损毁后重建（FR-342 同语义）：beacon 重跑时**重新解析来源**，
	// 因而这里落的是请求原文（coreType=beacon），不冻结本次选中的具体制品——否则
	// 重建会一直钉在旧版本/旧地址上，与「制品库优先」的意图相反。
	if specJSON, mErr := json.Marshal(req); mErr == nil {
		_ = p.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
			Update("provision_spec", string(specJSON)).Error
	}
	// FR-468：登记版本绑定（Beacon 制品库来源记 assetID；GitHub/占位来源记落盘名+摘要）。
	p.recordBinaryBindingOnProvision(inst, plan, plan.AssetID)
	p.markBinaryProvisioning(inst.ID)

	title := fmt.Sprintf("Beacon 搭建 %s（%s）", inst.Name, sourceText)
	taskID := p.tasks.RunAsync(RunSpec{
		NodeID: req.NodeID, InstanceID: inst.ID, Kind: model.TaskKindBinaryProvision,
		Title: title, CreatedBy: createdBy, Timeout: binaryFetchTimeout + 5*time.Minute,
	}, func(ctx context.Context, stage func(int, string)) (string, error) {
		stage(binaryStageResolve, "解析制品来源…")
		if resolveErr != nil {
			p.markBinaryInstanceDamaged(inst.ID, "搭建未完成："+resolveErr.Error())
			return "", resolveErr
		}
		if err := p.provisionBinaryOnWorker(ctx, inst, plan, stage); err != nil {
			p.markBinaryInstanceDamaged(inst.ID, "搭建未完成："+err.Error())
			return "", err
		}
		stage(binaryStageDeriveCmd, "派生启动命令…")
		_ = p.db.Model(&model.Instance{}).Where("id = ?", inst.ID).Update("status_reason", "").Error
		slog.Info("Beacon 实例搭建完成",
			"instance", inst.Name, "instanceId", inst.ID, "source", plan.SourceKind, "binary", plan.Filename)
		return "", nil
	})
	if taskID == "" {
		return inst, "", errors.New("登记 Beacon 搭建任务失败")
	}
	return inst, taskID, nil
}

// binaryProvisionRunner 组装 binary 路径的任务执行体（首次搭建用；重建走 rebuildBinaryInstance）。
// 抽成函数仅为让两个入口共用同一段编排与失败语义，避免「首次」与「重建」各自漂移。
func (p *ProvisionService) binaryProvisionRunner(inst *model.Instance, plan *binaryFetchPlan, _ bool) func(context.Context, func(int, string)) (string, error) {
	return func(ctx context.Context, stage func(int, string)) (string, error) {
		stage(binaryStageResolve, "解析制品来源…")
		if err := p.provisionBinaryOnWorker(ctx, inst, plan, stage); err != nil {
			// 取件失败 → 损毁态：statusReason 让实例卡片/详情可见「为什么不可用」，
			// 启动前一目了然（损毁态亦拦启动）；provisionSpec 已在库可重建。
			p.markBinaryInstanceDamaged(inst.ID, "搭建未完成："+err.Error())
			return "", err
		}
		stage(binaryStageDeriveCmd, "派生启动命令…")
		_ = p.db.Model(&model.Instance{}).Where("id = ?", inst.ID).Update("status_reason", "").Error
		slog.Info("二进制搭建完成", "instance", inst.Name, "instanceId", inst.ID, "source", plan.SourceKind)
		return "", nil
	}
}

// markBinaryProvisioning 标注「搭建中」（binary 与 beacon 共用）：
// 实例卡片/详情可见状态，配合启动闸阻止过早启动——否则用户可能在二进制尚未落地时就点启动，
// 得到 command not found。
func (p *ProvisionService) markBinaryProvisioning(instanceID uint) {
	_ = p.db.Model(&model.Instance{}).Where("id = ?", instanceID).
		Update("status_reason", "搭建中：正在获取二进制（完成前请勿启动）").Error
}

// createBeaconInstance 建 Beacon 预设实例（FR-442 §4.2）：在 binary 实例属性之上把角色落 beacon。
//
// 其余默认值全与 binary 一致：generic 类型（非 MC Java 进程）、daemon、不绑 JDK、
// startCommand 承载启动命令——预设只改「角色」一处，避免同一语义在两条路径上各有一套默认值。
func (p *ProvisionService) createBeaconInstance(req ProvisionServerRequest, plan *binaryFetchPlan) (*model.Instance, error) {
	startCommand := strings.TrimSpace(req.StartCommand)
	if startCommand == "" {
		startCommand = deriveBinaryStartCommand(plan.Filename)
	}
	releasePortAlloc := p.instance.lockNodePortAlloc()
	defer releasePortAlloc()
	// Beacon 非 MC Java 服务端，探针（Bukkit 插件）不适用，不分配探针端口（FR-454）。
	ports, err := allocPortsForNode(p.db, req.NodeID, false)
	if err != nil {
		return nil, err
	}
	return p.instance.Create(CreateInstanceRequest{
		NodeID:       req.NodeID,
		Name:         req.Name,
		Type:         model.InstanceTypeGeneric,
		Role:         model.InstanceRoleBeacon,
		ProcessType:  model.ProcessTypeDaemon,
		JDKID:        0,
		StartCommand: startCommand,
		ServerPort:   ports.ServerPort,
		QueryPort:    ports.QueryPort,
		AutoRestart:  true,
		GroupID:      req.GroupID,
	})
}

// resolveBinarySource 校验并归一化制品来源（同步段执行：来源不合法立即失败，不建实例）。
// 三类来源的校验口径见 validateBinaryNodePath / validateBinaryURL / resolveBinaryAsset。
// requestBaseURL 是本次请求可见的 CP 公共基址（asset 来源据此拼签名下载 URL）。
// 返回的 plan 供后台段经 Worker 执行；同时返回人工可读的来源描述（任务标题/阶段文案用）。
func (p *ProvisionService) resolveBinarySource(src BinarySource, requestBaseURL string) (*binaryFetchPlan, string, error) {
	kind := BinarySourceKind(strings.ToLower(strings.TrimSpace(string(src.Kind))))
	// 先判 kind：kind 缺失/未知时不应先报「文件名非法」（用户还没说要什么来源，
	// 报文件名只会误导——先给「缺少 kind」才是可操作的诊断）。
	switch kind {
	case BinarySourceAsset, BinarySourceURL, BinarySourceNodeFile:
	case "":
		return nil, "", errors.New("缺少 binarySource.kind（可选 asset / url / node_file）")
	default:
		return nil, "", fmt.Errorf("不支持的 binarySource.kind: %s（可选 asset / url / node_file）", src.Kind)
	}

	filename := strings.TrimSpace(src.Filename)
	if !validBinaryFilename(filename) {
		return nil, "", fmt.Errorf("非法的落盘文件名 %q：须为不含路径分隔符/空白的纯文件名（如 beacon-1.1.0-linux-amd64）", src.Filename)
	}
	sha := normalizeSHA256(src.SHA256)
	if sha != "" && !validSHA256Hex(sha) {
		return nil, "", errors.New("sha256 格式非法（须为 64 位十六进制）")
	}

	switch kind {
	case BinarySourceAsset:
		asset, err := p.resolveBinaryAsset(src.AssetID)
		if err != nil {
			return nil, "", err
		}
		if p.artifactVersions == nil {
			return nil, "", errors.New("制品版本库服务未装配，无法以 asset 为来源搭建二进制实例")
		}
		token, err := p.artifactVersions.IssueBinaryDownloadToken(BinaryDownloadTokenScope{AssetID: asset.ID})
		if err != nil {
			return nil, "", fmt.Errorf("签发制品分发 token 失败: %w", err)
		}
		downloadURL, err := p.BuildBinaryDownloadURLForRequest(asset.ID, token, requestBaseURL)
		if err != nil {
			return nil, "", err
		}
		// 摘要以制品库为准（内容寻址），忽略请求内可能填错的 sha256——否则自相矛盾的值会让
		// 校验永远失败，且掩盖真实的库内摘要。
		return &binaryFetchPlan{
			SourceKind:  string(BinarySourceURL),
			DownloadURL: downloadURL,
			SHA256:      strings.ToLower(strings.TrimSpace(asset.SHA256)),
			Filename:    filename,
			Executable:  src.ExecutableOrDefault(),
			AssetID:     asset.ID,
		}, fmt.Sprintf("制品库 asset#%d（%s）", asset.ID, asset.Filename), nil

	case BinarySourceURL:
		raw := strings.TrimSpace(src.URL)
		if err := validateBinaryURL(raw); err != nil {
			return nil, "", err
		}
		return &binaryFetchPlan{
			SourceKind:  string(BinarySourceURL),
			DownloadURL: raw,
			SHA256:      sha,
			Filename:    filename,
			Executable:  src.ExecutableOrDefault(),
		}, fmt.Sprintf("远程地址 %s", raw), nil

	case BinarySourceNodeFile:
		clean := filepath.Clean(strings.TrimSpace(src.NodePath))
		if err := p.validateBinaryNodePath(clean); err != nil {
			return nil, "", err
		}
		return &binaryFetchPlan{
			SourceKind: string(BinarySourceNodeFile),
			NodePath:   clean,
			SHA256:     sha,
			Filename:   filename,
			Executable: src.ExecutableOrDefault(),
		}, fmt.Sprintf("节点本地文件 %s", clean), nil

	case "":
		return nil, "", errors.New("缺少 binarySource.kind（可选 asset / url / node_file）")
	default:
		return nil, "", fmt.Errorf("不支持的 binarySource.kind: %s（可选 asset / url / node_file）", src.Kind)
	}
}

// resolveBinaryAsset 取指定 asset 并确认其可交付。
func (p *ProvisionService) resolveBinaryAsset(assetID uint) (*model.Asset, error) {
	if assetID == 0 {
		return nil, errors.New("kind=asset 时缺少 assetId")
	}
	if p.binaryAssets == nil {
		return nil, errors.New("制品库服务未装配，无法以 asset 为来源搭建二进制实例")
	}
	asset, err := p.binaryAssets.GetByID(assetID)
	if err != nil {
		return nil, fmt.Errorf("制品库 asset#%d 不可用: %w", assetID, err)
	}
	if asset.Size <= 0 {
		return nil, fmt.Errorf("制品库 asset#%d 内容为空，无法作为二进制来源", assetID)
	}
	if asset.StorageState == model.AssetStorageLost {
		return nil, fmt.Errorf("制品库 asset#%d 已标记失效（外置对象缺失），请先重传同内容文件再搭建", assetID)
	}
	return asset, nil
}

// validateBinaryURL 校验远程下载地址：须为 https（防二进制被中间人替换）。
// 与自更新/JDK 下载源同口径：不接受明文 http 与其它协议（file/ftp 等可绕过传输校验或读本地）。
func validateBinaryURL(raw string) error {
	if raw == "" {
		return errors.New("kind=url 时缺少 url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("url 解析失败: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("url 必须为 https（当前 %q）：二进制经明文传输可被中间人替换", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("url 缺少主机名")
	}
	return nil
}

// validateBinaryNodePath 校验节点本地文件路径（防越权读任意文件，spec §3.2 安全约束）。
//
// 与 instance_import 的路径校验同口径：必须是绝对路径；且必须落在受控放行根内。
// 默认放行根为**空集**——未显式配置时一律拒绝，避免默认打开越权读取面。
// 运维需要读取受控区外的文件时，应先上传到制品库再以 asset 为来源（更安全，且自动获得 sha256）。
func (p *ProvisionService) validateBinaryNodePath(clean string) error {
	if clean == "" || clean == "." || clean == string(filepath.Separator) {
		return errors.New("kind=node_file 时缺少 nodePath")
	}
	if !filepath.IsAbs(clean) {
		return fmt.Errorf("nodePath 必须是绝对路径: %s", clean)
	}
	if !pathWithinAnyRoot(clean, p.binaryAllowedRoots()) {
		return fmt.Errorf("nodePath 落在受控放行目录之外，已拒绝（防越权读取节点任意文件）: %s；"+
			"如需使用该文件，请先上传到制品库再以 kind=asset 为来源", clean)
	}
	return nil
}

// binaryAllowedRoots 返回节点文件来源的受控放行根（绝对路径）。
// 由 main 装配注入（control-plane.yml binary.node_file_roots）；未注入时返回空集（一律拒绝）。
func (p *ProvisionService) binaryAllowedRoots() []string {
	if p.binaryRoots == nil {
		return nil
	}
	return p.binaryRoots()
}

// pathWithinAnyRoot 判定 path 是否落在任一放行根之下（含根本身）。
// 用 filepath.Rel 做包含判定，避免字符串前缀匹配把 /data-evil 误判为 /data 之内。
func pathWithinAnyRoot(path string, roots []string) bool {
	if len(roots) == 0 {
		return false
	}
	clean := filepath.Clean(path)
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "" || root == "." || !filepath.IsAbs(root) {
			continue
		}
		if clean == root {
			return true
		}
		rel, err := filepath.Rel(root, clean)
		if err != nil {
			continue
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// validBinaryFilename 校验落盘文件名：纯文件名、无路径分隔符、无穿越、无空白/Shell 元字符。
//
// 该名校验是安全边界：它会被拼进结构化启动命令（`./<filename>`）并由 Worker 经 `sh -c` 执行，
// 放任 Shell 元字符即等同任意命令执行（`beacon;rm -rf /`）。故只接受「紧凑的安全字符集」
// 而非「排除若干危险字符」——白名单在跨平台 sh/cmd 语义差异下更难被绕过。
func validBinaryFilename(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || len(name) > 128 {
		return false
	}
	if name != filepath.Base(name) {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == '+':
		default:
			return false
		}
	}
	// 拒绝 "." / ".." 形态（含前导点点后被后续字符掩盖的情形已由字符集排除）。
	if strings.Trim(name, ".") == "" {
		return false
	}
	return true
}

// validSHA256Hex 校验 64 位十六进制摘要。
func validSHA256Hex(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, c := range v {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// deriveBinaryStartCommand 由落盘文件名派生启动命令（spec §3.4 决策 2A）。
//
// 复用既有 startCommand 字段而非引入 binarySpec：startCommand 已是 varchar(1024) 的自由命令串，
// Worker 经 sh -c 执行，天然支持任意二进制；新结构会与 launchSpec 形成双重真源。
// 形如 ./beacon-1.1.0-linux-amd64 —— 相对实例工作目录执行，与生产实例的既有运行方式一致。
func deriveBinaryStartCommand(filename string) string {
	return "./" + strings.TrimSpace(filename)
}

// binaryProvisionStageFromBytes 把字节进度映射到 [binaryStageFetchFrom, binaryStageFetchTo] 区间。
// total<=0（上游未给 Content-Length）时停在区间起点，避免进度条谎报。
func binaryProvisionStageFromBytes(downloaded, total int64) int {
	if total <= 0 || downloaded <= 0 {
		return binaryStageFetchFrom
	}
	if downloaded >= total {
		return binaryStageFetchTo
	}
	span := binaryStageFetchTo - binaryStageFetchFrom
	return binaryStageFetchFrom + int(int64(span)*downloaded/total)
}

// binaryFetchStageText 生成取件阶段的来源描述（spec §3.3 的 20 阶段文案）。
func binaryFetchStageText(kind string) string {
	if kind == string(BinarySourceNodeFile) {
		return "获取二进制（来源：节点本地）…"
	}
	return "获取二进制（来源：远程 URL / 制品库）…"
}

// buildFetchBinaryRequest 把取件计划翻译为 Worker 请求（包级函数便于单测直接断言）。
func buildFetchBinaryRequest(instanceUUID string, plan *binaryFetchPlan) *workerpb.FetchBinaryRequest {
	return &workerpb.FetchBinaryRequest{
		InstanceUuid: instanceUUID,
		SourceKind:   plan.SourceKind,
		DestFilename: plan.Filename,
		DownloadUrl:  plan.DownloadURL,
		Sha256:       plan.SHA256,
		NodePath:     plan.NodePath,
		Executable:   plan.Executable,
	}
}

// provisionBinaryOnWorker 在 Worker 上完成取件与落盘（后台段执行体）。
// stage 非 nil 时按字节进度上报，使任务页真实反映进度而非停在固定百分比。
func (p *ProvisionService) provisionBinaryOnWorker(ctx context.Context, inst *model.Instance, plan *binaryFetchPlan, stageFn func(int, string)) error {
	stage := func(progress int, text string) {
		if stageFn != nil {
			stageFn(progress, text)
		}
	}
	var node model.Node
	if err := p.db.First(&node, inst.NodeID).Error; err != nil {
		return fmt.Errorf("查找节点失败: %w", err)
	}
	client, ok := p.pool.Get(node.UUID)
	if !ok {
		return fmt.Errorf("节点 %s 未连接", node.UUID)
	}

	stage(binaryStageFetchFrom, binaryFetchStageText(plan.SourceKind))
	fetchCtx, cancel := context.WithTimeout(ctx, binaryFetchTimeout)
	defer cancel()
	stream, err := client.Worker.FetchBinary(fetchCtx, buildFetchBinaryRequest(inst.UUID, plan))
	if err != nil {
		return fmt.Errorf("取二进制失败: %w", err)
	}

	// 流式消费：中间帧只更新阶段进度，末帧（done）携带终态结果。
	// 末帧缺失即视为异常中止——绝不能把「流断了」当成功（否则半截文件会被当成完整二进制）。
	var final *binaryFetchOutcome
	lastProgress := -1
	for {
		progress, recvErr := stream.Recv()
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				break
			}
			return fmt.Errorf("取二进制失败: %w", recvErr)
		}
		if progress.Done {
			final = &binaryFetchOutcome{
				Success:   progress.Success,
				Error:     progress.Error,
				Size:      progress.Size,
				SHA256:    progress.Sha256,
				FromLocal: progress.FromLocal,
			}
			continue
		}
		// 进度文案按整百分比去重，避免 TaskLog 被逐帧刷屏。
		pct := binaryProvisionStageFromBytes(progress.Downloaded, progress.Total)
		if pct == lastProgress {
			continue
		}
		lastProgress = pct
		stage(pct, fmt.Sprintf("下载/复制中：%s", humanBytes(progress.Downloaded)))
	}
	if final == nil {
		return errors.New("取二进制失败：Worker 未返回终态（连接可能中断）")
	}
	if !final.Success {
		return fmt.Errorf("取二进制失败: %s", final.Error)
	}
	stage(binaryStageVerify, "校验完整性…")
	stage(binaryStageInstall, "写入工作目录并设置权限…")
	slog.Info("二进制取件完成",
		"instance", inst.Name, "instanceId", inst.ID,
		"source", plan.SourceKind, "size", final.Size, "fromLocal", final.FromLocal)
	return nil
}

// binaryFetchOutcome 是取件终态的归一结果（隔离 workerpb 类型，便于单测）。
type binaryFetchOutcome struct {
	Success   bool
	Error     string
	Size      int64
	SHA256    string
	FromLocal bool
}

// humanBytes 把字节数格式化为人类可读形式（阶段文案用）。
func humanBytes(n int64) string {
	switch {
	case n <= 0:
		return "0 B"
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	}
}

// createBinaryInstance 建 binary 实例（同步段）：系统分配工作目录与端口，类型 generic、
// 角色 universal、不绑定 JDK（spec §3.5 实例默认属性）。
//
// 与 MC 搭建的差异：不下发 launchSpec（结构化启动是 java 专属语义），启动命令由
// startCommand 字段承载（派生或显式指定），使请求体无双重真源。
func (p *ProvisionService) createBinaryInstance(req ProvisionServerRequest, plan *binaryFetchPlan) (*model.Instance, error) {
	startCommand := strings.TrimSpace(req.StartCommand)
	if startCommand == "" {
		startCommand = deriveBinaryStartCommand(plan.Filename)
	}
	// 端口分配互斥同 MC 路径：防同节点并发创建选中同一端口。
	releasePortAlloc := p.instance.lockNodePortAlloc()
	defer releasePortAlloc()
	// 通用二进制非 MC Java 进程，探针（Bukkit 插件）不适用，不分配探针端口（FR-454）。
	ports, err := allocPortsForNode(p.db, req.NodeID, false)
	if err != nil {
		return nil, err
	}
	return p.instance.Create(CreateInstanceRequest{
		NodeID: req.NodeID,
		Name:   req.Name,
		// Type 落 generic：二进制不是 Minecraft Java 进程，落 minecraft_java 会让
		// docker 端口映射按 25565 约定改写、模板 RAM 需求按 JVM 估算，均不适用。
		Type:        model.InstanceTypeGeneric,
		Role:        model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDaemon,
		// 不绑 JDK（spec §3.5）：Go/Rust 等编译型二进制不需要 JVM。
		JDKID:        0,
		StartCommand: startCommand,
		ServerPort:   ports.ServerPort,
		QueryPort:    ports.QueryPort,
		AutoRestart:  true,
		GroupID:      req.GroupID,
	})
}

// rebuildBinaryInstance 重建损毁的 binary 实例（FR-441 + FR-342 语义）：
// 复用存库的搭建参数重跑取件到既有实例，不需再次提供来源；成功 → STOPPED，失败 → 仍 DAMAGED。
//
// FR-468 起**冻结版本**：优先读 InstanceBinaryBinding 取绑定版本重取，而非重新按优先级
// 解析来源。旧行为（ADR-090 原文「重解析而非复用上次制品」）会在制品库出现更高版本时
// 把重建变成一次静默升降级，运维无法预期；「吃到新版本」现由受控升级
// （BinaryVersionService.Upgrade）显式承担，重建回归「恢复原状」职责。
//
// 逃生口：请求显式给了 binarySource（或 Beacon 用 default 占位外的显式来源）时仍以请求为准
// ——那是有意换来源，不该被绑定冻结。
func (p *ProvisionService) rebuildBinaryInstance(ctx context.Context, inst *model.Instance, req ProvisionServerRequest, createdBy uint, requestBaseURL string) (string, error) {
	beacon := IsBeaconCore(req.CoreType)
	var plan *binaryFetchPlan
	var sourceText string
	var err error

	binding, bindErr := p.instanceBinaryBinding(inst.ID)
	if bindErr != nil {
		return "", bindErr
	}
	if explicitBinarySourceProvided(req) {
		// 显式覆盖：以请求为准（逃生口）。
		plan, sourceText, err = p.resolveBinarySource(binarySourceOf(req), requestBaseURL)
	} else if binding != nil && binding.CurrentAssetID != 0 {
		// 冻结版本：绑定存在且来自制品库 → 固定取该 asset。
		plan, err = p.binaryPlanFromBinding(binding, requestBaseURL)
		sourceText = fmt.Sprintf("绑定版本 asset#%d", binding.CurrentAssetID)
	} else if beacon {
		// 无制品库绑定的 Beacon（GitHub 来源 / 早期实例）：保持既有「按优先级解析」行为。
		plan, sourceText, err = p.resolveBeaconPresetSource(ctx, requestBaseURL)
	} else {
		plan, sourceText, err = p.resolveBinarySource(binarySourceOf(req), requestBaseURL)
	}
	if err != nil {
		return "", err
	}
	_ = p.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status_reason", "重建中：正在重新获取二进制（完成前请勿启动）").Error
	kindText := "binary"
	if beacon {
		kindText = "beacon"
	}
	title := fmt.Sprintf("重建 %s（%s，%s）", inst.Name, kindText, sourceText)
	instCopy := *inst
	// 落盘名可能随取件计划变化：启动命令须同步替换旧文件名引用，否则实例仍指向旧文件名，
	// 重建成功后一启动就是 command not found。
	// 仅在启动命令确实引用旧文件名时替换——`./run.sh` 这类不含旧名的命令是运维的明确意图，
	// 不该被系统改写。
	binaryStartCommand, commandRewritten := replaceBinaryTokenInStartCommand(inst.StartCommand, bindingFilenameOf(binding, inst), plan.Filename)
	if !commandRewritten {
		binaryStartCommand = ""
	}
	taskID := p.tasks.RunAsync(RunSpec{
		NodeID: inst.NodeID, InstanceID: inst.ID, Kind: model.TaskKindBinaryProvision,
		Title: title, CreatedBy: createdBy, Timeout: binaryFetchTimeout + 5*time.Minute,
	}, func(ctx context.Context, stage func(int, string)) (string, error) {
		stage(binaryStageResolve, "解析制品来源…")
		if err := p.provisionBinaryOnWorker(ctx, &instCopy, plan, stage); err != nil {
			// 重建仍失败：保持 DAMAGED，只更新原因（用户可修好后再次重建）。
			_ = p.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
				Update("status_reason", "重建未完成："+err.Error()).Error
			return "", err
		}
		stage(binaryStageDeriveCmd, "派生启动命令…")
		updates := map[string]any{"status": model.InstanceStatusStopped, "status_reason": ""}
		if binaryStartCommand != "" {
			updates["start_command"] = binaryStartCommand
		}
		_ = p.db.Model(&model.Instance{}).Where("id = ?", inst.ID).Updates(updates).Error
		// FR-468：重建完成后刷新绑定的落盘名/摘要（冻结版本的实例可能曾被人手换过文件）。
		p.refreshBindingAfterRebuild(inst.ID, plan)
		slog.Info("二进制实例重建完成", "instance", inst.Name, "instanceId", inst.ID, "preset", kindText)
		return "", nil
	})
	if taskID == "" {
		return "", errors.New("登记重建任务失败")
	}
	return taskID, nil
}

// bindingFilenameOf 取绑定的落盘名作为「旧文件名」；无绑定时由既有启动命令兜底解析。
//
// 兜底：命令形如 `./xxx`（单 token 相对路径）时取 `xxx`，使未登记绑定的早期实例
// 在重建换名时同样能正确改写启动命令。
func bindingFilenameOf(binding *model.InstanceBinaryBinding, inst *model.Instance) string {
	if binding != nil && strings.TrimSpace(binding.CurrentFilename) != "" {
		return binding.CurrentFilename
	}
	cmd := strings.TrimSpace(inst.StartCommand)
	if !strings.HasPrefix(cmd, "./") {
		return ""
	}
	first := strings.Fields(cmd)[0]
	return strings.TrimPrefix(first, "./")
}

// markBinaryInstanceDamaged 把实例置为损毁态并标注「搭建未完成」原因
// （与 MC 路径同一状态机语义：损毁态拦启动，provisionSpec 可重建）。
func (p *ProvisionService) markBinaryInstanceDamaged(instanceID uint, reason string) {
	_ = p.db.Model(&model.Instance{}).Where("id = ?", instanceID).
		Updates(map[string]any{
			"status":        model.InstanceStatusDamaged,
			"status_reason": reason,
		}).Error
}
