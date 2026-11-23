package storage

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// 探测（Probe）的目标地址限制。
//
// 【为何 Worker 还要拦一道】CP 侧的 TestCandidate / TestSaved 在 CP 进程内校验过 endpoint，
// 但「已保存的存储」走另一条路：CP 的 TestConnection 只把 DB 里的 spec 原样下发，
// 由 Worker 发真实请求。而 spec 是历史数据——可能在门禁上线前就已入库，
// 也可能从别的写入路径进来。Worker 通常部署在内网，探测能力比 CP 更“值钱”
// （能摸到内网任意 HTTP/SFTP 服务），所以这条通道必须自己拦。
//
// 【只拦 Probe，不拦正式上传/下载】这是刻意的边界：内网自建 MinIO / NAS（如 10.x:9000）
// 是完全合法的备份后端，日常备份必须能连。被滥用的是「让 Worker 去连一个任意地址并告诉我
// 连不连得上」——那才是 SSRF 的信息回显面。故 Upload/Download/Delete 不做目标限制。
var errProbeTargetBlocked = errors.New("探测目标不在允许的地址范围内（禁止内网/回环/链路本地/云元数据）")

// blockedProbeNets 需拒绝的网段（与 CP 侧 backup_storage_ssrf.go 同一套枚举，故意重复）。
//
// 为何不共享：Worker 与 CP 是两个独立二进制、独立依赖边界，
// 为了一个 CIDR 列表把 service 包拉进 worker 的依赖里不划算；
// 两边各持一份、各自有测试盯着，反而是更稳的约束。
var blockedProbeNets = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16",
		"198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4",
		"::1/128", "fc00::/7", "fe80::/10", "ff00::/8",
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// probeAllowLoopback 仅供测试：httptest 服务监听 127.0.0.1。生产永不开。
var probeAllowLoopback bool

// SetProbeAllowLoopbackForTest 仅供测试开启回环例外（与 CP 侧同名函数同语义）。
func SetProbeAllowLoopbackForTest(allow bool) { probeAllowLoopback = allow }

// probeAllowPrivate 是部署级「探测允许的私网段」。
//
// 【为何需要它】之前一刀切拒全部私网，会把内网自建 MinIO/NAS（10.x:9000、192.168.x）
// 的探测也拒掉——但正式上传/下载却允许连它们。同一个存储后端「能备份、不能测连接」
// 是自相矛盾的行为，会让运维无法自检备份链路。
//
// 【为何不用粗开关】仓库已有约定（httpclient.GuardOptions.AllowPrivate）：
// 放行粒度是**精确 CIDR**而不是“允许内网”这种布尔——后者一次放行会把整片私网
// （含同机的数据库、内部面板）都暴露给探测。故这里同样用 CIDR 列表。
//
// 【怎么配】环境变量 JM_STORAGE_PROBE_ALLOW_PRIVATE，逗号分隔的 CIDR：
//
//	JM_STORAGE_PROBE_ALLOW_PRIVATE=10.0.0.0/8,192.168.0.0/16
//
// 不配 = 拒全部私网（默认从严）。云元数据地址段（169.254.169.254 等）
// **永远拒绝**，即使写在这个列表里也不放行。
const probeAllowPrivateEnv = "JM_STORAGE_PROBE_ALLOW_PRIVATE"

// metadataProbePrefixes 云元数据地址：无论 AllowPrivate 怎么配都永远拒。
//
// 单独列出来是因为它们常落在「为自托管而放行的私网段」里
// （169.254.169.254 属链路本地），而元数据端点泄露的是主机身份凭据，
// 代价远高于其它内网服务。
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
// 变成可探测——否则配 0.0.0.0/0 就等于关掉整道门禁，配 127.0.0.0/8 就能扫 CP/Worker 本机端口。
// 这些特殊地址在任何配置下都不可放行，只能靠测试开关（仅测试）临时开回环。
func isAllowedProbePrivate(ip net.IP) bool {
	if isMetadataProbeIP(ip) {
		return false
	}
	// 特殊地址永不可通过配置放行。
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	// 仅允许落在“合法私网”范围内的地址：RFC1918 + CGNAT。
	// 这样即使配了 0.0.0.0/0，公网与特殊段也不会因此被“放行”
	//（公网本来就不在拒绝列表里，无需放行）。
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

// rfc1918AndCgnat 是“合法私网”的允许范围。
//
// 只含 RFC1918（10/8、172.16/12、192.168/16）与 CGNAT（100.64/10，部分云内网用它）。
// 不含回环/链路本地/组播/保留——那些即使被配置 CIDR 覆盖也不得探测。
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

// isBlockedProbeIP 判断 IP 是否落在禁止探测的网段。
//
// 判定顺序很重要：
//  1. 元数据地址 **永拒**（优先于一切放行）；
//  2. 部署显式放行的 CIDR → **放行**；
//  3. 其余按内置黑名单逐项判。
//
// 为何放行要放在第 2 步而不是只在黑名单命中时例外：链路本地（169.254/16）、
// 回环等段在下面有独立的 Is* 快速判断，若只在黑名单循环里给例外，
// 那些段永远走不到放行分支——部署想放行自己的内网段就放不开。
func isBlockedProbeIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	// 1) 云元数据地址：无论怎么配都拒。
	if isMetadataProbeIP(ip) {
		return true
	}
	// 2) 部署显式放行的网段。
	if isAllowedProbePrivate(ip) {
		return false
	}
	// 3) 回环测试例外（仅测试开关）。
	if probeAllowLoopback && ip.IsLoopback() {
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

// validateProbeTarget 校验 host[:port] 形式的目标是否允许探测。
//
// fail-closed：解析失败/无地址/任一地址落在禁用网段都拒——
// 多 A 记录下只要有一个内网地址就可能被选中，放行等于给攻击者留了混入机会。
func validateProbeTarget(hostport string) error {
	raw := strings.TrimSpace(hostport)
	if raw == "" {
		return fmt.Errorf("缺少目标地址")
	}
	bare := raw
	if h, _, err := net.SplitHostPort(raw); err == nil {
		bare = h
	}
	bare = strings.Trim(bare, "[]")
	if bare == "" {
		return fmt.Errorf("缺少目标地址")
	}
	// 字面量 IP 与其他形式（如 2130706433、0x7f000001）先尝试解析：
	// net.ParseIP 不认十进制/十六进制写法，但解析器可能认，故两条路都校验。
	if ip := net.ParseIP(bare); ip != nil {
		if isBlockedProbeIP(ip) {
			return fmt.Errorf("%w: %s", errProbeTargetBlocked, bare)
		}
		return nil
	}
	lower := strings.ToLower(bare)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") || strings.HasSuffix(lower, ".local") {
		return fmt.Errorf("%w: %s", errProbeTargetBlocked, bare)
	}
	addrs, err := net.LookupIP(bare)
	if err != nil {
		return fmt.Errorf("无法解析目标主机 %s: %w", bare, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("目标主机 %s 无可用地址", bare)
	}
	for _, ip := range addrs {
		if isBlockedProbeIP(ip) {
			return fmt.Errorf("%w: %s -> %s", errProbeTargetBlocked, bare, ip.String())
		}
	}
	return nil
}

// probeURLHost 从完整 URL 里取出 host[:port]（S3/WebDAV 的 endpoint 是 URL 形态）。
func probeURLHost(endpoint string) (string, error) {
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		return "", fmt.Errorf("缺少 endpoint")
	}
	if !strings.Contains(ep, "://") {
		ep = "http://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("缺少 host")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("不支持的协议 %q（仅允许 http/https）", u.Scheme)
	}
	return u.Host, nil
}

// probeDialControl 在真正建立连接前按实际拨号的 IP 再校验一次。
//
// 为何“解析阶段已校验”还要再校验：DNS rebinding——校验时域名解析到公网，
// 实际连接时解析到内网。连接前复核是唯一不受该手法影响的检查点。
func probeDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return fmt.Errorf("%w: %s", errProbeTargetBlocked, address)
	}
	if isBlockedProbeIP(ip) {
		return fmt.Errorf("%w: %s", errProbeTargetBlocked, address)
	}
	return nil
}

// probeHTTPClient 是**仅用于探测**的 HTTP 客户端：不跟随重定向 + 连接前复核目标 IP。
//
// 【为何 Proxy 必须为 nil】走代理时 DialContext 看到的是**代理的地址**，
// 真正目标 IP 由代理解析——probeDialControl 就失效了，准入变成“只校验代理合法”。
// 仓库已有的受保护出站（httpclient.Guard）对这种情况的约定是直接拒绝
// （ErrProxyUnsupported：无法固定已校验 IP），不静默走代理。
// 探测只验证连通性、不传业务数据，直接不走代理更简单；
// 而**正式上传/下载仍用 defaultHTTPClient（带 ProxyFromEnvironment）**，两者刻意分开。
var probeHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("存储探测不跟随重定向: %w", errProbeTargetBlocked)
	},
	Transport: &http.Transport{
		// Proxy: nil 是刻意的：见上方说明。绝不能写 ProxyFromEnvironment。
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout: 30 * time.Second,
			Control: probeDialControl,
		}).DialContext,
	},
}

// probeDialer 供 SFTP 探测复用（裸 TCP，同样需要连接前复核）。
var probeDialer = &net.Dialer{
	Timeout: 30 * time.Second,
	Control: probeDialControl,
}

// probeConfig 在保留原配置的前提下标记「本次是探测」，供 New 选择探测专用 client/dialer。
//
// 用 Config 上的一个未导出字段而不是新增 NewProbeBackend：
// 三个后端的构造逻辑（scheme/prefix/endpoint 规整）必须完全一致，
// 分成两套构造函数就会出现「探测能连、备份不能连」这类不一致。
func probeConfig(cfg Config) Config {
	cfg.probe = true
	return cfg
}

// validateProbeConfig 按后端类型校验探测目标。
//
// 三类 endpoint 形态不同：
//   - s3 / webdav：URL（可能不带 scheme，需补全后再取 host）；
//   - sftp：host[:port]，缺省端口 22。
//
// 未知/本地类型不在此拦——交给 New 报“不支持/无需远程传输”。
func validateProbeConfig(cfg Config) error {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case TypeS3, TypeWebDAV:
		host, err := probeURLHost(cfg.Endpoint)
		if err != nil {
			return err
		}
		return validateProbeTarget(host)
	case TypeSFTP:
		host := strings.TrimSpace(cfg.Endpoint)
		if host == "" {
			return fmt.Errorf("SFTP 缺少 endpoint")
		}
		if _, _, err := net.SplitHostPort(host); err != nil {
			// 没带端口：补 22 后再校验（与 newSFTPBackend 的规整一致）。
			host = net.JoinHostPort(strings.Trim(host, "[]"), "22")
		}
		return validateProbeTarget(host)
	default:
		return nil
	}
}
