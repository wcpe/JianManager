package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 用户令牌收窄回归测试：只读令牌调写接口必须 403，
// scope 外实例必须 403，scope 内读取放行。

func setupTokenScopeChain(t *testing.T) (*gin.Engine, *service.UserTokenService, *service.AuthzService, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.UserToken{}, &model.DeviceCode{},
		&model.RoleTemplate{}, &model.RolePermission{},
		&model.UserRoleBinding{}, &model.UserPermissionOverride{},
		&model.GroupMember{}, &model.GroupInstance{}, &model.Instance{}, &model.Node{},
	))
	authz := service.NewAuthzService(db)
	require.NoError(t, authz.Permissions().SeedSystemRoles())
	tokenSvc := service.NewUserTokenService(db, authz)

	r := gin.New()
	r.Use(UserTokenAuth(tokenSvc))
	r.Use(JWTAuth("test-secret"))
	r.Use(LoadAccess(authz))
	r.GET("/read", RequirePerm("instance.read"), func(c *gin.Context) { c.Status(http.StatusOK) })
	r.POST("/write", RequirePerm("instance.write"), func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/inst/:id", func(c *gin.Context) {
		var id uint
		if _, err := fmt.Sscanf(c.Param("id"), "%d", &id); err != nil || id == 0 {
			c.Status(http.StatusBadRequest)
			return
		}
		ok, err := authz.CanAccessInstance(getAccess(c), id)
		require.NoError(t, err)
		if !ok {
			c.Status(http.StatusForbidden)
			return
		}
		c.Status(http.StatusOK)
	})
	return r, tokenSvc, authz, db
}

func tokenReq(r *gin.Engine, method, path, token string) int {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

func TestUserToken_CapabilityNarrowingEnforced(t *testing.T) {
	r, tokenSvc, authz, db := setupTokenScopeChain(t)

	user := model.User{Username: "u1", Password: "x", Role: model.RoleMember, Status: model.UserStatusActive}
	require.NoError(t, db.Create(&user).Error)
	// 绑定拥有 instance.read + instance.write 的角色，使令牌能力落在用户权限内。
	role, err := authz.Permissions().CreateRole("rw", "", []string{"instance.read", "instance.write"})
	require.NoError(t, err)
	require.NoError(t, authz.Permissions().BindUserRole(user.ID, role.ID))

	// 只读令牌：只声明 instance.read
	res, err := tokenSvc.Create(user.ID, service.CreateUserTokenRequest{
		Name:         "ro",
		Capabilities: []string{"instance.read"},
	})
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, tokenReq(r, "GET", "/read", res.Plaintext), "scope 内读取应放行")
	assert.Equal(t, http.StatusForbidden, tokenReq(r, "POST", "/write", res.Plaintext), "只读令牌写操作应 403")
}

func TestUserToken_InstanceScopeEnforced(t *testing.T) {
	r, tokenSvc, _, db := setupTokenScopeChain(t)

	user := model.User{Username: "u2", Password: "x", Role: model.RolePlatformAdmin, Status: model.UserStatusActive}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&model.Instance{Name: "i1", NodeID: 1}).Error)
	require.NoError(t, db.Create(&model.Instance{Name: "i2", NodeID: 2}).Error)

	// 令牌 scope 限定 instance 1
	res, err := tokenSvc.Create(user.ID, service.CreateUserTokenRequest{
		Name:              "scoped",
		ScopedInstanceIDs: []uint{1},
	})
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, tokenReq(r, "GET", "/inst/1", res.Plaintext))
	assert.Equal(t, http.StatusForbidden, tokenReq(r, "GET", "/inst/2", res.Plaintext), "scope 外实例应 403")
}
