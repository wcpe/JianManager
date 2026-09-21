package database

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"gorm.io/gorm"
)

// newBeaconBackfillDB 建一个仅含 nodes/instances 的临时库（避开全量 AutoMigrate 的开销）。
func newBeaconBackfillDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "beacon-backfill.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	return db
}

// TestBackfillBeaconInstanceType_ByRoleOnly FR-454 验收③：只按 role 归正 (beacon→generic)，
// 重复执行幂等；role 非 beacon 的实例（含 name 含 beacon 者）type 不被改动。
func TestBackfillBeaconInstanceType_ByRoleOnly(t *testing.T) {
	db := newBeaconBackfillDB(t)
	node := &model.Node{Name: "n", Host: "127.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)

	mk := func(name string, typ model.InstanceType, role model.InstanceRole) *model.Instance {
		inst := &model.Instance{
			NodeID: node.ID, Name: name, Type: typ, Role: role,
			ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/" + name,
			StartCommand: "x", Status: model.InstanceStatusStopped,
		}
		require.NoError(t, db.Create(inst).Error)
		return inst
	}

	// 生产 id=130 的形态：role=beacon 但 type 被误记为 minecraft_java。
	beacon := mk("beacon", model.InstanceTypeMinecraftJava, model.InstanceRoleBeacon)
	// 生产 id=3 的形态：name 含 beacon 但 role=universal —— 不得擅自归正。
	beaconNamed := mk("beacon", model.InstanceTypeMinecraftJava, model.InstanceRoleUniversal)
	backend := mk("smp", model.InstanceTypeMinecraftJava, model.InstanceRoleBackend)
	generic := mk("bin", model.InstanceTypeGeneric, model.InstanceRoleUniversal)

	require.NoError(t, backfillBeaconInstanceType(db))
	// 幂等：再跑一次不报错、不再命中。
	require.NoError(t, backfillBeaconInstanceType(db))
	require.NoError(t, backfillBeaconInstanceType(db))

	var gotBeacon, gotBeaconNamed, gotBackend, gotGeneric model.Instance
	require.NoError(t, db.First(&gotBeacon, beacon.ID).Error)
	require.NoError(t, db.First(&gotBeaconNamed, beaconNamed.ID).Error)
	require.NoError(t, db.First(&gotBackend, backend.ID).Error)
	require.NoError(t, db.First(&gotGeneric, generic.ID).Error)

	require.Equal(t, model.InstanceTypeGeneric, gotBeacon.Type, "role=beacon 的 type 应归正为 generic")
	require.Equal(t, model.InstanceRoleBeacon, gotBeacon.Role, "归正只改 type，不动 role")
	require.Equal(t, model.InstanceTypeMinecraftJava, gotBeaconNamed.Type,
		"name 含 beacon 但 role 非 beacon 的实例不得擅自改动")
	require.Equal(t, model.InstanceTypeMinecraftJava, gotBackend.Type)
	require.Equal(t, model.InstanceTypeGeneric, gotGeneric.Type)
}

// TestBackfillBeaconInstanceType_ZeroesInapplicableProbePort FR-454（第二轮复审 N3）：
// 归正 type 时顺带把非适用实例的历史脏 probe_port 归零；适用实例（MC 后端、role IS NULL）不受影响。
func TestBackfillBeaconInstanceType_ZeroesInapplicableProbePort(t *testing.T) {
	db := newBeaconBackfillDB(t)
	node := &model.Node{Name: "n", Host: "127.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)

	mk := func(name string, typ model.InstanceType, role model.InstanceRole, probe int) *model.Instance {
		inst := &model.Instance{
			NodeID: node.ID, Name: name, Type: typ, Role: role,
			ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/" + name,
			StartCommand: "x", ProbePort: probe, Status: model.InstanceStatusStopped,
		}
		require.NoError(t, db.Create(inst).Error)
		return inst
	}

	beacon := mk("beacon-ib", model.InstanceTypeMinecraftJava, model.InstanceRoleBeacon, 29000)
	proxy := mk("gate-ib", model.InstanceTypeMinecraftJava, model.InstanceRoleProxy, 29001)
	genericUniversal := mk("bin-ib", model.InstanceTypeGeneric, model.InstanceRoleUniversal, 29002)
	backend := mk("smp-ib", model.InstanceTypeMinecraftJava, model.InstanceRoleBackend, 29003)
	nullRole := mk("legacy-nullrole", model.InstanceTypeMinecraftJava, model.InstanceRoleUniversal, 29004)
	// 造出 role IS NULL 的历史行：NULL 视为适用，探针端口必须保留（不得被本条件误清）。
	require.NoError(t, db.Exec("UPDATE instances SET role = NULL WHERE id = ?", nullRole.ID).Error)

	require.NoError(t, backfillBeaconInstanceType(db))
	// 幂等：再跑一次仍不报错，且适用实例端口保持不动。
	require.NoError(t, backfillBeaconInstanceType(db))

	load := func(id uint) model.Instance {
		var got model.Instance
		require.NoError(t, db.First(&got, id).Error)
		return got
	}
	require.Zero(t, load(beacon.ID).ProbePort, "Beacon 不适用探针，历史端口应归零")
	require.Zero(t, load(proxy.ID).ProbePort, "代理不适用探针，历史端口应归零")
	require.Zero(t, load(genericUniversal.ID).ProbePort, "通用二进制不适用探针，历史端口应归零")
	require.Equal(t, 29003, load(backend.ID).ProbePort, "MC 后端适用探针，端口不得被动")
	require.Equal(t, 29004, load(nullRole.ID).ProbePort, "role IS NULL 视为适用，端口不得被动")
}

// TestListBeaconNamedNonBeaconInstances_NullRoleAndBestEffort FR-454（第二轮复审 N2/N4）：
//   - N2：`role <> 'beacon'` 对 role IS NULL 行三值逻辑恒 NULL 会漏列，须显式补 `role IS NULL`；
//   - N4：本函数是纯诊断（best-effort），查询失败只返回 nil、绝不冒泡（否则会拖垮 CP 启动路径）。
func TestListBeaconNamedNonBeaconInstances_NullRoleAndBestEffort(t *testing.T) {
	db := newBeaconBackfillDB(t)
	node := &model.Node{Name: "n", Host: "127.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)

	mk := func(name string, role model.InstanceRole) *model.Instance {
		inst := &model.Instance{
			NodeID: node.ID, Name: name, Type: model.InstanceTypeMinecraftJava, Role: role,
			ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/" + name,
			StartCommand: "x", Status: model.InstanceStatusStopped,
		}
		require.NoError(t, db.Create(inst).Error)
		return inst
	}

	universalNamed := mk("beacon", model.InstanceRoleUniversal)
	nullRoleNamed := mk("Beacon-Prod", model.InstanceRoleUniversal)
	require.NoError(t, db.Exec("UPDATE instances SET role = NULL WHERE id = ?", nullRoleNamed.ID).Error)
	mk("beacon-preset", model.InstanceRoleBeacon) // role=beacon：身份就绪，不属于「待复核」
	mk("smp", model.InstanceRoleBackend)          // 名字无关

	ids := listBeaconNamedNonBeaconInstances(db)
	require.ElementsMatch(t, []uint{universalNamed.ID, nullRoleNamed.ID}, ids,
		"须覆盖 role 非 beacon 且含 role IS NULL 的命名实例，并排除 role=beacon")

	// best-effort：诊断查询失败（表缺失）只降级为日志，不 panic、不返回错误。
	require.NoError(t, db.Migrator().DropTable(&model.Instance{}))
	require.NotPanics(t, func() { require.Nil(t, listBeaconNamedNonBeaconInstances(db)) })
}

// TestAutoMigrate_NormalizesLegacyBeaconType FR-454：AutoMigrate（CP 启动路径）在迁移收尾
// 即完成归正，无需人工干预；再次 AutoMigrate 幂等。
func TestAutoMigrate_NormalizesLegacyBeaconType(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy-beacon.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	node := &model.Node{Name: "legacy", Host: "127.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	legacy := &model.Instance{
		NodeID: node.ID, Name: "beacon", Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBeacon, ProcessType: model.ProcessTypeDaemon,
		WorkDir: "var/servers/beacon", StartCommand: "beacon", Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(legacy).Error)

	require.NoError(t, AutoMigrate(db))
	var got model.Instance
	require.NoError(t, db.First(&got, legacy.ID).Error)
	require.Equal(t, model.InstanceTypeGeneric, got.Type)
}
