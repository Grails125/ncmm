// Package managedruntime stages container updates and promotes only healthy versions.
package managedruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3899/ncmm/internal/atomicfile"
	"github.com/3899/ncmm/internal/filelock"
	"github.com/3899/ncmm/internal/selfupdate"
	"github.com/3899/ncmm/internal/upgradepolicy"
)

type Slot struct {
	Version string `json:"version"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
}

type State struct {
	Format         int       `json:"format"`
	Current        Slot      `json:"current"`
	Previous       Slot      `json:"previous,omitempty"`
	Pending        Slot      `json:"pending,omitempty"`
	ImageSHA256    string    `json:"imageSHA256"`
	RejectedSHA256 string    `json:"rejectedSHA256,omitempty"`
	LastError      string    `json:"lastError,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func Directory() string {
	if value := os.Getenv("NCMM_RUNTIME_DIR"); value != "" {
		return value
	}
	return "/data/runtime"
}

func checkRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return fmt.Errorf("运行目录必须是规范的绝对路径")
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("运行目录不是普通目录")
	}
	return nil
}

func slotPath(version, digest string) string {
	return "versions/" + strings.TrimPrefix(version, "v") + "-" + digest[:16] + "/ncmm"
}

func location(root string, slot Slot) (string, error) {
	if _, ok := upgradepolicy.CompareNative(slot.Version, slot.Version); !ok || len(slot.SHA256) != 64 {
		return "", fmt.Errorf("运行版本信息无效")
	}
	for _, r := range slot.SHA256 {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return "", fmt.Errorf("运行文件摘要无效")
		}
	}
	if slot.Path != slotPath(slot.Version, slot.SHA256) {
		return "", fmt.Errorf("运行文件路径无效")
	}
	return filepath.Join(root, filepath.FromSlash(slot.Path)), nil
}

func Valid(root string, slot Slot) bool {
	path, err := location(root, slot)
	if err != nil {
		return false
	}
	for _, dir := range []string{root, filepath.Join(root, "versions"), filepath.Dir(path)} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0 && info.Size() <= 256<<20 && selfupdate.Matches(path, slot.SHA256)
}

func Read(root string) (State, error) {
	var state State
	path := filepath.Join(root, "state.json")
	info, err := os.Lstat(path)
	if err != nil {
		return state, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return state, fmt.Errorf("运行状态文件无效")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.Format != 1 {
		return state, fmt.Errorf("未知的运行状态格式")
	}
	for _, slot := range []Slot{state.Current, state.Previous, state.Pending} {
		if slot.Path != "" {
			if _, err := location(root, slot); err != nil {
				return state, err
			}
		}
	}
	return state, nil
}

func locked(root string, change func(*State) error, recoverState bool) error {
	if err := checkRoot(root); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := filelock.Acquire(ctx, filepath.Join(root, "state.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	state, err := Read(root)
	if err != nil {
		if !os.IsNotExist(err) && !recoverState {
			return err
		}
		state = State{Format: 1}
		if !os.IsNotExist(err) {
			state.LastError = "运行状态损坏，已从镜像恢复"
		}
	}
	if err := change(&state); err != nil {
		return err
	}
	state.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(root, "state.json"), data, 0600)
}

func writeSlot(root, version string, data []byte) (Slot, error) {
	if len(data) == 0 || len(data) > 256<<20 {
		return Slot{}, fmt.Errorf("运行文件为空或过大")
	}
	if _, ok := upgradepolicy.CompareNative(version, version); !ok {
		return Slot{}, fmt.Errorf("只接受本仓库原生版本")
	}
	slot := Slot{Version: strings.TrimPrefix(version, "v"), SHA256: selfupdate.Digest(data)}
	slot.Path = slotPath(slot.Version, slot.SHA256)
	path, _ := location(root, slot)
	for _, dir := range []string{filepath.Join(root, "versions"), filepath.Dir(path)} {
		if err := os.Mkdir(dir, 0755); err != nil && !os.IsExist(err) {
			return Slot{}, err
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Slot{}, fmt.Errorf("版本目录不是普通目录")
		}
	}
	if Valid(root, slot) {
		return slot, nil
	}
	if err := atomicfile.Write(path, data, 0755); err != nil {
		return Slot{}, err
	}
	return slot, nil
}

func Initialize(root, image, version string) error {
	info, err := os.Lstat(image)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 256<<20 {
		return fmt.Errorf("镜像程序无效")
	}
	data, err := os.ReadFile(image)
	if err != nil {
		return err
	}
	return locked(root, func(state *State) error {
		slot, err := writeSlot(root, version, data)
		if err != nil {
			return err
		}
		if !Valid(root, state.Current) {
			if Valid(root, state.Previous) {
				state.Current = state.Previous
			} else {
				state.Current = slot
			}
			state.Previous = Slot{}
		}
		if state.Pending.Path != "" && !Valid(root, state.Pending) {
			state.Pending = Slot{}
		}
		cmp, _ := upgradepolicy.CompareNative(slot.Version, state.Current.Version)
		pendingCmp, _ := upgradepolicy.CompareNative(slot.Version, state.Pending.Version)
		imageChanged := state.ImageSHA256 != slot.SHA256
		if slot.SHA256 != state.RejectedSHA256 && slot.Path != state.Current.Path && (cmp > 0 || cmp == 0 && imageChanged) && (state.Pending.Path == "" || pendingCmp >= 0) {
			state.Pending = slot
		}
		state.ImageSHA256 = slot.SHA256
		return atomicfile.Write(filepath.Join(root, "manager.json"), []byte(`{"format":1}`), 0600)
	}, true)
}

func Stage(root, version string, data []byte) (Slot, error) {
	var staged Slot
	err := locked(root, func(state *State) error {
		if !Valid(root, state.Current) {
			return fmt.Errorf("当前运行程序无效，请重建镜像恢复")
		}
		cmp, ok := upgradepolicy.CompareNative(version, state.Current.Version)
		if !ok || cmp < 0 {
			return fmt.Errorf("不能覆盖已有的更高运行版本")
		}
		digest := selfupdate.Digest(data)
		if digest == state.RejectedSHA256 {
			return fmt.Errorf("此更新未通过启动健康检查，请使用其他版本")
		}
		if state.Pending.Path != "" {
			if cmp, ok := upgradepolicy.CompareNative(version, state.Pending.Version); ok && cmp < 0 {
				return fmt.Errorf("已有更高版本等待重启")
			}
		}
		var err error
		staged, err = writeSlot(root, version, data)
		if err != nil {
			return err
		}
		if staged.Path != state.Current.Path {
			state.Pending = staged
		}
		state.LastError = ""
		return nil
	}, false)
	return staged, err
}

func Confirm(root string, slot Slot) error {
	err := locked(root, func(state *State) error {
		if !Valid(root, slot) {
			return fmt.Errorf("不能启用无效的运行文件")
		}
		if state.Current.Path != slot.Path {
			state.Previous = state.Current
			state.Current = slot
		}
		if state.Pending.Path == slot.Path {
			state.Pending = Slot{}
		}
		state.LastError = ""
		return nil
	}, false)
	if err == nil {
		_ = Prune(root)
	}
	return err
}

func Reject(root string, slot Slot, reason string) error {
	return locked(root, func(state *State) error {
		if _, err := location(root, slot); err != nil {
			return err
		}
		if state.Current.Path == slot.Path {
			if !Valid(root, state.Previous) {
				return fmt.Errorf("新程序异常且没有可恢复的健康版本")
			}
			state.Current = state.Previous
			state.Previous = Slot{}
		}
		if state.Pending.Path == slot.Path {
			state.Pending = Slot{}
		}
		state.RejectedSHA256 = slot.SHA256
		if len(reason) > 500 {
			reason = reason[:500]
		}
		state.LastError = reason
		return nil
	}, false)
}

func Candidate(root string) (Slot, error) {
	state, err := Read(root)
	if err != nil {
		return Slot{}, err
	}
	for _, slot := range []Slot{state.Pending, state.Current, state.Previous} {
		if Valid(root, slot) {
			return slot, nil
		}
	}
	return Slot{}, fmt.Errorf("没有有效的运行程序")
}

func CurrentExecutable(root string) (string, error) {
	state, err := Read(root)
	if err != nil {
		return "", err
	}
	if !Valid(root, state.Current) {
		return "", fmt.Errorf("当前运行程序无效")
	}
	return location(root, state.Current)
}

func InstalledMatches(executable, version, digest string) bool {
	if !selfupdate.RuntimeManaged(executable) {
		return selfupdate.Matches(executable, digest)
	}
	state, err := Read(Directory())
	if err != nil {
		return false
	}
	for _, slot := range []Slot{state.Pending, state.Current} {
		cmp, ok := upgradepolicy.CompareNative(slot.Version, version)
		if ok && cmp == 0 && slot.SHA256 == digest && Valid(Directory(), slot) {
			return true
		}
	}
	return false
}

// Prune 仅删除本启动器命名的旧程序文件；不递归清理目录中的其他用户文件。
func Prune(root string) error {
	return locked(root, func(state *State) error {
		keep := map[string]bool{}
		for _, slot := range []Slot{state.Current, state.Previous, state.Pending} {
			if slot.Path != "" {
				keep[filepath.Base(filepath.Dir(slot.Path))] = true
			}
		}
		versions := filepath.Join(root, "versions")
		entries, err := os.ReadDir(versions)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			if keep[name] || !entry.IsDir() || len(name) < 18 {
				continue
			}
			version := name[:len(name)-17]
			if _, ok := upgradepolicy.CompareNative(version, version); !ok || name[len(name)-17] != '-' {
				continue
			}
			validHash := true
			for _, r := range name[len(name)-16:] {
				if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
					validHash = false
				}
			}
			if !validHash {
				continue
			}
			dir := filepath.Join(versions, name)
			info, err := os.Lstat(dir)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			path := filepath.Join(dir, "ncmm")
			info, err = os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			lease, err := filelock.TryAcquire(path + ".update.lock")
			if err != nil {
				continue
			}
			removeErr := os.Remove(path)
			lease.Close()
			if removeErr == nil {
				_ = os.Remove(path + ".update.lock")
				_ = os.Remove(dir)
			}
		}
		return nil
	}, false)
}
