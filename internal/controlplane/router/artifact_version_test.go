package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

func TestArtifactVersionUploadLocalServerProbe(t *testing.T) {
	db := setupTestDB(t)
	root, err := dataroot.Init(filepath.Join(t.TempDir(), "data"))
	require.NoError(t, err)
	assets := service.NewAssetService(db, root)
	versions := service.NewArtifactVersionService(db, assets)

	r := gin.New()
	NewArtifactVersionHandler(versions).RegisterRoutes(r.Group(""))

	jar := []byte("local-serverprobe")
	w := uploadLocalServerProbe(t, r, "0.1.0", "ServerProbe-0.1.0.jar", jar)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var uploaded model.ArtifactVersion
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &uploaded))
	require.Equal(t, "0.1.0", uploaded.Version)
	require.NotZero(t, uploaded.AssetID)
	require.NotNil(t, uploaded.CachedAt)

	duplicate := uploadLocalServerProbe(t, r, "0.1.0", "ServerProbe-0.1.0.jar", jar)
	require.Equal(t, http.StatusConflict, duplicate.Code, duplicate.Body.String())
	require.Equal(t, "VERSION_EXISTS", parseJSON(t, duplicate)["error"])

	invalid := uploadLocalServerProbe(t, r, "0.1.1", "ServerProbe.zip", jar)
	require.Equal(t, http.StatusBadRequest, invalid.Code, invalid.Body.String())

	tooLarge := uploadLocalServerProbeWithContentLength(t, r, "0.1.2", "ServerProbe-0.1.2.jar", jar, service.ServerProbeUploadMaxSize+(1<<20)+1)
	require.Equal(t, http.StatusRequestEntityTooLarge, tooLarge.Code, tooLarge.Body.String())
	require.Equal(t, "UPLOAD_TOO_LARGE", parseJSON(t, tooLarge)["error"])
}

func TestArtifactVersionUploadLocalServerProbe_DoesNotParseWholeMultipartForm(t *testing.T) {
	db := setupTestDB(t)
	root, err := dataroot.Init(filepath.Join(t.TempDir(), "data"))
	require.NoError(t, err)
	versions := service.NewArtifactVersionService(db, service.NewAssetService(db, root))

	r := gin.New()
	parsedWholeForm := false
	r.Use(func(c *gin.Context) {
		c.Next()
		form := c.Request.MultipartForm
		parsedWholeForm = form != nil && (len(form.Value) > 0 || len(form.File) > 0)
	})
	NewArtifactVersionHandler(versions).RegisterRoutes(r.Group(""))

	w := uploadLocalServerProbe(t, r, "0.1.0", "ServerProbe-0.1.0.jar", []byte("local-serverprobe"))
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.False(t, parsedWholeForm, "上传不能调用 ParseMultipartForm；jar 必须直接流入 CAS")
}

func TestArtifactVersionUploadLocalServerProbe_RejectsTrailingFilePart(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)

	w := uploadLocalServerProbeWithTrailingFile(t, r, "/api/v1/artifact-packages/serverprobe/versions/upload", adminToken)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Equal(t, "INVALID_REQUEST", parseJSON(t, w)["error"])
	var count int64
	require.NoError(t, db.Model(&model.ArtifactVersion{}).Where("version = ?", "0.1.0").Count(&count).Error)
	require.Zero(t, count)
}

func TestArtifactVersionUploadLocalServerProbe_AdminRouteValidation(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "artifact-member", "password123")
	const path = "/api/v1/artifact-packages/serverprobe/versions/upload"

	created := uploadLocalServerProbeRequest(t, r, path, adminToken, "0.1.0", "ServerProbe-0.1.0.jar", []byte("local-serverprobe"), true, true, true, 0)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	secondVersion := uploadLocalServerProbeRequest(t, r, path, adminToken, "0.1.1", "ServerProbe-0.1.1.jar", []byte("local-serverprobe-0.1.1"), true, true, true, 0)
	require.Equal(t, http.StatusCreated, secondVersion.Code, secondVersion.Body.String())

	forbidden := uploadLocalServerProbeRequest(t, r, path, memberToken, "0.1.1", "ServerProbe-0.1.1.jar", []byte("local-serverprobe"), true, true, true, 0)
	require.Equal(t, http.StatusForbidden, forbidden.Code, forbidden.Body.String())

	missingVersion := uploadLocalServerProbeRequest(t, r, path, adminToken, "", "ServerProbe-0.1.2.jar", []byte("local-serverprobe"), false, true, true, 0)
	require.Equal(t, http.StatusBadRequest, missingVersion.Code, missingVersion.Body.String())
	require.Equal(t, "INVALID_REQUEST", parseJSON(t, missingVersion)["error"])

	missingFile := uploadLocalServerProbeRequest(t, r, path, adminToken, "0.1.3", "", nil, true, false, true, 0)
	require.Equal(t, http.StatusBadRequest, missingFile.Code, missingFile.Body.String())
	require.Equal(t, "INVALID_REQUEST", parseJSON(t, missingFile)["error"])

	versionAfterFile := uploadLocalServerProbeRequest(t, r, path, adminToken, "0.1.4", "ServerProbe-0.1.4.jar", []byte("local-serverprobe"), true, true, false, 0)
	require.Equal(t, http.StatusBadRequest, versionAfterFile.Code, versionAfterFile.Body.String())
	require.Equal(t, "INVALID_REQUEST", parseJSON(t, versionAfterFile)["error"])

	unsupportedField := uploadLocalServerProbeRequestWithField(t, r, path, adminToken, "0.1.5", "ServerProbe-0.1.5.jar", []byte("local-serverprobe"), "unexpected")
	require.Equal(t, http.StatusBadRequest, unsupportedField.Code, unsupportedField.Body.String())
	require.Equal(t, "INVALID_REQUEST", parseJSON(t, unsupportedField)["error"])

	tooLarge := uploadLocalServerProbeRequest(t, r, path, adminToken, "0.1.6", "ServerProbe-0.1.6.jar", make([]byte, service.ServerProbeUploadMaxSize+1), true, true, true, 0)
	require.Equal(t, http.StatusRequestEntityTooLarge, tooLarge.Code, tooLarge.Body.String())
	require.Equal(t, "UPLOAD_TOO_LARGE", parseJSON(t, tooLarge)["error"])
}

func uploadLocalServerProbe(t *testing.T, r http.Handler, version, filename string, content []byte) *httptest.ResponseRecorder {
	return uploadLocalServerProbeWithContentLength(t, r, version, filename, content, 0)
}

func uploadLocalServerProbeWithContentLength(t *testing.T, r http.Handler, version, filename string, content []byte, contentLength int64) *httptest.ResponseRecorder {
	t.Helper()
	return uploadLocalServerProbeRequest(t, r, "/artifact-packages/serverprobe/versions/upload", "", version, filename, content, true, true, true, contentLength)
}

func uploadLocalServerProbeRequest(t *testing.T, r http.Handler, path, token, version, filename string, content []byte, includeVersion, includeFile, versionBeforeFile bool, contentLength int64) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	writeVersion := func() {
		require.NoError(t, form.WriteField("version", version))
	}
	writeFile := func() {
		part, err := form.CreateFormFile("file", filename)
		require.NoError(t, err)
		_, err = part.Write(content)
		require.NoError(t, err)
	}
	if includeVersion && versionBeforeFile {
		writeVersion()
	}
	if includeFile {
		writeFile()
	}
	if includeVersion && !versionBeforeFile {
		writeVersion()
	}
	require.NoError(t, form.Close())

	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentLength > 0 {
		req.ContentLength = contentLength
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func uploadLocalServerProbeRequestWithField(t *testing.T, r http.Handler, path, token, version, filename string, content []byte, field string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	require.NoError(t, form.WriteField("version", version))
	require.NoError(t, form.WriteField(field, "value"))
	part, err := form.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, form.Close())

	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func uploadLocalServerProbeWithTrailingFile(t *testing.T, r http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	require.NoError(t, form.WriteField("version", "0.1.0"))
	for _, filename := range []string{"ServerProbe-0.1.0.jar", "ServerProbe-duplicate.jar"} {
		part, err := form.CreateFormFile("file", filename)
		require.NoError(t, err)
		_, err = part.Write([]byte("local-serverprobe"))
		require.NoError(t, err)
	}
	require.NoError(t, form.Close())

	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestArtifactVersionDownload_RequiresShortToken(t *testing.T) {
	db := setupTestDB(t)
	root, err := dataroot.Init(filepath.Join(t.TempDir(), "data"))
	require.NoError(t, err)
	assets := service.NewAssetService(db, root)
	versions := service.NewArtifactVersionService(db, assets)
	pkg, source, err := versions.EnsureDefaultServerProbe()
	require.NoError(t, err)
	jar := []byte("probe-jar")
	asset, err := assets.Ingest(bytes.NewReader(jar), service.IngestParams{
		Type: model.AssetTypeServerProbe, Name: "ServerProbe", Version: "0.2.0", Filename: "ServerProbe-0.2.0.jar",
	})
	require.NoError(t, err)
	sum := sha256.Sum256(jar)
	version := &model.ArtifactVersion{
		PackageID: pkg.ID, SourceID: source.ID, Version: "0.2.0", ReleaseRef: "v0.2.0",
		AssetName: "ServerProbe-0.2.0.jar", ExpectedSHA256: hex.EncodeToString(sum[:]), SourceURL: "https://source.example/probe.jar", AssetID: asset.ID,
	}
	require.NoError(t, db.Create(version).Error)

	r := gin.New()
	h := NewArtifactVersionHandler(versions)
	h.RegisterDownloadRoutes(r)
	token, err := versions.IssueProbeDownloadToken(service.ProbeDownloadTokenScope{VersionID: version.ID, NodeUUID: "node-1"})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe-artifacts/"+itoa(version.ID)+"/download?token="+token, nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, jar, w.Body.Bytes())

	bad := httptest.NewRecorder()
	r.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/probe-artifacts/"+itoa(version.ID)+"/download?token=bad", nil))
	require.Equal(t, http.StatusForbidden, bad.Code)

}
