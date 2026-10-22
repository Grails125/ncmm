package selfupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// RuntimeManaged 仅允许由启动器管理的版本文件进行容器内更新。
func RuntimeManaged(executable string) bool {
	if os.Getenv("NCMM_RUNTIME_MANAGED") != "1" {
		return false
	}
	root := os.Getenv("NCMM_RUNTIME_DIR")
	if root == "" {
		root = "/data/runtime"
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(executable) {
		return false
	}
	root = filepath.Clean(root)
	executable = filepath.Clean(executable)
	rel, err := filepath.Rel(root, executable)
	if err != nil {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 || parts[0] != "versions" || parts[1] == "" || parts[1] == "." || parts[1] == ".." || parts[2] != "ncmm" {
		return false
	}
	// 拒绝运行目录中的符号链接，避免标记或版本路径指向管理目录之外。
	for _, path := range []string{root, filepath.Join(root, "versions"), filepath.Dir(executable)} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	for _, path := range []string{executable, filepath.Join(root, "manager.json")} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return false
		}
	}
	marker := filepath.Join(root, "manager.json")
	info, err := os.Stat(marker)
	if err != nil || info.Size() > 1024 {
		return false
	}
	data, err := os.ReadFile(marker)
	var managed struct {
		Format int `json:"format"`
	}
	return err == nil && json.Unmarshal(data, &managed) == nil && managed.Format == 1
}
