//go:build windows

package selfupdate

import (
	"fmt"
	"os"
)

func installStaged(staged, path string) error {
	backup := path + ".old"
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("清理旧备份失败，原程序未变更: %w", err)
	}
	if err := os.Rename(path, backup); err != nil {
		return fmt.Errorf("备份原程序失败，原程序未变更: %w", err)
	}
	if err := os.Rename(staged, path); err != nil {
		if restoreErr := os.Rename(backup, path); restoreErr != nil {
			return fmt.Errorf("替换程序失败: %v；恢复失败: %v，请从 %s 恢复原程序", err, restoreErr, backup)
		}
		return fmt.Errorf("替换程序失败，已恢复原程序: %w", err)
	}
	return nil
}
