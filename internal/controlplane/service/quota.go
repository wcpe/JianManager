package service

import (
	"errors"
	"math"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 运行期配额强制（FR-467）。
//
// 既有能力全部只在「创建时」或「启动时」生效：组配额只在 service/instance.go 的 Create
// 事务里检查；docker 资源限额只在容器启动时注入 cgroup；FR-317 的内存守卫只在启动前挡一次。
// 运行期没有任何持续强制——一个 daemon 模式实例可以持续吃内存/CPU 直到把节点拖垮。
//
// 本文件提供「配额基准统一」与「巡检强制」两件事。裁决在 CP 侧（配额真源在 CP 数据库，
// 架构不变量：Worker 不碰 DB），Worker 只执行处置动作。

// EnforceMode 超限处置档位（FR-467 §2.5）。
type EnforceMode string

const (
	// EnforceModeAlert 只告警，不干预进程（默认档；最保守，不会误杀）。
	EnforceModeAlert EnforceMode = "alert"
	// EnforceModeThrottle 允许时收紧资源（docker）；非 docker 降级为告警并标注「无法限流」。
	EnforceModeThrottle EnforceMode = "throttle"
	// EnforceModeStop 优雅停止实例并置 statusReason。
	EnforceModeStop EnforceMode = "stop"
)

// ValidEnforceMode 报告档位是否合法。
func ValidEnforceMode(m string) bool {
	switch EnforceMode(m) {
	case EnforceModeAlert, EnforceModeThrottle, EnforceModeStop:
		return true
	}
	return false
}

// EffectiveQuota 实例可用的运行期配额（FR-467 §2.2）。
// 来源优先级：**实例级字段 > 组配额派生 > 不限（0）**。
type EffectiveQuota struct {
	// CPUCores 实例 CPU 上限（核）；0=不限。
	CPUCores float64
	// MemLimitMB 实例内存上限（MiB）；0=不限。
	MemLimitMB int64
	// DiskLimitMB 实例磁盘上限（MiB）；0=不限。
	DiskLimitMB int64
	// EnforceMode 超限处置档位（实例级显式值 > 组配额 > 平台设置默认）。
	EnforceMode EnforceMode
	// Source 各维度的来源说明（展示与排障用）：instance / group / none。
	MemSource  string
	DiskSource string
	CPUSource  string
	// GroupID 实例所属组（磁盘余量按组核算时用）；0=无组。
	GroupID uint
}

// ErrQuotaInstanceNotFound 配额查询的实例不存在。
var ErrQuotaInstanceNotFound = errors.New("实例不存在")

// quotaInstanceSource 是配额解析所需的实例字段读取（便于单测注入）。
type quotaInstanceSource interface {
	// loadQuotaInputs 返回实例的资源字段与所属组。
	loadQuotaInputs(instanceID uint) (*quotaInputs, error)
}

// quotaInputs 配额解析的输入快照。
type quotaInputs struct {
	InstanceID  uint
	CPULimit    float64
	MemLimitMB  int64
	DiskLimitMB int64
	GroupID     uint
}

// gormQuotaSource 从实例表读配额输入（生产实现）。
type gormQuotaSource struct{ db *gorm.DB }

func (s gormQuotaSource) loadQuotaInputs(instanceID uint) (*quotaInputs, error) {
	var inst model.Instance
	if err := s.db.Select("id", "cpu_limit", "mem_limit_mb", "disk_limit_mb").
		First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrQuotaInstanceNotFound
		}
		return nil, err
	}
	in := &quotaInputs{
		InstanceID:  inst.ID,
		CPULimit:    inst.CPULimit,
		MemLimitMB:  inst.MemLimitMB,
		DiskLimitMB: inst.DiskLimitMB,
	}
	in.GroupID = instanceGroupID(s.db, inst.ID)
	return in, nil
}

// instanceGroupID 取实例所属用户组（0 = 未分配）。
//
// R21 判定结论：**「一实例至多一组」是 schema 级不变量**，原先的
// `pickStrictestGroup`（多组取「磁盘额度最小」者）的 `len(groupIDs) > 1` 分支不可达：
//   - `GroupInstance.InstanceID` 是**单列 uniqueIndex**（model/instance.go，缺 group_id
//     的组合索引语义）——同一实例的第二行会被数据库直接拒绝；
//   - 全仓唯一的生产写入点是实例创建事务内的 `tx.Create(gi)`（instance.go 的
//     「分配给用户组」段），除此之外只有删除（实例删除 / 节点清理）与读取，没有
//     追加第二条归属的路径。
//
// 上面两条互为印证（代码事实 + 写入面事实），故旧实现的取严比较是死路径：它还为每次
// 配额解析多查一次 group_quotas（巡检每实例每轮调用 1~2 次，64 服规模下每轮上百次
// 无谓查询）。此处直接返回该组。
//
// 若将来放开多组归属（需同时改 model 的唯一索引与本函数），必须恢复「多组取最严」
// 的语义：配额是护栏，取宽会让实例在更严的组里超限而不被拦。
func instanceGroupID(db *gorm.DB, instanceID uint) uint {
	var groupIDs []uint
	if err := db.Model(&model.GroupInstance{}).Where("instance_id = ?", instanceID).
		Pluck("group_id", &groupIDs).Error; err != nil || len(groupIDs) == 0 {
		return 0
	}
	return groupIDs[0]
}

// groupDiskFreeMB 计算组配额派生给单实例的磁盘可用余量（软限）。
//
// 精度说明（spec §5）：组配额的存储口径目前按「备份总大小」估算（instance.go 的
// TODO(FR-003)）。此处按「组内已登记备份大小」推余量，与 Create 时的口径一致——
// 换了口径会让「创建时通过、运行期立刻超限」这类自相矛盾的行为出现。
func groupDiskFreeMB(db *gorm.DB, groupID uint) (int64, error) {
	var quota model.GroupQuota
	if err := db.Where("group_id = ?", groupID).First(&quota).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	if quota.MaxStorageMB <= 0 {
		return 0, nil
	}
	var used struct{ Total float64 }
	if err := db.Model(&model.Backup{}).
		Select("COALESCE(SUM(file_size_mb), 0) as total").
		Joins("JOIN group_instances ON group_instances.instance_id = backups.instance_id").
		Where("group_instances.group_id = ?", groupID).
		Scan(&used).Error; err != nil {
		return 0, err
	}
	free := int64(quota.MaxStorageMB) - int64(math.Ceil(used.Total))
	if free <= 0 {
		// 已超组配额：不再给任何余量（0 表示「不限」会让强制失效，故回 1MB 的最小正数）。
		return 1, nil
	}
	return free, nil
}

// QuotaService 配额解析与查询（FR-467）。
type QuotaService struct {
	db     *gorm.DB
	source quotaInstanceSource
	// defaultMode 平台设置 quota.enforce_mode 的读取（nil 时回 alert）。
	defaultMode func() EnforceMode
}

// NewQuotaService 创建配额服务。
func NewQuotaService(db *gorm.DB) *QuotaService {
	return &QuotaService{db: db, source: gormQuotaSource{db: db}}
}

// SetDefaultModeReader 注入平台默认强制档位读取器（main 接线）。
func (s *QuotaService) SetDefaultModeReader(fn func() EnforceMode) { s.defaultMode = fn }

// defaultEnforceMode 取平台默认档位；未注入时为 alert（最保守，绝不意外停服）。
func (s *QuotaService) defaultEnforceMode() EnforceMode {
	if s.defaultMode == nil {
		return EnforceModeAlert
	}
	m := s.defaultMode()
	if !ValidEnforceMode(string(m)) {
		return EnforceModeAlert
	}
	return m
}

// EffectiveQuota 解析实例的运行期配额（FR-467 §2.2：实例级 > 组派生 > 不限）。
func (s *QuotaService) EffectiveQuota(instanceID uint) (*EffectiveQuota, error) {
	in, err := s.source.loadQuotaInputs(instanceID)
	if err != nil {
		return nil, err
	}
	q := &EffectiveQuota{
		MemSource: "none", DiskSource: "none", CPUSource: "none",
		EnforceMode: s.defaultEnforceMode(),
		GroupID:     in.GroupID,
	}
	// CPU：实例级字段。
	if in.CPULimit > 0 {
		q.CPUCores, q.CPUSource = in.CPULimit, "instance"
	}
	// 内存：实例级字段。
	if in.MemLimitMB > 0 {
		q.MemLimitMB, q.MemSource = in.MemLimitMB, "instance"
	}
	// 磁盘：实例级字段优先，否则按组配额余量派生（软限）。
	if in.DiskLimitMB > 0 {
		q.DiskLimitMB, q.DiskSource = in.DiskLimitMB, "instance"
	} else if in.GroupID != 0 {
		free, ferr := groupDiskFreeMB(s.db, in.GroupID)
		if ferr != nil {
			return nil, ferr
		}
		if free > 0 {
			q.DiskLimitMB, q.DiskSource = free, "group"
		}
	}
	// 组配额可覆盖强制档位（组管理员可按组设定处置力度）。
	if in.GroupID != 0 {
		var quota model.GroupQuota
		if err := s.db.Where("group_id = ?", in.GroupID).First(&quota).Error; err == nil {
			if quota.EnforceMode != "" && ValidEnforceMode(quota.EnforceMode) {
				q.EnforceMode = EnforceMode(quota.EnforceMode)
			}
		}
	}
	return q, nil
}
