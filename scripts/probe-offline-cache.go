package main

import (
	"archive/zip"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type dependency struct {
	repo     string
	group    string
	artifact string
	version  string
}

type generatorOptions struct {
	probeJar    string
	outputZip   string
	outputInfo  string
	repoCentral string
	repoTaboo   string
	skipSHA1    bool
}

type cacheInfo struct {
	TabooLibVersion   string   `json:"taboolibVersion"`
	KotlinVersion     string   `json:"kotlinVersion"`
	CoroutinesVersion string   `json:"coroutinesVersion"`
	Dependencies      []string `json:"dependencies"`
	JarCount          int      `json:"jarCount"`
	SHA1Count         int      `json:"sha1Count"`
	LibrariesBytes    int64    `json:"librariesBytes"`
	LibrariesSHA256   string   `json:"librariesSha256"`
	LibrariesShortSHA string   `json:"librariesShortSha"`
}

const (
	defaultReflexVersion = "1.2.4"
	defaultASMVersion    = "9.8"
	defaultJarRelocator  = "1.7"
)

func main() {
	opt := parseOptions()
	if strings.TrimSpace(opt.probeJar) == "" || strings.TrimSpace(opt.outputZip) == "" {
		fatal(errors.New("必须提供 --probe-jar 与 --output-zip"))
	}
	if err := generateProbeOfflineCache(opt); err != nil {
		fatal(err)
	}
}

func parseOptions() generatorOptions {
	probeJar := flag.String("probe-jar", "", "ServerProbe jar 路径")
	outputZip := flag.String("output-zip", "", "输出 probe-libraries.zip 路径")
	outputInfo := flag.String("output-info", "", "输出 probe.json 元信息路径（可选）")
	repoCentral := flag.String("repo-central", "", "覆盖 env.properties 中的 Maven Central 地址（可选）")
	repoTaboo := flag.String("repo-taboolib", "", "覆盖 env.properties 中的 TabooLib 仓库地址（可选）")
	skipSHA1 := flag.Bool("skip-sha1", false, "跳过远程 .sha1 校验，仅写本地计算 sha1")
	legacyJar := flag.String("jar", "", "兼容旧参数：同 --probe-jar")
	legacyOut := flag.String("out", "", "兼容旧参数：同 --output-zip")
	flag.Parse()
	if strings.TrimSpace(*probeJar) == "" {
		*probeJar = *legacyJar
	}
	if strings.TrimSpace(*outputZip) == "" {
		*outputZip = *legacyOut
	}
	return generatorOptions{
		probeJar:    strings.TrimSpace(*probeJar),
		outputZip:   strings.TrimSpace(*outputZip),
		outputInfo:  strings.TrimSpace(*outputInfo),
		repoCentral: strings.TrimSpace(*repoCentral),
		repoTaboo:   strings.TrimSpace(*repoTaboo),
		skipSHA1:    *skipSHA1,
	}
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func generateProbeOfflineCache(opt generatorOptions) error {
	runtimeProps, versionProps, err := readTabooLibProperties(opt.probeJar)
	if err != nil {
		return err
	}
	if strings.TrimSpace(runtimeProps["file-libs"]) != "" && strings.TrimSpace(runtimeProps["file-libs"]) != "libraries" {
		return fmt.Errorf("不支持的 file-libs: %s", runtimeProps["file-libs"])
	}
	deps, err := resolveDependencies(runtimeProps, versionProps, opt)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "serverprobe-libraries-*")
	if err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmp)
	root := filepath.Join(tmp, "libraries")
	client := &http.Client{Timeout: 2 * time.Minute}
	for _, dep := range deps {
		if err := downloadDependency(client, root, dep, !opt.skipSHA1); err != nil {
			return err
		}
	}
	if err := validateLibraries(root); err != nil {
		return err
	}
	if err := writeLibrariesZip(root, opt.outputZip); err != nil {
		return err
	}
	info, err := buildCacheInfo(root, opt.outputZip, deps, versionProps)
	if err != nil {
		return err
	}
	if strings.TrimSpace(opt.outputInfo) != "" {
		if err := writeCacheInfo(opt.outputInfo, info); err != nil {
			return err
		}
	}
	fmt.Printf("已生成 ServerProbe 离线依赖缓存: %s (jar=%d sha1=%d bytes=%d sha256=%s)\n", opt.outputZip, info.JarCount, info.SHA1Count, info.LibrariesBytes, info.LibrariesShortSHA)
	return nil
}

func readTabooLibProperties(jarPath string) (map[string]string, map[string]string, error) {
	zr, err := zip.OpenReader(jarPath)
	if err != nil {
		return nil, nil, fmt.Errorf("读取 ServerProbe jar 失败: %w", err)
	}
	defer zr.Close()
	runtimeProps, err := readZipProperties(&zr.Reader, "META-INF/taboolib/env.properties")
	if err != nil {
		return nil, nil, err
	}
	versionProps, err := readZipProperties(&zr.Reader, "META-INF/taboolib/version.properties")
	if err != nil {
		return nil, nil, err
	}
	return runtimeProps, versionProps, nil
}

func readZipProperties(zr *zip.Reader, name string) (map[string]string, error) {
	for _, file := range zr.File {
		if file.Name != name {
			continue
		}
		r, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("打开 %s 失败: %w", name, err)
		}
		defer r.Close()
		b, err := io.ReadAll(r)
		if err != nil {
			return nil, fmt.Errorf("读取 %s 失败: %w", name, err)
		}
		return parseProperties(string(b)), nil
	}
	return nil, fmt.Errorf("ServerProbe jar 缺少 %s", name)
}

func parseProperties(text string) map[string]string {
	props := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		props[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return props
}

func resolveDependencies(runtimeProps, versionProps map[string]string, opt generatorOptions) ([]dependency, error) {
	repoCentral := strings.TrimRight(firstNonEmpty(opt.repoCentral, required(runtimeProps, "repo-central")), "/")
	repoTabooLib := strings.TrimRight(firstNonEmpty(opt.repoTaboo, required(runtimeProps, "repo-taboolib")), "/")
	kotlinVersion := required(versionProps, "kotlin")
	coroutinesVersion := required(versionProps, "kotlin-coroutines")
	tabooLibVersion := required(versionProps, "taboolib")
	if repoCentral == "" || repoTabooLib == "" || kotlinVersion == "" || coroutinesVersion == "" || tabooLibVersion == "" {
		return nil, errors.New("TabooLib 配置缺少仓库或版本信息")
	}
	var deps []dependency
	add := func(repo, group, artifact, version string) {
		deps = append(deps, dependency{repo: repo, group: group, artifact: artifact, version: version})
	}
	add(repoCentral, "me.lucko", "jar-relocator", defaultJarRelocator)
	for _, artifact := range []string{"asm", "asm-util", "asm-commons", "asm-tree", "asm-analysis"} {
		add(repoCentral, "org.ow2.asm", artifact, defaultASMVersion)
	}
	add(repoCentral, "org.jetbrains.kotlin", "kotlin-stdlib", kotlinVersion)
	add(repoCentral, "org.jetbrains.kotlin", "kotlin-stdlib-jdk8", kotlinVersion)
	add(repoCentral, "org.jetbrains.kotlinx", "kotlinx-coroutines-core-jvm", coroutinesVersion)
	for _, artifact := range []string{"common-env", "common-util", "common-legacy-api", "common-platform-api"} {
		add(repoTabooLib, "io.izzel.taboolib", artifact, tabooLibVersion)
	}
	for _, module := range splitCSV(runtimeProps["module"]) {
		add(repoTabooLib, "io.izzel.taboolib", module, tabooLibVersion)
	}
	add(repoTabooLib, "org.tabooproject.reflex", "reflex", defaultReflexVersion)
	add(repoTabooLib, "org.tabooproject.reflex", "analyser", defaultReflexVersion)
	return dedupeDependencies(deps), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func required(props map[string]string, key string) string {
	return strings.TrimSpace(props[key])
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func dedupeDependencies(deps []dependency) []dependency {
	seen := map[string]dependency{}
	keys := make([]string, 0, len(deps))
	for _, dep := range deps {
		key := dep.id()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = dep
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]dependency, 0, len(keys))
	for _, key := range keys {
		out = append(out, seen[key])
	}
	return out
}

func (d dependency) id() string {
	return d.group + ":" + d.artifact + ":" + d.version
}

func downloadDependency(client *http.Client, root string, dep dependency, verifyRemoteSHA1 bool) error {
	rel := artifactRelPath(dep)
	url := dep.repo + "/" + rel
	target := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("创建依赖目录失败: %w", err)
	}
	tmp := target + ".download"
	defer os.Remove(tmp)
	rawSHA1, err := downloadFileSHA1(client, url, tmp)
	if err != nil {
		return err
	}
	if verifyRemoteSHA1 {
		remoteSHA1, err := fetchRemoteSHA1(client, url+".sha1")
		if err == nil && remoteSHA1 != "" && remoteSHA1 != rawSHA1 {
			return fmt.Errorf("%s 远端 sha1 校验不符: want=%s got=%s", url, remoteSHA1, rawSHA1)
		}
		if err != nil {
			fmt.Printf("sha1 unavailable %s: %v，改用本地计算\n", dep.id(), err)
		}
	}
	if err := normalizeJar(tmp, target); err != nil {
		return fmt.Errorf("规范化依赖 jar 失败 %s: %w", dep.id(), err)
	}
	localSHA1, err := fileSHA1(target)
	if err != nil {
		return err
	}
	if err := os.WriteFile(target+".sha1", []byte(localSHA1), 0o644); err != nil {
		return fmt.Errorf("写入依赖 sha1 失败: %w", err)
	}
	fmt.Printf("cached %s\n", dep.id())
	return nil
}

func downloadFileSHA1(client *http.Client, url, target string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("下载 %s 失败: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 %s 返回 HTTP %d", url, resp.StatusCode)
	}
	file, err := os.Create(target)
	if err != nil {
		return "", fmt.Errorf("创建依赖文件失败: %w", err)
	}
	h := sha1.New()
	_, copyErr := io.Copy(io.MultiWriter(file, h), resp.Body)
	closeErr := file.Close()
	if copyErr != nil {
		return "", fmt.Errorf("写入依赖文件失败: %w", copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("关闭依赖文件失败: %w", closeErr)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fetchRemoteSHA1(client *http.Client, url string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return "", errors.New("sha1 响应为空")
	}
	return strings.ToLower(strings.TrimSpace(fields[0])), nil
}

func normalizeJar(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	files := make([]*zip.File, 0, len(zr.File))
	for _, file := range zr.File {
		if file.FileInfo().IsDir() || isJarSignatureFile(file.Name) {
			continue
		}
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	fixedTime := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, file := range files {
		header := &zip.FileHeader{Name: file.Name, Method: zip.Deflate}
		header.Modified = fixedTime
		header.SetMode(file.FileInfo().Mode().Perm())
		w, err := zw.CreateHeader(header)
		if err != nil {
			_ = out.Close()
			return err
		}
		r, err := file.Open()
		if err != nil {
			_ = out.Close()
			return err
		}
		_, copyErr := io.Copy(w, r)
		closeErr := r.Close()
		if copyErr != nil {
			_ = out.Close()
			return copyErr
		}
		if closeErr != nil {
			_ = out.Close()
			return closeErr
		}
	}
	if err := zw.Close(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func isJarSignatureFile(name string) bool {
	upper := strings.ToUpper(filepath.ToSlash(name))
	if !strings.HasPrefix(upper, "META-INF/") {
		return false
	}
	rel := strings.TrimPrefix(upper, "META-INF/")
	if strings.Contains(rel, "/") {
		return false
	}
	return strings.HasSuffix(rel, ".SF") || strings.HasSuffix(rel, ".DSA") || strings.HasSuffix(rel, ".RSA")
}

func fileSHA1(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开依赖文件失败: %w", err)
	}
	defer file.Close()
	h := sha1.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", fmt.Errorf("计算依赖 sha1 失败: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func artifactRelPath(dep dependency) string {
	groupPath := strings.ReplaceAll(dep.group, ".", "/")
	name := dep.artifact + "-" + dep.version + ".jar"
	return groupPath + "/" + dep.artifact + "/" + dep.version + "/" + name
}

func validateLibraries(root string) error {
	jarCount, shaCount, err := countLibraries(root)
	if err != nil {
		return err
	}
	if jarCount == 0 || jarCount != shaCount {
		return fmt.Errorf("依赖缓存不完整: jar=%d sha1=%d", jarCount, shaCount)
	}
	prefixes := []string{
		"io/izzel/taboolib/",
		"me/lucko/jar-relocator/",
		"org/jetbrains/kotlin/",
		"org/jetbrains/kotlinx/",
		"org/ow2/asm/",
		"org/tabooproject/reflex/",
	}
	for _, prefix := range prefixes {
		if !hasPrefixFile(root, prefix) {
			return fmt.Errorf("依赖缓存缺少前缀: libraries/%s", prefix)
		}
	}
	return nil
}

func countLibraries(root string) (int, int, error) {
	jarCount := 0
	shaCount := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch {
		case strings.HasSuffix(path, ".jar"):
			jarCount++
		case strings.HasSuffix(path, ".jar.sha1"):
			shaCount++
		}
		return nil
	})
	return jarCount, shaCount, err
}

func hasPrefixFile(root, prefix string) bool {
	found := false
	prefix = filepath.FromSlash(prefix)
	err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(prefix, "/"))), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		found = true
		return filepath.SkipAll
	})
	if err != nil {
		return false
	}
	return found
}

func writeLibrariesZip(root, outPath string) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}
	files := []string{}
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	}); err != nil {
		return fmt.Errorf("扫描依赖缓存失败: %w", err)
	}
	sort.Strings(files)
	out, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("创建输出 zip 失败: %w", err)
	}
	zw := zip.NewWriter(out)
	fixedTime := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, file := range files {
		rel, err := filepath.Rel(filepath.Dir(root), file)
		if err != nil {
			_ = out.Close()
			return fmt.Errorf("计算 zip 相对路径失败: %w", err)
		}
		rel = filepath.ToSlash(rel)
		info, err := os.Stat(file)
		if err != nil {
			_ = out.Close()
			return fmt.Errorf("读取依赖文件信息失败: %w", err)
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			_ = out.Close()
			return fmt.Errorf("创建 zip 头失败: %w", err)
		}
		header.Name = rel
		header.Method = zip.Deflate
		header.Modified = fixedTime
		w, err := zw.CreateHeader(header)
		if err != nil {
			_ = out.Close()
			return fmt.Errorf("创建 zip 条目失败: %w", err)
		}
		in, err := os.Open(file)
		if err != nil {
			_ = out.Close()
			return fmt.Errorf("打开依赖文件失败: %w", err)
		}
		_, copyErr := io.Copy(w, in)
		closeErr := in.Close()
		if copyErr != nil {
			_ = out.Close()
			return fmt.Errorf("写入 zip 条目失败: %w", copyErr)
		}
		if closeErr != nil {
			_ = out.Close()
			return fmt.Errorf("关闭依赖文件失败: %w", closeErr)
		}
	}
	if err := zw.Close(); err != nil {
		_ = out.Close()
		return fmt.Errorf("关闭输出 zip 失败: %w", err)
	}
	return out.Close()
}

func buildCacheInfo(root, zipPath string, deps []dependency, versionProps map[string]string) (cacheInfo, error) {
	jarCount, shaCount, err := countLibraries(root)
	if err != nil {
		return cacheInfo{}, err
	}
	stat, err := os.Stat(zipPath)
	if err != nil {
		return cacheInfo{}, fmt.Errorf("读取输出 zip 信息失败: %w", err)
	}
	sha256Hex, err := fileSHA256(zipPath)
	if err != nil {
		return cacheInfo{}, err
	}
	depIDs := make([]string, 0, len(deps))
	for _, dep := range deps {
		depIDs = append(depIDs, dep.id())
	}
	return cacheInfo{
		TabooLibVersion:   required(versionProps, "taboolib"),
		KotlinVersion:     required(versionProps, "kotlin"),
		CoroutinesVersion: required(versionProps, "kotlin-coroutines"),
		Dependencies:      depIDs,
		JarCount:          jarCount,
		SHA1Count:         shaCount,
		LibrariesBytes:    stat.Size(),
		LibrariesSHA256:   sha256Hex,
		LibrariesShortSHA: sha256Hex[:8],
	}, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开文件失败: %w", err)
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", fmt.Errorf("计算 sha256 失败: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeCacheInfo(path string, info cacheInfo) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建元信息目录失败: %w", err)
	}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化元信息失败: %w", err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("写入元信息失败: %w", err)
	}
	return nil
}
