package service

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// errProbeBlockedNetwork 目标地址落在禁止探测的网段（内网/回环/链路本地/元数据）。
//
// 为什么必须在应用层拦：备份存储的「测试连接」接受调用方提供的任意 endpoint，
// 由**控制面主机**发起出站请求。若只靠网关或反向代理，任何直连 CP 的路径
// （内网部署、容器网络、本机端口转发）都能绕过——那就把 CP 变成了内网扫描器，
// 还能顺带摸到云元数据服务。
var errProbeBlockedNetwork = errors.New("目标地址不在允许的出站范围内（禁止内网/回环/链路本地/云元数据）")

// backupProbeHTTPClient 是备份存储探测专用的 HTTP 客户端。
//
// 与 http.DefaultClient 的三点差异，都是为了堵 SSRF：
//  1. 不跟随重定向——否则「先返回 302 到公网、再跳到 169.254.169.254」这类跳板
//     能绕过入口校验（校验发生在解析阶段，跟随重定向时不会再校验目标）。
//  2. 显式超时——DefaultClient 无超时，慢速内网服务可以把探测挂在半连接上。
//  3. 自定义 DialContext 挂 probeDialControl——在真正建立连接前按实际拨号的 IP 再校验一次，
//     堵 DNS rebinding（校验时域名解析到公网，连接时解析到内网）。
//     没有这一条时，入口的 validateProbeHost 只能拦住“校验那一刻”的解析结果。
//  4. **显式禁用代理**（Proxy: nil）。这是必须的，不是保守：
//     一旦走 HTTP 代理，DialContext 看到的目标地址是**代理的地址**，
//     而真正的目标 IP 由代理去解析——probeDialControl 就完全失效了，
//     校验变成“只校验了代理是否合法”。仓库已有的受保护出站（httpclient.Guard）
//     对这种情况的约定是**直接拒绝**（ErrProxyUnsupported：无法固定已校验 IP），
//     不静默走代理。探测场景比它更窄（只验证连通性、不传业务数据），
//     直接不走代理即可：若部署环境必须靠代理才能访问外部存储，
//     探测会报网络错误，而**正式上传/下载仍走 defaultHTTPClient（带 ProxyFromEnvironment）**
//     不受影响——两者刻意分开。
var backupProbeHTTPClient = &http.Client{
	Timeout: backupStorageProbeTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("备份存储探测不跟随重定向: %w", errProbeBlockedNetwork)
	},
	Transport: &http.Transport{
		// Proxy: nil 是刻意的：见上方第 4 条。绝不能写 ProxyFromEnvironment，
		// 否则 Control 只看到代理地址，目标 IP 准入形同虚设。
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout: backupStorageProbeTimeout,
			Control: probeDialControl,
		}).DialContext,
	},
}

// blockedProbeNets 显式枚举需拒绝的网段。
//
// 不依赖 IP.IsPrivate() 的原因：它只覆盖 RFC1918 与 ULA，漏掉
// 169.254.0.0/16（链路本地，含 AWS/GCP/Azure 的 169.254.169.254 元数据）、
// 100.64.0.0/10（CGNAT，部分云内网用它）、以及 IPv6 的 fe80::/10。
var blockedProbeNets = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8",      // 本网络
		"10.0.0.0/8",     // RFC1918
		"100.64.0.0/10",  // CGNAT（云内网常用）
		"127.0.0.0/8",    // 回环
		"169.254.0.0/16", // 链路本地（含云元数据 169.254.169.254）
		"172.16.0.0/12",  // RFC1918
		"192.0.0.0/24",   // IETF 协议保留
		"192.168.0.0/16", // RFC1918
		"198.18.0.0/15",  // 基准测试保留
		"224.0.0.0/4",    // 组播
		"240.0.0.0/4",    // 保留
		"::1/128",        // IPv6 回环
		"fc00::/7",       // IPv6 ULA
		"fe80::/10",      // IPv6 链路本地
		"ff00::/8",       // IPv6 组播
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// probePolicy 出站探测策略。默认全部拦截；仅测试可开启回环例外。
//
// 生产永远不应开启 AllowLoopback：开启后「测试连接」就能访问 CP 本机任意端口。
var probePolicy = struct{ AllowLoopback bool }{}

// SetProbeAllowLoopbackForTest 仅供测试开启回环例外。
// 生产调用它等于关掉一道 SSRF 门禁，因此不在业务代码路径中出现。
func SetProbeAllowLoopbackForTest(allow bool) {
	probePolicy.AllowLoopback = allow
}

// probeAllowPrivateEnv 是部署级「探测允许的私网段」环境变量（逗号分隔 CIDR）。
//
// 【为何需要它】之前一刀切拒全部私网，会把内网自建 MinIO/NAS 的探测也拒掉，
// 而正式上传/下载却允许连它们——同一个存储后端「能备份、不能测连接」是自相矛盾的。
//
// 【为何用 CIDR 而非布尔】与仓库已有约定（httpclient.GuardOptions.AllowPrivate）同构：
// 放行粒度是精确网段，避免一次放行把整片私网（含同机的数据库、内部面板）都暴露给探测。
//
// 不配 = 拒全部私网（默认从严）。云元数据地址永远拒（即使写在列表里）。
const probeAllowPrivateEnv = "JM_STORAGE_PROBE_ALLOW_PRIVATE"

// metadataProbePrefixes 云元数据地址：无论放行列表怎么配都永远拒。
var metadataProbePrefixes = func() []*net.IPNet {
	cidrs := []string{"169.254.169.254/32", "169.254.170.2/32", "100.100.100.200/32"}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// probeAllowPrivateNets 解析后的放行 CIDR（进程启动时读一次）。
var probeAllowPrivateNets = func() []*net.IPNet {
	raw := strings.TrimSpace(os.Getenv(probeAllowPrivateEnv))
	if raw == "" {
		return nil
	}
	out := make([]*net.IPNet, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		s := strings.TrimSpace(part)
		if s == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(s); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// SetProbeAllowPrivateForTest 仅供测试设定放行 CIDR（生产读环境变量）。
func SetProbeAllowPrivateForTest(cidrs []string) {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(strings.TrimSpace(c)); err == nil {
			out = append(out, n)
		}
	}
	probeAllowPrivateNets = out
}

// isMetadataProbeIP 判断是否云元数据地址（永拒）。
func isMetadataProbeIP(ip net.IP) bool {
	for _, n := range metadataProbePrefixes {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// isAllowedProbePrivate 判断该私网 IP 是否被部署显式放行（元数据地址一律不算）。
//
// 【只放行“合法私网”】环境变量里的 CIDR 不能把回环/链路本地/组播/未指定/保留段
// 变成可探测——否则配 0.0.0.0/0 就等于关掉整道门禁，配 127.0.0.0/8 就能扫 CP 本机端口。
// 这些特殊地址在任何配置下都不可放行，只能靠测试开关（仅测试）临时开回环。
func isAllowedProbePrivate(ip net.IP) bool {
	if isMetadataProbeIP(ip) {
		return false
	}
	// 特殊地址永不可通过配置放行。
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	// 仅允许落在“合法私网”范围内（RFC1918 + CGNAT）。
	if !isRfc1918OrCgnat(ip) {
		return false
	}
	for _, n := range probeAllowPrivateNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// rfc1918AndCgnat 是“合法私网”的允许范围（与 Worker 侧同一套）。
var rfc1918AndCgnat = func() []*net.IPNet {
	cidrs := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10"}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// isRfc1918OrCgnat 判断是否落在合法私网范围。
func isRfc1918OrCgnat(ip net.IP) bool {
	for _, n := range rfc1918AndCgnat {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// isBlockedProbeIP 判断某具体 IP 是否落在禁止探测的网段。
//
// 判定顺序：1) 元数据地址永拒 → 2) 部署显式放行 CIDR → 3) 内置黑名单。
// 放行必须在 Is* 快速判断之前，否则链路本地/回环等段永远走不到放行分支。
func isBlockedProbeIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// IPv4-mapped IPv6（::ffff:10.0.0.1）必须先归一化，否则 To4() 为 nil 时
	// 会漏过 v4 网段判断。
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	// 1) 云元数据地址：无论怎么配都拒。
	if isMetadataProbeIP(ip) {
		return true
	}
	// 2) 部署显式放行的网段（如内网自建 MinIO）。
	if isAllowedProbePrivate(ip) {
		return false
	}
	// 3) 回环测试例外（仅测试开关）。
	if probePolicy.AllowLoopback && ip.IsLoopback() {
		return false
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	for _, n := range blockedProbeNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// validateProbeHost 校验探测目标的主机名/IP 是否允许出站。
//
// 解析后逐 IP 校验并**fail-closed**：解析失败、无地址、任一地址落在禁用网段都拒绝。
// 为什么任一而非全部：DNS 轮询/多 A 记录下，只要有一个内网地址就可能被客户端选中，
// 「有一个能用就放行」等于给攻击者留下了把内网 IP 混进公网域名解析的机会。
func validateProbeHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("缺少 host")
	}
	// 去端口（可能形如 192.0.2.4:9000 或 [::1]:22）。
	bare := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		bare = h
	}
	bare = strings.Trim(bare, "[]")
	if bare == "" {
		return fmt.Errorf("缺少 host")
	}

	// 字面量 IP 与其他形式（如 2130706433、0x7f000001）统一先尝试解析：
	// net.ParseIP 不认这些十进制/十六进制写法，但 DNS 解析器可能认，故两条路都校验。
	if ip := net.ParseIP(bare); ip != nil {
		if isBlockedProbeIP(ip) {
			return fmt.Errorf("%w: %s", errProbeBlockedNetwork, bare)
		}
		return nil
	}

	// 本地主机名（localhost、*.local 等）直接拒，避免依赖解析器行为。
	lower := strings.ToLower(bare)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") || strings.HasSuffix(lower, ".local") {
		return fmt.Errorf("%w: %s", errProbeBlockedNetwork, bare)
	}

	addrs, err := net.LookupIP(bare)
	if err != nil {
		// 解析失败：明确报错而不是放行。探测本身要能连上才能通过，
		// 这里拒绝不会误伤正常配置（正常域名应可解析）。
		return fmt.Errorf("无法解析目标主机 %s: %w", bare, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("目标主机 %s 无可用地址", bare)
	}
	for _, ip := range addrs {
		if isBlockedProbeIP(ip) {
			return fmt.Errorf("%w: %s -> %s", errProbeBlockedNetwork, bare, ip.String())
		}
	}
	return nil
}

// isConnRefusedByPolicy 判断拨号错误是否由本地策略拒绝（用于把底层错误
// 统一映射成可读的业务错误，避免把 errno 细节暴露给调用方）。
func isConnRefusedByPolicy(err error) bool {
	return errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}

// probeDialControl 是 net.Dialer.Control 钩子：在真正建立连接前拿到目标 IP 再校验一次。
//
// 为何「解析阶段已校验」还要再校验：DNS rebinding——校验时域名解析到公网，
// 实际连接时解析到内网。连接前按实际拨号的 IP 复核是唯一不受该手法影响的检查点。
func probeDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return fmt.Errorf("%w: %s", errProbeBlockedNetwork, address)
	}
	if isBlockedProbeIP(ip) {
		return fmt.Errorf("%w: %s", errProbeBlockedNetwork, address)
	}
	return nil
}

// probeDialer 带目标 IP 复核的拨号器，供 SFTP 等裸 TCP 探测复用。
var probeDialer = &net.Dialer{
	Timeout: backupStorageProbeTimeout,
	Control: probeDialControl,
}

// 编译期确保 time 包被引用（超时常量在上方使用）。
var _ = time.Second

// validateProbeSpecTarget 按存储类型校验待探测的 spec 目标是否允许出站。
//
// 三种 endpoint 形态不同：
//   - s3 / webdav：URL（可能不带 scheme，需补全后再取 host）；
//   - sftp：host[:port]，缺省端口 22。
//
// 未知/本地类型不在此拦——交给 Worker 报“不支持/无需远程传输”。
func validateProbeSpecTarget(spec *workerpb.StorageBackendSpec) error {
	if spec == nil {
		return fmt.Errorf("缺少存储配置")
	}
	switch strings.ToLower(strings.TrimSpace(spec.GetType())) {
	case string(model.BackupStorageS3), string(model.BackupStorageWebDAV):
		if _, err := backupStorageProbeURL(spec.GetEndpoint(), spec.GetUseSsl()); err != nil {
			return err
		}
		// backupStorageProbeURL 内部已调 validateProbeHost（逐 IP fail-closed），不重复校验。
		return nil
	case string(model.BackupStorageSFTP):
		addr, err := sftpProbeAddr(spec.GetEndpoint())
		if err != nil {
			return err
		}
		return validateProbeHost(addr)
	default:
		return nil
	}
}
