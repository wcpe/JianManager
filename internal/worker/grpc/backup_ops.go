package grpc

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/wxys233/JianManager/internal/worker/storage"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// backupsSubdir 是节点数据根下存放备份归档的子目录（相对数据根）。
// 备份归档与制品库（var/artifacts）平级，整类便于浏览/迁移/归档。
const backupsSubdir = "var/backups"

// CreateBackup 将实例工作目录打包为 tar.gz 落到节点数据根 var/backups/<instanceID>/。
// 全量备份打包全部常规文件；增量备份据 base_manifest 仅打包新增或变化（size/mtime 不同）的文件。
// 无论增量与否都返回「本次备份后工作目录的完整清单」，供 Control Plane 持久化，
// 作为下一次增量的基准与链式恢复的依据（增量链由 CP 维护，Worker 无状态）。
// 若 storage 非空且 type!=local，则打包后把归档上传到该远程后端。
func (s *Server) CreateBackup(ctx context.Context, req *workerpb.CreateBackupRequest) (*workerpb.CreateBackupResponse, error) {
	inst, ok := s.manager.GetInstance(req.InstanceUuid)
	if !ok {
		return &workerpb.CreateBackupResponse{Success: false, Error: fmt.Sprintf("实例 %s 未注册", req.InstanceUuid)}, nil
	}
	workDir := strings.TrimSpace(inst.WorkDir)
	if workDir == "" {
		return &workerpb.CreateBackupResponse{Success: false, Error: "实例工作目录为空"}, nil
	}
	if s.root == nil {
		return &workerpb.CreateBackupResponse{Success: false, Error: "节点未配置数据根"}, nil
	}

	// 增量基准：path -> 指纹。增量时跳过 size 与 mtime 均未变的文件。
	base := map[string]*workerpb.BackupManifestEntry{}
	if req.Incremental {
		for _, e := range req.BaseManifest {
			base[e.Path] = e
		}
	}

	relPath := backupRelPath(req.InstanceUuid, req.BackupUuid)
	absPath := s.root.Abs(relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return &workerpb.CreateBackupResponse{Success: false, Error: fmt.Sprintf("创建备份目录失败: %v", err)}, nil
	}

	manifest, packed, size, err := writeBackupArchive(absPath, workDir, base, req.Incremental)
	if err != nil {
		_ = os.Remove(absPath)
		return &workerpb.CreateBackupResponse{Success: false, Error: err.Error()}, nil
	}

	resp := &workerpb.CreateBackupResponse{
		Success:   true,
		RelPath:   relPath,
		SizeBytes: size,
		FileCount: packed,
		Manifest:  manifest,
	}

	// 远程存储：打包完成后上传归档（FR-057）。本地备份（storage 空或 local）不上传。
	if spec := req.Storage; spec != nil && spec.Type != "" && spec.Type != storage.TypeLocal {
		backend, berr := storage.New(specToConfig(spec))
		if berr != nil {
			_ = os.Remove(absPath)
			return &workerpb.CreateBackupResponse{Success: false, Error: fmt.Sprintf("初始化远程存储失败: %v", berr)}, nil
		}
		key := storage.ObjectKey(spec.Prefix, req.InstanceUuid, req.BackupUuid)
		if uerr := backupUpload(ctx, backend, absPath, key); uerr != nil {
			_ = os.Remove(absPath)
			return &workerpb.CreateBackupResponse{Success: false, Error: fmt.Sprintf("上传远程存储失败: %v", uerr)}, nil
		}
		resp.StorageKey = key
	}

	return resp, nil
}

// RestoreBackup 按 rel_paths 顺序回放归档到实例工作目录（全量基在前，增量依次覆盖）。
// 远程归档（storage 非空且 type!=local）先据 storage_keys 拉回本地再回放。
func (s *Server) RestoreBackup(ctx context.Context, req *workerpb.RestoreBackupRequest) (*workerpb.RestoreBackupResponse, error) {
	inst, ok := s.manager.GetInstance(req.InstanceUuid)
	if !ok {
		return &workerpb.RestoreBackupResponse{Success: false, Error: fmt.Sprintf("实例 %s 未注册", req.InstanceUuid)}, nil
	}
	workDir := strings.TrimSpace(inst.WorkDir)
	if workDir == "" {
		return &workerpb.RestoreBackupResponse{Success: false, Error: "实例工作目录为空"}, nil
	}
	if s.root == nil {
		return &workerpb.RestoreBackupResponse{Success: false, Error: "节点未配置数据根"}, nil
	}
	if len(req.RelPaths) == 0 {
		return &workerpb.RestoreBackupResponse{Success: false, Error: "备份链为空"}, nil
	}

	var backend storage.Backend
	if spec := req.Storage; spec != nil && spec.Type != "" && spec.Type != storage.TypeLocal {
		b, berr := storage.New(specToConfig(spec))
		if berr != nil {
			return &workerpb.RestoreBackupResponse{Success: false, Error: fmt.Sprintf("初始化远程存储失败: %v", berr)}, nil
		}
		backend = b
	}

	var restored int64
	for i, rel := range req.RelPaths {
		abs := s.root.Abs(rel)
		// 远程归档：本地缺失则按对应 key 拉回。
		if backend != nil {
			if _, statErr := os.Stat(abs); statErr != nil {
				key := ""
				if i < len(req.StorageKeys) {
					key = req.StorageKeys[i]
				}
				if key == "" {
					return &workerpb.RestoreBackupResponse{Success: false, Error: fmt.Sprintf("缺少远程对象键: %s", rel)}, nil
				}
				if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
					return &workerpb.RestoreBackupResponse{Success: false, Error: fmt.Sprintf("创建本地缓存目录失败: %v", err)}, nil
				}
				if derr := backupDownload(ctx, backend, key, abs); derr != nil {
					return &workerpb.RestoreBackupResponse{Success: false, Error: fmt.Sprintf("拉取远程归档失败: %v", derr)}, nil
				}
			}
		}
		n, err := extractBackupArchive(abs, workDir)
		if err != nil {
			return &workerpb.RestoreBackupResponse{Success: false, Error: err.Error()}, nil
		}
		restored += n
	}

	return &workerpb.RestoreBackupResponse{Success: true, RestoredFiles: restored}, nil
}

// uploadFile 把本地归档以 key 上传到远程后端（流式，不全量载入内存）。
func backupUpload(ctx context.Context, b storage.Backend, absPath, key string) error {
	f, err := os.Open(absPath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	return b.Upload(ctx, key, f, info.Size())
}

// downloadFile 把远程 key 拉取并写入本地 absPath（流式）。
func backupDownload(ctx context.Context, b storage.Backend, key, absPath string) error {
	rc, err := b.Download(ctx, key)
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(absPath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

// specToConfig 把 proto 传输参数转换为 storage 包配置（凭证已由 CP 解析）。
func specToConfig(spec *workerpb.StorageBackendSpec) storage.Config {
	return storage.Config{
		Type:      spec.Type,
		Endpoint:  spec.Endpoint,
		Bucket:    spec.Bucket,
		Region:    spec.Region,
		Prefix:    spec.Prefix,
		AccessKey: spec.AccessKey,
		SecretKey: spec.SecretKey,
		UseSSL:    spec.UseSsl,
	}
}

// backupRelPath 计算备份归档相对数据根的路径：var/backups/<instanceUUID>/<backupUUID>.tar.gz。
// 以实例 UUID 分目录，便于整实例备份浏览与清理；以「/」分隔保证便携登记。
func backupRelPath(instanceUUID, backupUUID string) string {
	return backupsSubdir + "/" + instanceUUID + "/" + backupUUID + ".tar.gz"
}

// writeBackupArchive 遍历 workDir 写出 tar.gz 归档。
// base 非空（增量）时仅打包新增或变化的常规文件；始终返回工作目录的完整清单。
// 返回：完整清单、本次实际打包文件数、归档字节数。
func writeBackupArchive(absArchive, workDir string, base map[string]*workerpb.BackupManifestEntry, incremental bool) ([]*workerpb.BackupManifestEntry, int64, int64, error) {
	out, err := os.Create(absArchive)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("创建归档失败: %w", err)
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)

	manifest := []*workerpb.BackupManifestEntry{}
	var packed int64

	walkErr := filepath.Walk(workDir, func(p string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(workDir, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		// 仅备份常规文件；目录由 tar 中文件路径隐式重建，符号链接/设备等跳过。
		if !info.Mode().IsRegular() {
			return nil
		}
		entry := &workerpb.BackupManifestEntry{Path: relSlash, Size: info.Size(), ModTime: info.ModTime().Unix()}
		manifest = append(manifest, entry)

		if incremental {
			if prev, ok := base[relSlash]; ok && prev.Size == entry.Size && prev.ModTime == entry.ModTime {
				return nil // 未变化，增量跳过
			}
		}

		hdr, herr := tar.FileInfoHeader(info, "")
		if herr != nil {
			return herr
		}
		hdr.Name = relSlash
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, ferr := os.Open(p)
		if ferr != nil {
			return ferr
		}
		_, cerr := io.Copy(tw, f)
		_ = f.Close()
		if cerr != nil {
			return cerr
		}
		packed++
		return nil
	})

	// 关闭顺序：tar -> gzip -> file，任一失败都视为归档失败。
	tarErr := tw.Close()
	gzErr := gz.Close()
	closeErr := out.Close()
	if walkErr != nil {
		return nil, 0, 0, fmt.Errorf("打包工作目录失败: %w", walkErr)
	}
	if tarErr != nil {
		return nil, 0, 0, fmt.Errorf("关闭 tar 失败: %w", tarErr)
	}
	if gzErr != nil {
		return nil, 0, 0, fmt.Errorf("关闭 gzip 失败: %w", gzErr)
	}
	if closeErr != nil {
		return nil, 0, 0, fmt.Errorf("写入归档失败: %w", closeErr)
	}

	st, err := os.Stat(absArchive)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("读取归档大小失败: %w", err)
	}
	return manifest, packed, st.Size(), nil
}

// extractBackupArchive 解压 tar.gz 归档到 workDir（覆盖同名文件），返回写入文件数。
// 防御 zip-slip：拒绝解出到 workDir 之外的条目。
func extractBackupArchive(absArchive, workDir string) (int64, error) {
	in, err := os.Open(absArchive)
	if err != nil {
		return 0, fmt.Errorf("打开归档失败: %w", err)
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return 0, fmt.Errorf("解压 gzip 失败: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return 0, err
	}

	var n int64
	for {
		hdr, herr := tr.Next()
		if herr == io.EOF {
			break
		}
		if herr != nil {
			return n, fmt.Errorf("读取归档条目失败: %w", herr)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		target := filepath.Join(absWork, filepath.FromSlash(hdr.Name))
		if target != absWork && !strings.HasPrefix(target, absWork+string(filepath.Separator)) {
			return n, fmt.Errorf("归档条目越界: %s", hdr.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return n, err
		}
		mode := os.FileMode(hdr.Mode)
		if mode == 0 {
			mode = 0o644
		}
		f, ferr := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if ferr != nil {
			return n, ferr
		}
		_, cerr := io.Copy(f, tr)
		_ = f.Close()
		if cerr != nil {
			return n, cerr
		}
		n++
	}
	return n, nil
}
