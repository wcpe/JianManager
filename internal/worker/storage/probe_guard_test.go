package storage

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/http/httpproxy"
)

// TestIsBlockedProbeIP_Worker 与 CP 同套网段枚举：内网/回环/元数据必须拦。
func TestIsBlockedProbeIP_Worker(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.0.0.5", "172.16.3.4", "192.168.1.1",
		"169.254.169.254", "100.64.1.1", "0.0.0.0",
		"::1", "fe80::1", "fc00::1", "::ffff:10.0.0.1",
	}
	for _, raw := range blocked {
		ip := net.ParseIP(raw)
		require.NotNil(t, ip, raw)
		require.True(t, isBlockedProbeIP(ip), "%s 应被拦下", raw)
	}
	// 使用文档保留地址验证非私网分类，不依赖真实公共服务。
	for _, raw := range []string{"198.51.100.8", "203.0.113.11", "2001:db8::11"} {
		ip := net.ParseIP(raw)
		require.NotNil(t, ip, raw)
		require.False(t, isBlockedProbeIP(ip), "%s 不应被拦下", raw)
	}
}

// TestValidateProbeTarget_Worker 主机名与字面量都要拦；公网字面量放行。
func TestValidateProbeTarget_Worker(t *testing.T) {
	for _, host := range []string{
		"localhost", "localhost:9000", "127.0.0.1:9000", "[::1]:9000",
		"169.254.169.254", "10.1.2.3:22", "s3.local:9000", "app.localhost",
	} {
		require.Error(t, validateProbeTarget(host), "%s 应被拒绝", host)
	}
	require.NoError(t, validateProbeTarget("198.51.100.8:443"))
	require.NoError(t, validateProbeTarget("[2001:db8::11]:443"))
}

// TestValidateProbeConfig_ByBackendType 三类 endpoint 形态各自校验。
func TestValidateProbeConfig_ByBackendType(t *testing.T) {
	bad := []Config{
		{Type: TypeS3, Endpoint: "http://127.0.0.1:9000", Bucket: "b"},
		{Type: TypeS3, Endpoint: "10.0.0.9:9000", Bucket: "b"},
		{Type: TypeWebDAV, Endpoint: "https://192.168.1.5/dav"},
		{Type: TypeSFTP, Endpoint: "127.0.0.1", AccessKey: "u"},
		{Type: TypeSFTP, Endpoint: "10.1.2.3:22", AccessKey: "u"},
	}
	for _, cfg := range bad {
		require.Error(t, validateProbeConfig(cfg), "%+v 应被拒绝", cfg)
	}

	// 公网 + 非 http(s) 协议、本地类型。
	require.NoError(t, validateProbeConfig(Config{Type: TypeS3, Endpoint: "198.51.100.8:443", Bucket: "b", UseSSL: true}))
	require.NoError(t, validateProbeConfig(Config{Type: TypeLocal}))
	require.Error(t, validateProbeConfig(Config{Type: TypeWebDAV, Endpoint: "file:///etc/passwd"}))
}

// TestProbe_RejectsInternalBeforeAnyRequest 探测必须在下发请求前就拒。
//
// 用一个会记录命中次数的 httptest 服务验证：即使 endpoint 指向真实监听端口，
// 只要它在回环段且未开测试例外，就不得被访问。
func TestProbe_RejectsInternalBeforeAnyRequest(t *testing.T) {
	hit := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	SetProbeAllowLoopbackForTest(false)
	defer SetProbeAllowLoopbackForTest(false)

	err := Probe(context.Background(), Config{Type: TypeWebDAV, Endpoint: srv.URL, AccessKey: "u", SecretKey: "p"})
	require.Error(t, err, "回环目标应被拒")
	require.Zero(t, hit, "被拒的探测不得发出任何请求")
}

// TestProbe_ProbeClientBlocksAfterAllowLoopback 即使开了回环例外，内网/元数据仍要拦。
func TestProbe_ProbeClientBlocksAfterAllowLoopback(t *testing.T) {
	SetProbeAllowLoopbackForTest(true)
	defer SetProbeAllowLoopbackForTest(false)

	tr, ok := probeHTTPClient.Transport.(*http.Transport)
	require.True(t, ok, "探测 client 必须有自定义 Transport")
	require.NotNil(t, tr.DialContext)

	// 回环放行（测试需要），但内网与元数据仍拒。
	require.False(t, isBlockedProbeIP(net.ParseIP("127.0.0.1")))
	require.True(t, isBlockedProbeIP(net.ParseIP("10.0.0.1")))
	require.True(t, isBlockedProbeIP(net.ParseIP("169.254.169.254")))

	// 真实拨号：元数据地址必须被 DialContext 拦下。
	_, err := tr.DialContext(context.Background(), "tcp", "169.254.169.254:80")
	require.Error(t, err)
}

// TestProbeHTTPClient_DoesNotFollowRedirects 探测 client 不得跟随重定向（防 302 跳板）。
func TestProbeHTTPClient_DoesNotFollowRedirects(t *testing.T) {
	require.NotNil(t, probeHTTPClient.CheckRedirect)
	req, err := http.NewRequest(http.MethodGet, "http://198.51.100.8/", nil)
	require.NoError(t, err)
	require.Error(t, probeHTTPClient.CheckRedirect(req, nil))
}

// TestBackendHTTPClient_ProbeVsTransfer 正式传输不得带 SSRF 限制（内网 MinIO 必须能用）。
//
// 这是本次修复最容易误伤的一条：如果把限制加到 defaultHTTPClient 上，
// 内网自建 MinIO/NAS 的日常备份就全断了。
func TestBackendHTTPClient_ProbeVsTransfer(t *testing.T) {
	transfer := backendHTTPClient(Config{Type: TypeS3})
	require.Nil(t, transfer.Transport, "正式传输应沿用默认 Transport（不限制目标）")
	require.Nil(t, transfer.CheckRedirect, "正式传输应允许重定向")

	probe := backendHTTPClient(Config{Type: TypeS3, probe: true})
	require.Same(t, probeHTTPClient, probe, "探测态必须用受限 client")
}

// TestNewSFTPBackend_CarriesProbeFlag 探测态标记要传到后端（dialer 选择依赖它）。
func TestNewSFTPBackend_CarriesProbeFlag(t *testing.T) {
	b, err := newSFTPBackend(Config{Type: TypeSFTP, Endpoint: "198.51.100.8", AccessKey: "u", probe: true})
	require.NoError(t, err)
	backend, ok := b.(*sftpBackend)
	require.True(t, ok)
	require.True(t, backend.probe, "探测态必须传递到 sftpBackend")

	b2, err := newSFTPBackend(Config{Type: TypeSFTP, Endpoint: "198.51.100.8", AccessKey: "u"})
	require.NoError(t, err)
	require.False(t, b2.(*sftpBackend).probe, "正式传输不得标记为探测")
}

// TestProbeHTTPClient_ProxyDisabled 探测 client 必须显式禁用代理。
//
// 【为何这条有判别力】若 Transport.Proxy = ProxyFromEnvironment，设了 HTTP_PROXY 后
// DialContext 看到的是**代理地址**，probeDialControl 校验的也是代理，
// 真正的目标 IP 由代理解析——目标 IP 准入形同虚设。
func TestProbeHTTPClient_ProxyDisabled(t *testing.T) {
	tr, ok := probeHTTPClient.Transport.(*http.Transport)
	require.True(t, ok)
	require.Nil(t, tr.Proxy, "探测 Transport 必须显式禁用代理（nil）")

	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	t.Setenv("REQUEST_METHOD", "")
	req, err := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	require.NoError(t, err)

	// 现场解析本用例的配置，避开标准库的进程级首次读取缓存。
	proxied, perr := httpproxy.FromEnvironment().ProxyFunc()(req.URL)
	require.NoError(t, perr)
	require.NotNil(t, proxied, "当前代理环境应当接管请求")
	require.Equal(t, "http://127.0.0.1:1", proxied.String())
	// 而探测 Transport 不受影响。
	require.Nil(t, tr.Proxy)
}

// TestTransferHTTPClient_KeepsProxyEnv 正式传输**保留**代理（与探测相反，不得误伤）。
//
// 部署契约：正式上传/下载是用户明确配置的备份目的地，内网 MinIO/NAS 合法，
// 且企业环境经常必须经代理出站。把限制加到正式路径会直接打断日常备份。
func TestTransferHTTPClient_KeepsProxyEnv(t *testing.T) {
	client := backendHTTPClient(Config{Type: TypeS3})
	require.Nil(t, client.Transport, "正式传输应沿用默认 Transport（含 ProxyFromEnvironment、不限制目标）")
	require.Nil(t, client.CheckRedirect, "正式传输应允许重定向")
}

// TestProbeAllowPrivate_ExplicitCIDROnly 部署显式放行的私网段才可探测（不是粗开关）。
//
// 【行为一致性】内网自建 MinIO/NAS（10.x:9000）正式上传/下载本来就能连，
// 若探测一刀切拒，运维就无法自检备份链路——同一个后端「能备份、不能测连接」自相矛盾。
// 但也不能静默放行所有内网：放行粒度必须是精确 CIDR（与 httpclient.GuardOptions.AllowPrivate 同构）。
func TestProbeAllowPrivate_ExplicitCIDROnly(t *testing.T) {
	// 默认（未配置）：全部私网拒。
	SetProbeAllowPrivateForTest(nil)
	defer SetProbeAllowPrivateForTest(nil)
	require.True(t, isBlockedProbeIP(net.ParseIP("10.0.0.9")), "未配置时 10/8 应拒")
	require.True(t, isBlockedProbeIP(net.ParseIP("192.168.1.5")), "未配置时 192.168/16 应拒")

	// 只放行 10.0.0.0/8：同段放行，其他私网仍拒。
	SetProbeAllowPrivateForTest([]string{"10.0.0.0/8"})
	require.False(t, isBlockedProbeIP(net.ParseIP("10.0.0.9")), "显式放行的 10/8 应允许探测")
	require.True(t, isBlockedProbeIP(net.ParseIP("192.168.1.5")), "未放行的 192.168/16 仍应拒")
	require.True(t, isBlockedProbeIP(net.ParseIP("172.16.3.4")), "未放行的 172.16/12 仍应拒")

	// 元数据地址即使落在放行段内也永拒；链路本地同理（不属于“合法私网”）。
	SetProbeAllowPrivateForTest([]string{"169.254.0.0/16"})
	require.True(t, isBlockedProbeIP(net.ParseIP("169.254.169.254")), "云元数据地址永拒（即使被放行段覆盖）")
	require.True(t, isBlockedProbeIP(net.ParseIP("169.254.1.1")), "链路本地属特殊地址，即使配了 CIDR 也不放行")
}

// TestValidateProbeConfig_AllowPrivateRespected 按类型的校验也遵循放行列表。
func TestValidateProbeConfig_AllowPrivateRespected(t *testing.T) {
	SetProbeAllowPrivateForTest([]string{"10.0.0.0/8"})
	defer SetProbeAllowPrivateForTest(nil)

	// 字面量 IP：直接命中放行段。
	require.NoError(t, validateProbeConfig(Config{Type: TypeS3, Endpoint: "10.0.0.9:9000", Bucket: "b"}))
	// 未放行段仍拒。
	require.Error(t, validateProbeConfig(Config{Type: TypeS3, Endpoint: "192.168.1.5:9000", Bucket: "b"}))
}

// TestSFTPDial_HonorsContextDeadline 握手不得被“只接受连接不发 banner”的黑洞挂住。
//
// 【缺陷现场】ssh.NewClientConn 拿到的已是建好的 TCP 连接，它自己不读 ctx；
// net.Dialer.Timeout 只管到 TCP 建连。对端若接受后不发 banner，握手会永久阻塞。
// 这里用一个“接受连接但永不写字节”的假服务器复现，断言 dial 在超时内返回错误。
func TestSFTPDial_HonorsContextDeadline(t *testing.T) {
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
		// 关键：接受后什么都不写（不发送 SSH banner）。
	}()

	// 放行回环（httptest 同理），否则会先被 SSRF 门禁拦下，测不到超时逻辑。
	SetProbeAllowLoopbackForTest(true)
	defer SetProbeAllowLoopbackForTest(false)

	b, berr := newSFTPBackend(Config{
		Type: TypeSFTP, Endpoint: ln.Addr().String(), AccessKey: "u", SecretKey: "p", probe: true,
	})
	require.NoError(t, berr)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, dialErr := b.(*sftpBackend).dial(ctx)
	elapsed := time.Since(start)

	require.Error(t, dialErr, "黑洞 banner 应报错而不是永久挂住")
	require.Less(t, elapsed, 5*time.Second, "应在 ctx 超时后很快返回（实测 %v）", elapsed)

	// 清理假服务器持有的连接。
	select {
	case c := <-accepted:
		_ = c.Close()
	default:
	}
}

// TestSFTPConn_ClosesOnContextCancelAfterHandshake 握手成功后，ctx 取消仍应释放阻塞中的 sess.Run。
//
// 【缺陷现场】第一版只在 dial 内部起监护并在函数返回时 `defer close(done)` 关掉它，
// 于是**握手之后**的长命令没人看管：调用方 ctx 取消时 sess.Run 会一直挂到对端自己结束。
// 修法是把监护活到连接关闭（sftpConn.Close）。
//
// 本测试用一个能完成 SSH 握手、但执行命令时永不返回的假 SSH 服务器复现。
func TestSFTPConn_ClosesOnContextCancelAfterHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		c, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer c.Close()
		cfg := &ssh.ServerConfig{
			PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
				return nil, nil // 任何密码都接受
			},
		}
		// 用固定 host key（测试专用，非生产密钥）。
		key, kerr := rsa.GenerateKey(rand.Reader, 2048)
		if kerr != nil {
			return
		}
		signer, serr := ssh.NewSignerFromKey(key)
		if serr != nil {
			return
		}
		cfg.AddHostKey(signer)

		sconn, chans, reqs, herr := ssh.NewServerConn(c, cfg)
		if herr != nil {
			return
		}
		defer sconn.Close()
		go ssh.DiscardRequests(reqs)

		for newCh := range chans {
			ch, chReqs, cerr := newCh.Accept()
			if cerr != nil {
				continue
			}
			go ssh.DiscardRequests(chReqs)
			// 关键：不回应 exec 请求，也不关闭 channel —— 命令永不返回。
			_ = ch
		}
	}()

	SetProbeAllowLoopbackForTest(true)
	defer SetProbeAllowLoopbackForTest(false)

	b, berr := newSFTPBackend(Config{
		Type: TypeSFTP, Endpoint: ln.Addr().String(), AccessKey: "u", SecretKey: "p",
	})
	require.NoError(t, berr)

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	start := time.Now()
	// Delete 内部是 sess.Run("rm -f ...")，命令永不返回，只能靠 ctx 取消释放。
	err = b.Delete(ctx, "some/key.tar.gz")
	elapsed := time.Since(start)

	require.Error(t, err, "ctx 取消后 sess.Run 应返回错误而不是永久挂住")
	require.Less(t, elapsed, 8*time.Second, "应在 ctx 取消后很快返回（实测 %v）", elapsed)

	select {
	case <-serverDone:
	case <-time.After(3 * time.Second):
	}
}

// TestProbeAllowPrivate_SpecialAddressesNeverAllowed_Worker 配置 CIDR 不得放行特殊地址。
func TestProbeAllowPrivate_SpecialAddressesNeverAllowed_Worker(t *testing.T) {
	SetProbeAllowPrivateForTest([]string{"127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4", "0.0.0.0/8"})
	defer SetProbeAllowPrivateForTest(nil)

	require.True(t, isBlockedProbeIP(net.ParseIP("127.0.0.1")), "回环永不可通过配置放行")
	require.True(t, isBlockedProbeIP(net.ParseIP("169.254.1.1")), "链路本地永不可通过配置放行")
	require.True(t, isBlockedProbeIP(net.ParseIP("224.0.0.1")), "组播永不可通过配置放行")
	require.True(t, isBlockedProbeIP(net.ParseIP("0.0.0.0")), "未指定地址永不可放行")

	// 0.0.0.0/0：特殊段仍拒，合法私网可放行。
	SetProbeAllowPrivateForTest([]string{"0.0.0.0/0"})
	require.True(t, isBlockedProbeIP(net.ParseIP("127.0.0.1")), "0.0.0.0/0 下回环仍拒")
	require.True(t, isBlockedProbeIP(net.ParseIP("169.254.169.254")), "0.0.0.0/0 下元数据仍拒")
	require.False(t, isBlockedProbeIP(net.ParseIP("10.0.0.9")), "0.0.0.0/0 下合法私网可放行")
}
