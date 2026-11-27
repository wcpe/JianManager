package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ListWithRefs：一次返回 JDK + 引用实例 + 删除可行性，且删除判定必须与 Delete 的
// 真实守卫同源——否则界面会「看着能删、点了 409」。用一个表驱动用例同时校验。
func TestJDKService_ListWithRefs_MatchesDeleteGuard(t *testing.T) {
	cases := []struct {
		name              string
		direct            bool // 有实例 jdk_id 指向它
		majorDep          bool // 有实例按大版本依赖
		sameMajorOther    bool // 同大版本还有另一个托管 JDK
		sameMajorExternal bool // 同大版本有更新的外部登记 JDK
		expectBlocked     bool
		expectReason      string
	}{
		{name: "无引用：可删", expectBlocked: false, expectReason: ""},
		{name: "直接绑定：拦", direct: true, expectBlocked: true, expectReason: JDKDeleteBlockedDirect},
		{
			name: "大版本依赖 + 同版本无其它托管：拦", majorDep: true,
			expectBlocked: true, expectReason: JDKDeleteBlockedMajor,
		},
		{
			name: "大版本依赖 + 同版本还有托管：不拦（resolveJDKPath 会挑另一个）", majorDep: true, sameMajorOther: true,
			expectBlocked: false, expectReason: "",
		},
		{
			name: "大版本依赖 + 同版本有更新的外部 JDK：与 ListWithRefs 同源不拦", majorDep: true, sameMajorExternal: true,
			expectBlocked: false, expectReason: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newJDKTestDB(t)
			svc := NewJDKService(db, nil)

			node := &model.Node{Name: "n1", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
			require.NoError(t, db.Create(node).Error)
			jdk := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4", Arch: "x64", Path: "/opt/jdk21", Managed: true}
			require.NoError(t, db.Create(jdk).Error)
			if tc.sameMajorOther {
				other := &model.NodeJDK{NodeID: node.ID, Vendor: "Zulu", MajorVersion: 21, Version: "21.0.3", Arch: "x64", Path: "/opt/jdk21b", Managed: true}
				require.NoError(t, db.Create(other).Error)
			}
			if tc.sameMajorExternal {
				ext := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.5", Arch: "x64", Path: "/opt/jdk21-ext", Managed: false}
				require.NoError(t, db.Create(ext).Error)
			}

			// 直接用原始 SQL 造实例，绕开 Create 的默认值逻辑（该用例只关心绑定列）。
			if tc.direct {
				i := &model.Instance{NodeID: node.ID, Name: "i-direct", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, JDKID: jdk.ID}
				require.NoError(t, db.Create(i).Error)
			}
			if tc.majorDep {
				i := &model.Instance{NodeID: node.ID, Name: "i-major", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, JavaMajorVersion: 21}
				require.NoError(t, db.Create(i).Error)
			}

			rows, err := svc.ListWithRefs(node.ID)
			require.NoError(t, err)
			// 找到被测 JDK（可能还有 sameMajorOther 那一行）。
			var target *JDKWithRefs
			for i := range rows {
				if rows[i].ID == jdk.ID {
					target = &rows[i]
				}
			}
			require.NotNil(t, target)
			require.Equal(t, tc.expectBlocked, target.DeleteBlocked, "界面预判与期望不一致")
			require.Equal(t, tc.expectReason, target.DeleteBlockedReason)

			// 同源校验：拿真实 Delete 跑一遍，2xx 与否必须与界面预判一致。
			_, delErr := svc.Delete(node.ID, jdk.ID)
			realBlocked := errors.Is(delErr, ErrJDKInUse)
			require.Equal(t, tc.expectBlocked, realBlocked, "界面预判 (%v) 与 Delete 实际结果 (%v) 不一致", tc.expectBlocked, delErr)
		})
	}
}

// ListWithRefs：引用实例必须带 binding，且 direct/major 两种都能解析出来。
func TestJDKService_ListWithRefs_Bindings(t *testing.T) {
	db := newJDKTestDB(t)
	svc := NewJDKService(db, nil)

	node := &model.Node{Name: "n1", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	jdk := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4", Arch: "x64", Path: "/opt/jdk21", Managed: true}
	require.NoError(t, db.Create(jdk).Error)

	direct := &model.Instance{NodeID: node.ID, Name: "a-direct", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, JDKID: jdk.ID}
	major := &model.Instance{NodeID: node.ID, Name: "b-major", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, JavaMajorVersion: 21}
	require.NoError(t, db.Create(direct).Error)
	require.NoError(t, db.Create(major).Error)

	rows, err := svc.ListWithRefs(node.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 2, rows[0].RefCount)
	// 稳定排序：按实例名升序，a-direct 在 b-major 前。
	require.Equal(t, "a-direct", rows[0].Instances[0].Name)
	require.Equal(t, JDKBindingDirect, rows[0].Instances[0].Binding)
	require.Equal(t, "b-major", rows[0].Instances[1].Name)
	require.Equal(t, JDKBindingMajor, rows[0].Instances[1].Binding)
}

// 无引用的 JDK：Instances 必须是空切片而不是 null——前端直接 .map() 不判空。
func TestJDKService_ListWithRefs_EmptyInstancesIsArray(t *testing.T) {
	db := newJDKTestDB(t)
	svc := NewJDKService(db, nil)

	node := &model.Node{Name: "n1", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	jdk := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4", Arch: "x64", Path: "/opt/jdk21"}
	require.NoError(t, db.Create(jdk).Error)

	rows, err := svc.ListWithRefs(node.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].Instances, "Instances 应为空切片（JSON []）而非 nil（JSON null）")
	require.Empty(t, rows[0].Instances)
}
