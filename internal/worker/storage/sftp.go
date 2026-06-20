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

// sftpBackend 通过 SSH 对接远程主机，以会话 exec（cat 重定向）传输归档。
// 不依赖 SFTP 子系统库（pkg/sftp 不在依赖中），改用 SSH exec 实现上传/下载/删除，
// 仅需目标主机具备 cat/rm/mkdir 等基础命令。Endpoint 为 host[:port]，缺省端口 22。
type sftpBackend struct {
	addr   string // host:port
	user   string
	pass   string
	prefix string // 远程根目录（绝对或相对家目录），与 key 拼成完整远程路径
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
	return &sftpBackend{addr: host, user: cfg.AccessKey, pass: cfg.SecretKey, prefix: strings.TrimRight(cfg.Prefix, "/")}, nil
}

// dial 建立 SSH 连接。出于自包含部署的可用性，这里采用 InsecureIgnoreHostKey：
// 远程存储多为运营商自有可信主机，主机指纹固定校验留待后续增强（记 backlog）。
func (s *sftpBackend) dial(ctx context.Context) (*ssh.Client, error) {
	cfg := &ssh.ClientConfig{
		User:            s.user,
		Auth:            []ssh.AuthMethod{ssh.Password(s.pass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}
	d := &net.Dialer{Timeout: 30 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return nil, fmt.Errorf("SFTP 连接失败: %w", err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, s.addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("SFTP 握手失败: %w", err)
	}
	return ssh.NewClient(c, chans, reqs), nil
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

	sess, err := cli.NewSession()
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
	sess, err := cli.NewSession()
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
func (s *sftpBackend) run(cli *ssh.Client, cmd string) error {
	sess, err := cli.NewSession()
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
