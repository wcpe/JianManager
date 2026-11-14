package service

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// TestIsBlockedProbeIP_RejectsInternalRanges SSRF 防护：内网/回环/链路本地/元数据地址必须拦。
func TestIsBlockedProbeIP_RejectsInternalRanges(t *testing.T) {
	blocked := []string{
		"127.0.0.1",       // 回环
		"127.1.2.3",       // 回环段
		"10.0.0.5",        // RFC1918
		"172.16.3.4",      // RFC1918
		"192.168.1.1",     // RFC1918
		"169.254.169.254", // 云元数据（链路本地）
		"100.64.1.1",      // CGNAT
		"0.0.0.0",         // 未指定
		"::1",             // IPv6 回环
		"fe80::1",         // IPv6 链路本地
		"fc00::1",         // IPv6 ULA
		"::ffff:10.0.0.1", // IPv4-mapped 内网
	}
	for _, raw := range blocked {
		ip := net.ParseIP(raw)
		require.NotNil(t, ip, raw)
		require.True(t, isBlockedProbeIP(ip), "%s 应被拦下", raw)
	}
}

// TestIsBlockedProbeIP_AllowsPublic 公网地址不应被误拦。
func TestIsBlockedProbeIP_AllowsPublic(t *testing.T) {
	// 使用文档保留地址验证非私网分类，不依赖真实公共服务。
	for _, raw := range []string{"198.51.100.8", "203.0.113.11", "2001:db8::11"} {
		ip := net.ParseIP(raw)
		require.NotNil(t, ip, raw)
		require.False(t, isBlockedProbeIP(ip), "%s 不应被拦下", raw)
	}
}

// TestValidateProbeHost_BlocksLocalAndMetadata 主机名形式也要拦（localhost / .local / 字面量内网）。
func TestValidateProbeHost_BlocksLocalAndMetadata(t *testing.T) {
	for _, host := range []string{
		"localhost", "localhost:9000", "127.0.0.1:9000", "[::1]:9000",
		"169.254.169.254", "10.1.2.3:22", "s3.local:9000", "app.localhost",
	} {
		require.Error(t, validateProbeHost(host), "%s 应被拒绝", host)
	}
}

// TestValidateProbeHost_AllowsPublicLiteral 公网字面量 IP 允许（无需 DNS 解析）。
func TestValidateProbeHost_AllowsPublicLiteral(t *testing.T) {
	require.NoError(t, validateProbeHost("198.51.100.8:443"))
	require.NoError(t, validateProbeHost("[2001:db8::11]:443"))
}

// TestBackupStorageProbeURL_RejectsNonHTTP 非 http/https 协议必须拒绝（file:// / gopher:// 等）。
func TestBackupStorageProbeURL_RejectsNonHTTP(t *testing.T) {
	for _, ep := range []string{"file:///etc/passwd", "gopher://198.51.100.8/", "ftp://198.51.100.8/x"} {
		_, err := backupStorageProbeURL(ep, true)
		require.Error(t, err, "%s 应被拒绝", ep)
	}
}

// TestBackupStorageProbeURL_RejectsInternalHost 内网 host 在解析阶段就被拒。
func TestBackupStorageProbeURL_RejectsInternalHost(t *testing.T) {
	for _, ep := range []string{"http://127.0.0.1:9000", "https://192.168.1.10", "http://169.254.169.254/latest/meta-data"} {
		_, err := backupStorageProbeURL(ep, true)
		require.Error(t, err, "%s 应被拒绝", ep)
	}
}

// TestProbeDialControl_RejectsInternal 连接前复核：DNS rebinding 手法也会在这一层被拦。
func TestProbeDialControl_RejectsInternal(t *testing.T) {
	require.Error(t, probeDialControl("tcp", "10.0.0.1:22", nil))
	require.Error(t, probeDialControl("tcp", "169.254.169.254:80", nil))
	require.NoError(t, probeDialControl("tcp", "198.51.100.8:53", nil))
}

// TestProbeAllowLoopbackForTest_OnlyAffectsLoopback 测试开关只能放行回环，不能让内网放开。
func TestProbeAllowLoopbackForTest_OnlyAffectsLoopback(t *testing.T) {
	SetProbeAllowLoopbackForTest(true)
	t.Cleanup(func() { SetProbeAllowLoopbackForTest(false) })

	require.False(t, isBlockedProbeIP(net.ParseIP("127.0.0.1")), "回环应被放行")
	require.True(t, isBlockedProbeIP(net.ParseIP("10.0.0.1")), "内网仍应拦下")
	require.True(t, isBlockedProbeIP(net.ParseIP("169.254.169.254")), "元数据地址仍应拦下")
}

// TestBackupProbeHTTPClient_UsesProbeDialControl HTTP 探测客户端必须挂上连接前复核。
//
// 【为何这条断言必要】只校验入口的 validateProbeHost 挡不住 DNS rebinding：
// 校验时域名解析到公网，实际连接时解析到内网。只有 Transport 的 DialContext 挂了
// probeDialControl，才能在拨号那一刻拦住。所以这里直接断言 Transport 配置，
// 而不是仅测 validateProbeHost（后者已单独有测）。
func TestBackupProbeHTTPClient_UsesProbeDialControl(t *testing.T) {
	tr, ok := backupProbeHTTPClient.Transport.(*http.Transport)
	require.True(t, ok, "探测 client 必须有自定义 Transport")
	require.NotNil(t, tr.DialContext, "Transport 必须挂 DialContext（否则连接前复核失效）")

	// 不跟随重定向也必须保持：否则 302 跳板可绕过入口校验。
	require.NotNil(t, backupProbeHTTPClient.CheckRedirect)
	req, err := http.NewRequest(http.MethodGet, "http://198.51.100.8/", nil)
	require.NoError(t, err)
	require.Error(t, backupProbeHTTPClient.CheckRedirect(req, nil), "探测 client 不得跟随重定向")

	// 真实拨号必须被拦（回环/内网），证明 DialContext 确实生效而不是摆设。
	SetProbeAllowLoopbackForTest(false)
	_, err = tr.DialContext(context.Background(), "tcp", "127.0.0.1:9")
	require.Error(t, err, "连接回环应被 DialContext 拦下")
	_, err = tr.DialContext(context.Background(), "tcp", "169.254.169.254:80")
	require.Error(t, err, "连接云元数据地址应被 DialContext 拦下")
}

// TestValidateProbeSpecTarget_RejectsInternalByType 按存储类型校验已保存的 spec。
//
// 覆盖三条路径共用的「下发前校验」：
// s3/webdav 是 URL 形态，sftp 是 host[:port] 形态。
func TestValidateProbeSpecTarget_RejectsInternalByType(t *testing.T) {
	cases := []struct {
		name string
		spec *workerpb.StorageBackendSpec
	}{
		{"s3 回环", &workerpb.StorageBackendSpec{Type: string(model.BackupStorageS3), Endpoint: "http://127.0.0.1:9000"}},
		{"s3 内网", &workerpb.StorageBackendSpec{Type: string(model.BackupStorageS3), Endpoint: "10.0.0.9:9000"}},
		{"s3 元数据", &workerpb.StorageBackendSpec{Type: string(model.BackupStorageS3), Endpoint: "169.254.169.254"}},
		{"webdav 内网", &workerpb.StorageBackendSpec{Type: string(model.BackupStorageWebDAV), Endpoint: "https://192.168.1.5/dav"}},
		{"sftp 回环", &workerpb.StorageBackendSpec{Type: string(model.BackupStorageSFTP), Endpoint: "127.0.0.1"}},
		{"sftp 内网带端口", &workerpb.StorageBackendSpec{Type: string(model.BackupStorageSFTP), Endpoint: "10.1.2.3:22"}},
		{"sftp 元数据", &workerpb.StorageBackendSpec{Type: string(model.BackupStorageSFTP), Endpoint: "169.254.169.254"}},
	}
	for _, tc := range cases {
		require.Error(t, validateProbeSpecTarget(tc.spec), "%s 应被拒绝", tc.name)
	}

	// 公网字面量应放行（不误伤正常配置）。
	require.NoError(t, validateProbeSpecTarget(&workerpb.StorageBackendSpec{
		Type: string(model.BackupStorageS3), Endpoint: "198.51.100.8:443", UseSsl: true,
	}))
	// 本地存储不拦（交给 Worker 报“无需远程传输”）。
	require.NoError(t, validateProbeSpecTarget(&workerpb.StorageBackendSpec{Type: "local"}))
}

// TestTestConnection_BlocksInternalEndpointBeforeDispatch 已保存的内网 endpoint 不得下发 Worker。
//
// 这条是「已保存配置」路径的关键回归：TestCandidate/TestSaved 在保存时校验，
// 但历史脏数据可能已经在库里。若不拦，Worker 会替调用方去探测内网地址。
func TestTestConnection_BlocksInternalEndpointBeforeDispatch(t *testing.T) {
	db := newStorageTestDB(t)
	svc := NewBackupStorageService(db)
	st, err := svc.Create(&model.BackupStorage{
		Name: "legacy-internal", Type: model.BackupStorageS3,
		Endpoint: "http://10.0.0.9:9000", Bucket: "b",
		AccessKeyEnv: "${AK}", SecretKeyEnv: "${SK}",
	})
	require.NoError(t, err)
	t.Setenv("AK", "a")
	t.Setenv("SK", "s")

	// 无 Worker 在线也不应影响结论：校验发生在下发之前。
	res, err := svc.TestConnection(st.ID)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.OK, "内网 endpoint 不应被判为连接成功")
	require.Equal(t, "ENDPOINT_BLOCKED", res.ErrorCode, "应报 ENDPOINT_BLOCKED 而不是走到 Worker")
}

// TestBackupProbeHTTPClient_ProxyDisabled 探测 client 必须显式禁用代理。
//
// 【为何这条断言有判别力】若 Transport.Proxy 是 http.ProxyFromEnvironment，
// 设了 HTTP_PROXY 后拨号目标会变成**代理的地址**，probeDialControl 校验的也是代理，
// 真正的目标 IP 由代理解析——目标 IP 准入形同虚设。
// 所以这里真的设 HTTP_PROXY 环境变量，再验证 Transport 不会把请求交给代理。
func TestBackupProbeHTTPClient_ProxyDisabled(t *testing.T) {
	tr, ok := backupProbeHTTPClient.Transport.(*http.Transport)
	require.True(t, ok)
	require.Nil(t, tr.Proxy, "探测 Transport 必须显式禁用代理（nil），否则目标 IP 准入失效")

	// 真的设置代理环境变量：确认探测 client 不会把请求交给它。
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	// ProxyFromEnvironment 会读环境变量；用同一个请求验证它确实会接管，
	// 而我们的探测 Transport 不会——两相对比才有判别力。
	req, err := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	require.NoError(t, err)

	proxied, perr := http.ProxyFromEnvironment(req)
	require.NoError(t, perr)
	require.NotNil(t, proxied, "前置条件：设了 HTTP_PROXY 后 ProxyFromEnvironment 应当接管")
	require.Nil(t, tr.Proxy, "而探测 Transport 的 Proxy 必须是 nil，不受环境变量影响")
}

// TestValidateProbeSpecTarget_ProxyEnvDoesNotBypass 即使环境配了代理，内网目标仍被拒。
//
// 这是「代理不会变成绕过通道」的端到端侧面证明：校验发生在请求发起前，
// 与 Transport 是否走代理无关。
func TestValidateProbeSpecTarget_ProxyEnvDoesNotBypass(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")

	err := validateProbeSpecTarget(&workerpb.StorageBackendSpec{
		Type: string(model.BackupStorageS3), Endpoint: "10.0.0.9:9000",
	})
	require.Error(t, err, "配了代理也不得放行内网目标")
	require.Contains(t, err.Error(), "出站范围内")
}

// TestProbeAllowPrivate_CP 部署显式放行的私网段才可探测（与 Worker 同口径）。
func TestProbeAllowPrivate_CP(t *testing.T) {
	SetProbeAllowPrivateForTest(nil)
	defer SetProbeAllowPrivateForTest(nil)
	require.True(t, isBlockedProbeIP(net.ParseIP("10.0.0.9")), "未配置时 10/8 应拒")

	SetProbeAllowPrivateForTest([]string{"10.0.0.0/8"})
	require.False(t, isBlockedProbeIP(net.ParseIP("10.0.0.9")), "显式放行的 10/8 应允许")
	require.True(t, isBlockedProbeIP(net.ParseIP("192.168.1.5")), "未放行的 192.168/16 仍应拒")
	require.True(t, isBlockedProbeIP(net.ParseIP("169.254.169.254")), "云元数据永拒")

	// 按类型的 spec 校验也遵循放行列表。
	require.NoError(t, validateProbeSpecTarget(&workerpb.StorageBackendSpec{
		Type: string(model.BackupStorageS3), Endpoint: "10.0.0.9:9000",
	}))
	require.Error(t, validateProbeSpecTarget(&workerpb.StorageBackendSpec{
		Type: string(model.BackupStorageS3), Endpoint: "192.168.1.5:9000",
	}))
}

// TestProbeSFTPStorage_HonorsContextDeadline CP 侧 SFTP 探测不得被 banner 黑洞挂住。
//
// 【缺陷现场】probeSFTPStorage 在 TCP 建连后直接 `ssh.NewClientConn`，
// 而它自己不读 ctx；`ssh.ClientConfig.Timeout` 与 `net.Dialer.Timeout`
// 都只覆盖到 TCP 建连。对端 accept 后不发 banner 时握手会永久阻塞，
// 调用方的 15s 超时形同虚设。
func TestProbeSFTPStorage_HonorsContextDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		accepted <- c
		// 关键：接受后什么都不写（不发 SSH banner）。
	}()

	SetProbeAllowLoopbackForTest(true)
	defer SetProbeAllowLoopbackForTest(false)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = probeSFTPStorage(ctx, model.BackupStorage{
		Type: model.BackupStorageSFTP, Endpoint: ln.Addr().String(),
	}, "u", "p")
	elapsed := time.Since(start)

	require.Error(t, err, "黑洞 banner 应报错而不是永久挂住")
	require.Less(t, elapsed, 5*time.Second, "应在 ctx 超时后很快返回（实测 %v）", elapsed)

	select {
	case c := <-accepted:
		_ = c.Close()
	default:
	}
}

// TestProbeAllowPrivate_SpecialAddressesNeverAllowed 配置 CIDR 不得把特殊地址变成可探测。
//
// 【为何这条必要】放行列表如果在 Loopback/LinkLocal/Multicast/unspecified 判断**之前**生效，
// 运维配 `127.0.0.0/8` 或 `0.0.0.0/0` 就能让 CP 去扫本机端口 / 整片地址空间，
// 与「仅合法私网例外」的承诺不符。
func TestProbeAllowPrivate_SpecialAddressesNeverAllowed(t *testing.T) {
	// 即使把特殊段写进放行列表，也必须仍然拒绝。
	SetProbeAllowPrivateForTest([]string{"127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4", "0.0.0.0/8"})
	defer SetProbeAllowPrivateForTest(nil)

	require.True(t, isBlockedProbeIP(net.ParseIP("127.0.0.1")), "回环永不可通过配置放行")
	require.True(t, isBlockedProbeIP(net.ParseIP("169.254.1.1")), "链路本地永不可通过配置放行")
	require.True(t, isBlockedProbeIP(net.ParseIP("224.0.0.1")), "组播永不可通过配置放行")
	require.True(t, isBlockedProbeIP(net.ParseIP("0.0.0.0")), "未指定地址永不可放行")

	// 0.0.0.0/0 这种“全放行”配置也不得把公网/特殊段纳入。
	SetProbeAllowPrivateForTest([]string{"0.0.0.0/0"})
	require.True(t, isBlockedProbeIP(net.ParseIP("127.0.0.1")), "0.0.0.0/0 下回环仍拒")
	require.True(t, isBlockedProbeIP(net.ParseIP("169.254.169.254")), "0.0.0.0/0 下元数据仍拒")
	// 合法私网在 0.0.0.0/0 下可以放行（这是配置者的明确意图）。
	require.False(t, isBlockedProbeIP(net.ParseIP("10.0.0.9")), "0.0.0.0/0 下合法私网可放行")
}
