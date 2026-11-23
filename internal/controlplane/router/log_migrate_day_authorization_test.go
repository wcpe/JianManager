package router

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query/grpcsvc"
	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/grpc"
)

// cpDayMigratePB 仅替代隧道传输；迁移入口与整日冻结仍执行真实 Worker/Catalog 代码。
type cpDayMigratePB struct {
	workerpb.WorkerServiceClient
	service *grpcsvc.Service
	calls   int
}

func (p *cpDayMigratePB) LogMigratePartition(ctx context.Context, request *workerpb.LogMigratePartitionRequest, _ ...grpc.CallOption) (*workerpb.LogTaskResponse, error) {
	p.calls++
	return p.service.LogMigratePartition(ctx, request)
}

func TestSixModulePhysicalDayMigrationRejectsScopedNodeManagerBeforeRPC(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	router := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, router)
	node := createTestNode(t, db)
	groupA := createGroupViaAPI(t, router, adminToken, "cp-day-group-a")
	groupB := createGroupViaAPI(t, router, adminToken, "cp-day-group-b")
	instanceA := makeInstanceInGroup(t, db, node.ID, groupA, "cp-day-a", model.InstanceStatusStopped)
	instanceB := makeInstanceInGroup(t, db, node.ID, groupB, "cp-day-b", model.InstanceStatusStopped)
	authz := service.NewAuthzService(db)
	managerToken := gateUserToken(t, router, db, authz, service.NewUserService(db), "cp-day-scoped-manager", []string{"node.manage"}, groupA)
	var user model.User
	require.NoError(t, db.Where("username = ?", "cp-day-scoped-manager").First(&user).Error)
	access, err := authz.LoadUserAccess(user.ID)
	require.NoError(t, err)
	require.False(t, access.IsPlatformAdmin)
	require.True(t, access.HasNode("node.manage"))
	allowedA, err := authz.CanAccessInstance(access, instanceA)
	require.NoError(t, err)
	require.True(t, allowedA)
	allowedB, err := authz.CanAccessInstance(access, instanceB)
	require.NoError(t, err)
	require.False(t, allowedB)

	for _, probe := range []string{fmt.Sprintf("inst:%d", instanceA), fmt.Sprintf("node:%d", node.ID)} {
		t.Run(probe, func(t *testing.T) {
			cat := catalog.New(nil)
			const day = "2026-10-01"
			keys := []catalog.PartitionKey{{StorageNamespace: fmt.Sprintf("inst:%d", instanceA), UTCDay: day}, {StorageNamespace: fmt.Sprintf("inst:%d", instanceB), UTCDay: day}}
			for _, key := range keys {
				require.NoError(t, cat.Put(catalog.NewStableRecord(key, catalog.OwnerHot, 1, "cp-day-hot")))
			}
			worker := grpcsvc.New(nil, nil)
			worker.SetPartitionMigrator(grpcsvc.PartitionMigrateFunc(func(_ context.Context, namespace, utcDay, _ string) error {
				// 2026-10-29：`lifecycle.ResolveMigrateTarget` 已随冷热双层废除而删除，
				// 本用例要验的是**授权边界**（单探针不能代表整物理日全部成员），与搬不搬无关，
				// 因此探针逻辑直接内联：未在 Catalog 里找到该日分区就报错。
				if len(cat.RecordsForDay(utcDay)) == 0 {
					return fmt.Errorf("log partition %s/%s is not in Catalog", namespace, utcDay)
				}
				// 只执行生产侧首个真实状态汇聚点，不进行物理文件移动。
				_, err := cat.BeginDayMigration(utcDay, catalog.OwnerCold, "cp-day-cold")
				return err
			}))
			pb := &cpDayMigratePB{service: worker}
			pool.SetWorkerClientForTest(node.UUID, pb)
			response := makeRequest(router, http.MethodPost, migratePartitionEndpoint(node.ID), map[string]any{"storageNamespace": probe, "utcDay": day}, managerToken)
			_, frozen := cat.PhysicalDay(day)
			t.Logf("真实组A权限=%t、组B权限=%t、平台管理员=%t；HTTP=%d RPC=%d 整日状态已产生=%t", allowedA, allowedB, access.IsPlatformAdmin, response.Code, pb.calls, frozen)
			require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
			require.Zero(t, pb.calls, "单探针授权不能代表整物理日所有成员均获授权")
			require.False(t, frozen)
			for _, key := range keys {
				record, ok := cat.Get(key)
				require.True(t, ok)
				require.Equal(t, catalog.OwnerHot, record.Owner)
				require.Equal(t, catalog.StateCleaned, record.MigrationState, "必须保持 NewStableRecord 的初始稳定态")
				require.False(t, record.WriteRoute.Frozen)
			}
		})
	}
}
