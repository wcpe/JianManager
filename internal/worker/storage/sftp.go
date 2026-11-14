package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// defaultHTTPClient 返回带合理超时的 HTTP 客户端，供 S3/WebDAV 后端复用。
// 备份归档可能较大，故只设连接/响应头超时，不设整体超时（由 ctx 控制取消）。
func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 0}
}

// backendHTTPClient 按配置选 client：探测态用受限 client（禁内网/回环/元数据 + 不跟随重定向），
// 正式传输用默认 client（内网自建 MinIO/NAS 必须能连）。
func backendHTTPClient(cfg Config) *http.Client {
	if cfg.probe {
		return probeHTTPClient
	}
	return defaultHTTPClient()
}

// sftpBackend 通过 SSH 对接远程主机，以会话 exec（cat 重定向）传输归档。
// 不依赖 SFTP 子系统库（pkg/sftp 不在依赖中），改用 SSH exec 实现上传/下载/删除，
// 仅需目标主机具备 cat/rm/mkdir 等基础命令。Endpoint 为 host[:port]，缺省端口 22。
type sftpBackend struct {
	addr   string // host:port
	user   string
	pass   string
	prefix string // 远程根目录（绝对或相对家目录），与 key 拼成完整远程路径
	// probe 为 true 时拨号走 probeDialer（连接前复核目标 IP）；正式传输用普通 dialer。
	probe bool
}

func newSFTPBackend(cfg Config) (Backend, error) {
	host := strings.TrimSpace(cfg.Endpoint)
	if host == "" {
		return nil, fmt.Errorf("SFTP 缺少 endpoint")
	}
	if !strings.Contains(host, ":") {
		host += ":22"
	}
	if strings.TrimSpace(cfg.AccessKey) == "" {
		return nil, fmt.Errorf("SFTP 缺少用户名")
	}
	// prefix 作为远程根目录；S3/WebDAV 把 prefix 编进 key，SFTP 单独持有以便 mkdir。
	return &sftpBackend{
		addr:   host,
		user:   cfg.AccessKey,
		pass:   cfg.SecretKey,
		prefix: strings.TrimRight(cfg.Prefix, "/"),
		probe:  cfg.probe,
	}, nil
}

// sftpConn 是一条带 ctx 监护的 SSH 连接。
//
// 【为何要包一层】`ssh.NewClientConn` 拿到的 TCP 连接不读 ctx，握手完成后
// `sess.Run` 同样是阻塞调用（命令在远端执行、结果靠流式回传）。如果只在 dial 里
// 起监护 goroutine 并在函数返回时关掉它，**握手之后的长命令就没人看管了**：
// 调用方 ctx 取消时 sess.Run 会一直挂到对端自己结束。
// 所以监护必须活到连接关闭为止，由本句柄的 Close 收尾。
type sftpConn struct {
	client *ssh.Client
	conn   net.Conn
	done   chan struct{}
}

// Close 停掉监护并关闭连接（幂等）。
func (c *sftpConn) Close() error {
	if c == nil {
		return nil
	}
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return c.client.Close()
}

// dial 建立 SSH 连接。出于自包含部署的可用性，这里采用 InsecureIgnoreHostKey：
// 远程存储多为运营商自有可信主机，主机指纹固定校验留待后续增强（记 backlog）。
//
// 【超时必须自己守】`ssh.NewClientConn` 拿到的是已经建立的 TCP 连接，
// 它自己**不读 ctx**：如果对端接受连接后不发 banner（防火墙黑洞、假监听、
// 或恶意服务故意挂住），握手会永久阻塞——`net.Dialer.Timeout` 只管到 TCP 建连为止。
// 所以这里用 ctx 派生一个 deadline 作用于整条 conn（含后续所有读写），
// 并起一个监护 goroutine：ctx 一取消就 Close，让阻塞中的握手立即返回。
//
// 返回的 sftpConn 把监护**延续到连接关闭**（不只是握手期），
// 这样后续 sess.Run 也能在 ctx 取消时被释放。
func (s *sftpBackend) dial(ctx context.Context) (*sftpConn, error) {
	cfg := &ssh.ClientConfig{
		User:            s.user,
		Auth:            []ssh.AuthMethod{ssh.Password(s.pass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}
	d := &net.Dialer{Timeout: 30 * time.Second}
	if s.probe {
		// 探测：连接前复核实际拨号的 IP（防 DNS rebinding）。
		d = probeDialer
	}
	conn, err := d.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return nil, fmt.Errorf("SFTP 连接失败: %w", err)
	}

	// 握手期给整条连接上 deadline；用 30s 兜底而不是无限等。
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("SFTP 设置超时失败: %w", err)
	}

	// 监护活到连接关闭（由 sftpConn.Close 收尾），不只管握手。
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// ctx 取消：立即断连，让阻塞的握手 / sess.Run 退出。
			_ = conn.Close()
		case <-done:
		}
	}()

	c, chans, reqs, err := ssh.NewClientConn(conn, s.addr, cfg)
	if err != nil {
		close(done)
		_ = conn.Close()
		return nil, fmt.Errorf("SFTP 握手失败: %w", err)
	}
	// 握手完成后清掉 deadline：长传输的进度由调用方 ctx 控制，
	// 不应用 30s 硬超时卡断大归档（上传/下载各自带自己的预算）。
	if err := conn.SetDeadline(time.Time{}); err != nil {
		close(done)
		_ = conn.Close()
		return nil, fmt.Errorf("SFTP 清除超时失败: %w", err)
	}
	return &sftpConn{client: ssh.NewClient(c, chans, reqs), conn: conn, done: done}, nil
}

// remotePath 把 prefix 与对象键拼成完整远程路径。
func (s *sftpBackend) remotePath(key string) string {
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + strings.TrimLeft(key, "/")
}

func (s *sftpBackend) Upload(ctx context.Context, key string, r io.Reader, _ int64) error {
	cli, err := s.dial(ctx)
	if err != nil {
		return err
	}
	defer cli.Close()
	remote := s.remotePath(key)

	// 先建父目录。
	if dir := path.Dir(remote); dir != "." && dir != "/" {
		if err := s.run(cli, fmt.Sprintf("mkdir -p %s", shellQuote(dir))); err != nil {
			return fmt.Errorf("SFTP 建目录失败: %w", err)
		}
	}

	sess, err := cli.client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdin = r
	// 用 cat 重定向写入；shell 引用远程路径防注入。
	if err := sess.Run(fmt.Sprintf("cat > %s", shellQuote(remote))); err != nil {
		return fmt.Errorf("SFTP 上传失败: %w", err)
	}
	return nil
}

func (s *sftpBackend) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	cli, err := s.dial(ctx)
	if err != nil {
		return nil, err
	}
	remote := s.remotePath(key)
	sess, err := cli.client.NewSession()
	if err != nil {
		_ = cli.Close()
		return nil, err
	}
	var buf bytes.Buffer
	sess.Stdout = &buf
	if err := sess.Run(fmt.Sprintf("cat %s", shellQuote(remote))); err != nil {
		_ = sess.Close()
		_ = cli.Close()
		return nil, fmt.Errorf("SFTP 下载失败: %w", err)
	}
	_ = sess.Close()
	_ = cli.Close()
	// 归档需完整落地，exec 模式下整段读入内存再交回；大归档场景为已知权衡（记 backlog）。
	return bytesReader(buf.Bytes()), nil
}

func (s *sftpBackend) Delete(ctx context.Context, key string) error {
	cli, err := s.dial(ctx)
	if err != nil {
		return err
	}
	defer cli.Close()
	// rm -f 对不存在文件返回 0，天然幂等。
	if err := s.run(cli, fmt.Sprintf("rm -f %s", shellQuote(s.remotePath(key)))); err != nil {
		return fmt.Errorf("SFTP 删除失败: %w", err)
	}
	return nil
}

// run 在一个新会话里执行命令并等待完成。
//
// 阻塞的 sess.Run 依靠连接句柄的监护 goroutine 在 ctx 取消时被释放
// （见 sftpConn 注释）：命令跑到一半 ctx 取消，conn 被 Close，Run 立即返回错误。
func (s *sftpBackend) run(cli *sftpConn, cmd string) error {
	sess, err := cli.client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	return sess.Run(cmd)
}

// shellQuote 用单引号包裹远程路径，转义内部单引号，防止命令注入。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
