package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// newSearchTestDB 为 FR-247 搜索/聚合测试准备隔离的命名内存库。
func newSearchTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.GroupInstance{}, &model.NetworkMember{},
	))
	return db
}

func seedInst(t *testing.T, db *gorm.DB, name string, nodeID uint, status model.InstanceStatus, role model.InstanceRole, tags string, created time.Time) *model.Instance {
	t.Helper()
	inst := &model.Instance{
		NodeID: nodeID, Name: name, Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect,
		StartCommand: "x", Status: status, Role: role, Tags: tags, CreatedAt: created,
	}
	require.NoError(t, db.Create(inst).Error)
	return inst
}

func TestInstanceSearchParams_Normalize(t *testing.T) {
	cases := []struct {
		in, want InstanceSearchParams
	}{
		{InstanceSearchParams{Page: 0, PageSize: 0}, InstanceSearchParams{Page: 1, PageSize: 50, Sort: "name", Order: "asc"}},
		{InstanceSearchParams{Page: -3, PageSize: 999}, InstanceSearchParams{Page: 1, PageSize: 200, Sort: "name", Order: "asc"}},
		{InstanceSearchParams{Page: 2, PageSize: 30, Sort: "status", Order: "DESC"}, InstanceSearchParams{Page: 2, PageSize: 30, Sort: "status", Order: "desc"}},
		{InstanceSearchParams{Sort: "evil; DROP TABLE", Order: "x"}, InstanceSearchParams{Page: 1, PageSize: 50, Sort: "name", Order: "asc"}},
	}
	for i, c := range cases {
		p := c.in
		p.Normalize()
		require.Equalf(t, c.want.Page, p.Page, "case %d page", i)
		require.Equalf(t, c.want.PageSize, p.PageSize, "case %d pageSize", i)
		require.Equalf(t, c.want.Sort, p.Sort, "case %d sort", i)
		require.Equalf(t, c.want.Order, p.Order, "case %d order", i)
	}
}

func TestSearchInstances_PaginationAndTotal(t *testing.T) {
	db := newSearchTestDB(t)
	svc := NewInstanceService(db, nil, nil)
	base := time.Now()
	for i := 1; i <= 25; i++ {
		seedInst(t, db, fmt.Sprintf("srv-%02d", i), 1, model.InstanceStatusStopped, model.InstanceRoleUniversal, "", base)
	}

	items, total, err := svc.SearchInstances(nil, InstanceSearchParams{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, int64(25), total)
	require.Len(t, items, 10)
	require.Equal(t, "srv-01", items[0].Name) // 默认 name asc

	items, total, err = svc.SearchInstances(nil, InstanceSearchParams{Page: 3, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, int64(25), total)
	require.Len(t, items, 5) // 末页余 5

	items, total, err = svc.SearchInstances(nil, InstanceSearchParams{Page: 4, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, int64(25), total, "越界页 total 仍为真实总数")
	require.Empty(t, items)
}

func TestSearchInstances_QueryFiltersByNameCaseInsensitive(t *testing.T) {
	db := newSearchTestDB(t)
	svc := NewInstanceService(db, nil, nil)
	now := time.Now()
	seedInst(t, db, "alpha-1", 1, model.InstanceStatusStopped, model.InstanceRoleUniversal, "", now)
	seedInst(t, db, "alpha-2", 1, model.InstanceStatusStopped, model.InstanceRoleUniversal, "", now)
	seedInst(t, db, "beta-1", 1, model.InstanceStatusStopped, model.InstanceRoleUniversal, "", now)

	items, total, err := svc.SearchInstances(nil, InstanceSearchParams{Query: "alpha"})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.ElementsMatch(t, []string{"alpha-1", "alpha-2"}, names(items))

	items, _, err = svc.SearchInstances(nil, InstanceSearchParams{Query: "BETA"})
	require.NoError(t, err)
	require.Equal(t, []string{"beta-1"}, names(items), "LIKE 大小写不敏感")
}

func TestSearchInstances_Sort(t *testing.T) {
	db := newSearchTestDB(t)
	svc := NewInstanceService(db, nil, nil)
	base := time.Now()
	seedInst(t, db, "c", 1, model.InstanceStatusRunning, model.InstanceRoleUniversal, "", base.Add(3*time.Hour))
	seedInst(t, db, "a", 1, model.InstanceStatusStopped, model.InstanceRoleUniversal, "", base.Add(1*time.Hour))
	seedInst(t, db, "b", 1, model.InstanceStatusCrashed, model.InstanceRoleUniversal, "", base.Add(2*time.Hour))

	items, _, err := svc.SearchInstances(nil, InstanceSearchParams{Sort: "name", Order: "asc"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c"}, names(items))

	items, _, err = svc.SearchInstances(nil, InstanceSearchParams{Sort: "name", Order: "desc"})
	require.NoError(t, err)
	require.Equal(t, []string{"c", "b", "a"}, names(items))

	items, _, err = svc.SearchInstances(nil, InstanceSearchParams{Sort: "createdAt", Order: "desc"})
	require.NoError(t, err)
	require.Equal(t, []string{"c", "b", "a"}, names(items), "createdAt desc=最近优先")
}

func TestSearchInstances_FiltersStatusAndEnvTag(t *testing.T) {
	db := newSearchTestDB(t)
	svc := NewInstanceService(db, nil, nil)
	now := time.Now()
	seedInst(t, db, "run-prod", 1, model.InstanceStatusRunning, model.InstanceRoleUniversal, `["env:prod","blue"]`, now)
	seedInst(t, db, "stop-prod", 1, model.InstanceStatusStopped, model.InstanceRoleUniversal, `["env:prod"]`, now)
	seedInst(t, db, "run-dev", 1, model.InstanceStatusRunning, model.InstanceRoleUniversal, `["env:dev","blue"]`, now)

	st := model.InstanceStatusRunning
	items, total, err := svc.SearchInstances(nil, InstanceSearchParams{InstanceFilter: InstanceFilter{Status: &st}})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.ElementsMatch(t, []string{"run-prod", "run-dev"}, names(items))

	items, total, err = svc.SearchInstances(nil, InstanceSearchParams{InstanceFilter: InstanceFilter{Env: "prod"}})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.ElementsMatch(t, []string{"run-prod", "stop-prod"}, names(items))

	items, total, err = svc.SearchInstances(nil, InstanceSearchParams{InstanceFilter: InstanceFilter{Tag: "blue"}})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.ElementsMatch(t, []string{"run-prod", "run-dev"}, names(items))

	// 组合：env=prod AND status=RUNNING → 仅 run-prod
	items, total, err = svc.SearchInstances(nil, InstanceSearchParams{InstanceFilter: InstanceFilter{Status: &st, Env: "prod"}})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, []string{"run-prod"}, names(items))
}

func TestAggregateInstances_Counts(t *testing.T) {
	db := newSearchTestDB(t)
	svc := NewInstanceService(db, nil, nil)
	now := time.Now()
	// node1: 2 RUNNING backend, 1 STOPPED proxy ; node2: 1 CRASHED universal
	seedInst(t, db, "n1-a", 1, model.InstanceStatusRunning, model.InstanceRoleBackend, "", now)
	seedInst(t, db, "n1-b", 1, model.InstanceStatusRunning, model.InstanceRoleBackend, "", now)
	seedInst(t, db, "n1-c", 1, model.InstanceStatusStopped, model.InstanceRoleProxy, "", now)
	seedInst(t, db, "n2-a", 2, model.InstanceStatusCrashed, model.InstanceRoleUniversal, "", now)

	agg, err := svc.AggregateInstances(nil, InstanceSearchParams{})
	require.NoError(t, err)
	require.Equal(t, int64(4), agg.Total)
	require.Equal(t, int64(2), agg.ByStatus["RUNNING"])
	require.Equal(t, int64(1), agg.ByStatus["STOPPED"])
	require.Equal(t, int64(1), agg.ByStatus["CRASHED"])
	require.Equal(t, int64(0), agg.ByStatus["STARTING"], "缺席状态零补 0")
	require.Equal(t, int64(2), agg.ByRole["backend"])
	require.Equal(t, int64(1), agg.ByRole["proxy"])
	require.Equal(t, int64(1), agg.ByRole["universal"])

	// byStatus 之和 = total
	var sum int64
	for _, c := range agg.ByStatus {
		sum += c
	}
	require.Equal(t, agg.Total, sum)

	// byNode
	byNode := map[uint]int64{}
	for _, nc := range agg.ByNode {
		byNode[nc.NodeID] = nc.Count
	}
	require.Equal(t, int64(3), byNode[1])
	require.Equal(t, int64(1), byNode[2])
}

func TestAggregateInstances_HonorsFilter(t *testing.T) {
	db := newSearchTestDB(t)
	svc := NewInstanceService(db, nil, nil)
	now := time.Now()
	seedInst(t, db, "a", 1, model.InstanceStatusRunning, model.InstanceRoleUniversal, "", now)
	seedInst(t, db, "b", 1, model.InstanceStatusStopped, model.InstanceRoleUniversal, "", now)
	seedInst(t, db, "c", 1, model.InstanceStatusRunning, model.InstanceRoleUniversal, "", now)

	st := model.InstanceStatusRunning
	agg, err := svc.AggregateInstances(nil, InstanceSearchParams{InstanceFilter: InstanceFilter{Status: &st}})
	require.NoError(t, err)
	require.Equal(t, int64(2), agg.Total)
	require.Equal(t, int64(2), agg.ByStatus["RUNNING"])
	require.Equal(t, int64(0), agg.ByStatus["STOPPED"], "筛选 RUNNING 后停止计数为 0")
}

func TestSearchAndAggregate_PermissionScope(t *testing.T) {
	db := newSearchTestDB(t)
	svc := NewInstanceService(db, nil, nil)
	now := time.Now()
	in1 := seedInst(t, db, "g1-a", 1, model.InstanceStatusRunning, model.InstanceRoleUniversal, "", now)
	in2 := seedInst(t, db, "g1-b", 1, model.InstanceStatusStopped, model.InstanceRoleUniversal, "", now)
	seedInst(t, db, "ungrouped", 1, model.InstanceStatusRunning, model.InstanceRoleUniversal, "", now)
	// in1/in2 归入组 7
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: 7, InstanceID: in1.ID}).Error)
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: 7, InstanceID: in2.ID}).Error)

	// scope=组7 → 只见两实例
	items, total, err := svc.SearchInstances([]uint{7}, InstanceSearchParams{})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.ElementsMatch(t, []string{"g1-a", "g1-b"}, names(items))

	agg, err := svc.AggregateInstances([]uint{7}, InstanceSearchParams{})
	require.NoError(t, err)
	require.Equal(t, int64(2), agg.Total)

	// scope=空集 → 空结果，不报错
	items, total, err = svc.SearchInstances([]uint{}, InstanceSearchParams{})
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, items)
	agg, err = svc.AggregateInstances([]uint{}, InstanceSearchParams{})
	require.NoError(t, err)
	require.Zero(t, agg.Total)

	// scope=nil（管理员）→ 全部 3
	_, total, err = svc.SearchInstances(nil, InstanceSearchParams{})
	require.NoError(t, err)
	require.Equal(t, int64(3), total)
}
