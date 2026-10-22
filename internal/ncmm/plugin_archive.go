package ncmm

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/3899/ncmm/internal/filelock"
	"github.com/3899/ncmm/internal/plugininstall"
)

const pluginArchiveLimit = 256 << 20

type pluginArchiveEntry struct {
	name string
	data []byte
	mode os.FileMode
}

func readPluginArchive(data []byte, filename string) ([]pluginArchiveEntry, string, error) {
	var entries []pluginArchiveEntry
	seen := map[string]bool{}
	var total int64
	add := func(name string, mode os.FileMode, reader io.Reader) error {
		if strings.ContainsAny(name, "\\:") || strings.HasPrefix(name, "/") || mode&os.ModeSymlink != 0 {
			return fmt.Errorf("插件包包含不安全的文件路径")
		}
		for _, part := range strings.Split(name, "/") {
			if part == ".." {
				return fmt.Errorf("插件包包含越界路径")
			}
		}
		base := path.Base(name)
		if base == "." || base == ".." || base == "" {
			return fmt.Errorf("插件文件名无效")
		}
		key := strings.ToLower(base)
		if seen[key] {
			return fmt.Errorf("插件包包含重复文件名 %q", base)
		}
		seen[key] = true
		if len(seen) > 1024 {
			return fmt.Errorf("插件包文件数量超过限制")
		}
		content, err := io.ReadAll(io.LimitReader(reader, pluginArchiveLimit-total+1))
		if err != nil {
			return err
		}
		total += int64(len(content))
		if total > pluginArchiveLimit {
			return fmt.Errorf("插件包解压后超过 256 MB")
		}
		// 下载包自带的来源记录不能覆盖宿主记录。
		if key == "source.json" {
			return nil
		}
		entries = append(entries, pluginArchiveEntry{base, content, mode.Perm()})
		return nil
	}
	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, "", err
		}
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			r, err := f.Open()
			if err != nil {
				return nil, "", err
			}
			err = add(f.Name, f.Mode(), r)
			_ = r.Close()
			if err != nil {
				return nil, "", err
			}
		}
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		gr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, "", err
		}
		defer gr.Close()
		tr := tar.NewReader(gr)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, "", err
			}
			if h.Typeflag == tar.TypeDir {
				continue
			}
			if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
				return nil, "", fmt.Errorf("插件包仅支持普通文件")
			}
			if err = add(h.Name, os.FileMode(h.Mode), tr); err != nil {
				return nil, "", err
			}
		}
	default:
		if !isExecutableFile(filename) {
			return nil, "", fmt.Errorf("插件包格式不支持")
		}
		if err := add(filename, 0755, bytes.NewReader(data)); err != nil {
			return nil, "", err
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	executable := ""
	for _, entry := range entries {
		if strings.HasPrefix(entry.name, "ncmm-") || strings.HasPrefix(entry.name, "ncmm_") {
			if isExecutableFile(entry.name) {
				if executable != "" {
					return nil, "", fmt.Errorf("插件包包含多个执行入口")
				}
				executable = entry.name
			}
		}
	}
	if executable == "" {
		for _, entry := range entries {
			if isExecutableFile(entry.name) && (runtime.GOOS == "windows" || entry.mode&0111 != 0) {
				if executable != "" {
					return nil, "", fmt.Errorf("插件包包含多个执行入口")
				}
				executable = entry.name
			}
		}
	}
	if executable == "" {
		return nil, "", fmt.Errorf("插件包缺少执行入口")
	}
	return entries, parsePluginName(executable), nil
}

func (p *Plugin) InstallFromArchive(data []byte, filename, destDir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return p.installArchive(ctx, data, filename, destDir, "", nil)
}

func (p *Plugin) installArchive(ctx context.Context, data []byte, filename, destDir, expectedName string, source *plugininstall.Source) (string, error) {
	entries, name, err := readPluginArchive(data, filename)
	if err != nil {
		return "", err
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\:") {
		return "", fmt.Errorf("插件名称无效")
	}
	if expectedName != "" && !strings.EqualFold(name, expectedName) {
		return "", fmt.Errorf("升级包中的插件名称与当前插件不一致")
	}
	if destDir == "" {
		destDir = p.DefaultPluginDir()
	}
	destDir, err = filepath.Abs(destDir)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}
	lock, err := filelock.Acquire(ctx, filepath.Join(destDir, ".install.lock"))
	if err != nil {
		return "", err
	}
	defer lock.Close()
	stage, err := os.MkdirTemp(destDir, ".ncmm-install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	target := filepath.Join(destDir, "ncmm-"+name)
	for _, entry := range entries {
		mode := entry.mode
		if mode == 0 {
			mode = 0644
		}
		if isExecutableFile(entry.name) && parsePluginName(entry.name) == name {
			mode |= 0111
		}
		if err = os.WriteFile(filepath.Join(stage, entry.name), entry.data, mode); err != nil {
			return "", err
		}
	}
	if err = preservePluginFiles(target, stage); err != nil {
		return "", err
	}
	if source != nil {
		metadata, marshalErr := json.MarshalIndent(source, "", "  ")
		if marshalErr != nil {
			return "", marshalErr
		}
		if err = os.WriteFile(filepath.Join(stage, "source.json"), metadata, 0600); err != nil {
			return "", err
		}
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	_, err = os.Stat(target)
	if os.IsNotExist(err) {
		if err = os.Rename(stage, target); err != nil {
			return "", err
		}
		return name, nil
	}
	if err != nil {
		return "", err
	}
	backup := stage + "-previous"
	if err = os.Rename(target, backup); err != nil {
		return "", fmt.Errorf("无法替换插件目录，请先停止插件: %w", err)
	}
	if err = os.Rename(stage, target); err != nil {
		if restoreErr := os.Rename(backup, target); restoreErr != nil {
			return "", fmt.Errorf("插件替换失败且旧目录恢复失败: %v; %w", err, restoreErr)
		}
		return "", err
	}
	// 旧目录直到完整的新目录就位后才移除。
	if err = os.RemoveAll(backup); err != nil {
		return name, fmt.Errorf("插件已更新，但旧目录清理失败: %w", err)
	}
	return name, nil
}

func preservePluginFiles(oldDir, newDir string) error {
	if _, err := os.Lstat(oldDir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	var total int64
	count := 0
	return filepath.WalkDir(oldDir, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("插件目录含符号链接，拒绝自动替换")
		}
		rel, err := filepath.Rel(oldDir, current)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dest := filepath.Join(newDir, rel)
		if entry.IsDir() {
			return os.MkdirAll(dest, 0755)
		}
		if strings.EqualFold(rel, "source.json") {
			return nil
		}
		if !strings.EqualFold(rel, "config.yaml") {
			if _, err = os.Stat(dest); err == nil {
				return nil
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("插件本地数据含特殊文件，请手动处理后升级")
		}
		count++
		remaining := int64(pluginArchiveLimit) - total
		if info.Size() > remaining || count > 4096 {
			return fmt.Errorf("插件本地数据过大，请手动备份并升级")
		}
		file, err := os.Open(current)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(file, remaining+1))
		_ = file.Close()
		if err != nil {
			return err
		}
		total += int64(len(data))
		if total > pluginArchiveLimit {
			return fmt.Errorf("插件本地数据过大，请手动备份并升级")
		}
		if err := os.WriteFile(dest, data, info.Mode().Perm()); err != nil {
			return err
		}
		// 覆盖已解包配置时 WriteFile 不会收紧既有权限，必须恢复用户文件权限。
		return os.Chmod(dest, info.Mode().Perm())
	})
}
