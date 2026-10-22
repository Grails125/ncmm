// Package selfupdate provides staged executable replacement and installation receipts.
package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func Matches(path, digest string) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return false
	}
	return hex.EncodeToString(hash.Sum(nil)) == digest
}

// Replace 的调用方须持有目标程序对应的跨进程锁。先落盘完整文件，再替换目录项。
func Replace(path string, data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("更新文件为空")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("更新目标不是普通文件")
	}
	staged, err := os.CreateTemp(filepath.Dir(path), ".ncmm-update-*")
	if err != nil {
		return err
	}
	name := staged.Name()
	defer os.Remove(name)
	if err := staged.Chmod(info.Mode().Perm()); err != nil {
		staged.Close()
		return err
	}
	if _, err := staged.Write(data); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Sync(); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	return installStaged(name, path)
}
