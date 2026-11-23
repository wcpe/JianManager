package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 知识静态路由的可用性与路径安全。

// newSkillTestEngine 只挂 skill 路由，隔离其它中间件。
func newSkillTestEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerSkillRoutes(r)
	return r
}

func getSkill(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	r.ServeHTTP(w, req)
	return w
}

func TestSkillRoute_ServesSkillMD(t *testing.T) {
	r := newSkillTestEngine()
	w := getSkill(t, r, "/api/v1/skill/SKILL.md")

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/markdown")
	assert.Contains(t, w.Body.String(), "JianManager", "应返回 SKILL.md 正文")
}

func TestSkillRoute_ServesReferences(t *testing.T) {
	r := newSkillTestEngine()
	for _, p := range []string{
		"/api/v1/skill/references/destructive-ops.md",
		"/api/v1/skill/references/instance-lifecycle.md",
		"/api/v1/skill/references/policy-and-diagnostics.md",
	} {
		w := getSkill(t, r, p)
		require.Equal(t, http.StatusOK, w.Code, p)
		assert.NotEmpty(t, w.Body.String(), p)
	}
}

func TestSkillRoute_AnonymousAccessible(t *testing.T) {
	// 不带任何 Authorization 头也应可读（文档无敏感信息）。
	r := newSkillTestEngine()
	w := getSkill(t, r, "/api/v1/skill/SKILL.md")
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSkillRoute_RejectsNonMarkdown(t *testing.T) {
	r := newSkillTestEngine()
	w := getSkill(t, r, "/api/v1/skill/embed.go")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "只提供 .md")
}

func TestSkillRoute_RejectsTraversal(t *testing.T) {
	r := newSkillTestEngine()
	// Gin 会先对 URL 做一次规范化，但仍可能出现编码后的穿越。
	for _, p := range []string{
		"/api/v1/skill/../../etc/passwd.md",
		"/api/v1/skill/..%2f..%2fetc/passwd.md",
		"/api/v1/skill/references/../../../secret.md",
	} {
		w := getSkill(t, r, p)
		assert.NotEqual(t, http.StatusOK, w.Code, "不应放行: %s", p)
	}
}

func TestSkillRoute_UnknownFileIs404(t *testing.T) {
	r := newSkillTestEngine()
	w := getSkill(t, r, "/api/v1/skill/references/nope.md")
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "不存在")
}

func TestSkillRoute_EmptyPathGuides(t *testing.T) {
	r := newSkillTestEngine()
	w := getSkill(t, r, "/api/v1/skill/")
	assert.NotEqual(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "SKILL.md", "应提示正确用法")
}

func TestSkillRoute_CacheHeader(t *testing.T) {
	r := newSkillTestEngine()
	w := getSkill(t, r, "/api/v1/skill/SKILL.md")
	assert.Contains(t, w.Header().Get("Cache-Control"), "max-age")
}
