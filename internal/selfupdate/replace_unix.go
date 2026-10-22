//go:build !windows

package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/3899/ncmm/internal/atomicfile"
)

func installStaged(staged, path string) error {
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := atomicfile.Write(path+".old", old, info.Mode().Perm()); err != nil {
		return fmt.Errorf("备份原程序失败: %w", err)
	}
	if err := os.Rename(staged, path); err != nil {
		return fmt.Errorf("替换程序失败，原程序仍保留: %w", err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
