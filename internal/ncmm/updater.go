// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

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
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/internal/atomicfile"
	"github.com/3899/ncmm/internal/filelock"
	"github.com/3899/ncmm/internal/managedruntime"
	"github.com/3899/ncmm/internal/selfupdate"
	"github.com/3899/ncmm/internal/upgradepolicy"
	"github.com/3899/ncmm/pkg/log"
)

var updaterMu sync.Mutex

// 启动时保存路径，避免 Unix 原子替换后 os.Executable 指向已删除的旧 inode。
var runningUpdateExecutable, runningUpdateExecutableErr = os.Executable()

// UpdateState 定义跟目录 ncmm-update.json 中保存的状态元数据
type UpdateState struct {
	// 基础检测信息
	LastCheckTime  time.Time `json:"last_check_time"` // 上次网络 API 检测时间
	CurrentVersion string    `json:"current_version"` // 检查时运行版本
	LatestVersion  string    `json:"latest_version"`  // 远程最新版本号 (如 1.1.14)
	ReleaseNotes   string    `json:"release_notes"`   // 最新版本的更新说明/日志摘要

	// 自动热更新状态
	UpdateStatus    string    `json:"update_status"`              // 状态: "idle" | "updating" | "success" | "failed"
	UpdatedVersion  string    `json:"updated_version"`            // 已成功替换完成的目标版本号
	LastUpdateTime  time.Time `json:"last_update_time"`           // 上次热替换成功完成时间
	LastError       string    `json:"last_error"`                 // 上次失败错误日志
	InstalledSHA256 string    `json:"installed_sha256,omitempty"` // 安装回执，用于区分待重启与二进制降级

	// 系统架构及目标资产信息
	OS                  string    `json:"os"`           // 操作系统 (如 Windows / Linux / Darwin)
	Arch                string    `json:"arch"`         // 系统架构 (如 x86_64 / arm64)
	AssetFilename       string    `json:"target_asset"` // 匹配的压缩包文件名 (如 ncmm_Linux_x86_64.tar.gz)
	DownloadURL         string    `json:"download_url"` // 资产原始下载链接
	LastCheckFailed     bool      `json:"last_check_failed,omitempty"`
	RequiredVersion     string    `json:"required_version,omitempty"`
	RequiredPublishedAt time.Time `json:"required_published_at,omitempty"`
}

type githubRelease struct {
	TagName     string        `json:"tag_name"`
	Body        string        `json:"body"`
	Assets      []githubAsset `json:"assets"`
	PublishedAt time.Time     `json:"published_at"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// getHomePath 获取工作目录家目录
func (c *Root) getHomePath() string {
	if c.Opts.Home != "" {
		return filepath.Clean(c.Opts.Home)
	}
	return filepath.Clean(config.HomeDir)
}

// getUpdateStatePath 获取 ncmm-update.json 绝对路径
func (c *Root) getUpdateStatePath() string {
	return filepath.Join(c.getHomePath(), "ncmm-update.json")
}

// initDefaultUpdateState 基于当前二进制版本和操作系统架构初始化默认状态
func (c *Root) initDefaultUpdateState() UpdateState {
	currentVer := c.AppVersion
	if currentVer == "" {
		currentVer = "0.0.0"
	}
	osPart, archPart, ext := getPlatformInfo()
	targetName := fmt.Sprintf("ncmm_%s_%s%s", osPart, archPart, ext)
	downloadURL := fmt.Sprintf("https://github.com/Grails125/ncmm/releases/download/v%s/%s", strings.TrimPrefix(currentVer, "v"), targetName)

	return UpdateState{
		LastCheckTime:  time.Unix(0, 0), // 初始设为 Epoch 时间，确保首次允许触发网络检测
		CurrentVersion: currentVer,
		LatestVersion:  "v" + strings.TrimPrefix(currentVer, "v"),
		UpdateStatus:   "idle",
		OS:             osPart,
		Arch:           archPart,
		AssetFilename:  targetName,
		DownloadURL:    downloadURL,
	}
}

// saveUpdateState 并发安全地保存 ncmm-update.json 状态数据
func (c *Root) saveUpdateState(statePath string, state *UpdateState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0755); err != nil {
		return err
	}
	if err := atomicfile.Write(statePath, data, 0600); err != nil {
		return fmt.Errorf("保存更新状态失败: %w", err)
	}
	return nil
}

func (c *Root) loadUpdateState(statePath, execPath string) UpdateState {
	state := c.initDefaultUpdateState()
	data, err := os.ReadFile(statePath)
	if err != nil {
		return state
	}
	if json.Unmarshal(data, &state) != nil || !isNativeUpdate(state.LatestVersion, state.AssetFilename, state.DownloadURL) {
		log.Warn("[updater] 更新缓存无效，重新检查本分支发布")
		return c.initDefaultUpdateState()
	}
	if selfupdate.RuntimeManaged(execPath) {
		if runtimeState, err := managedruntime.Read(managedruntime.Directory()); err == nil && state.InstalledSHA256 != "" && runtimeState.RejectedSHA256 == state.InstalledSHA256 {
			if CompareVersions(c.AppVersion, state.LatestVersion) >= 0 {
				state.UpdateStatus, state.UpdatedVersion, state.InstalledSHA256, state.LastError = "idle", "", "", ""
				state.CurrentVersion = c.AppVersion
				return state
			}
			state.UpdateStatus = "failed"
			state.UpdatedVersion = ""
			state.LastError = "候选程序未通过健康检查，已保留原运行版本。" + runtimeState.LastError
			state.CurrentVersion = c.AppVersion
			return state
		}
	}
	installed := managedruntime.InstalledMatches(execPath, state.LatestVersion, state.InstalledSHA256)
	if state.UpdateStatus == "updating" {
		if installed {
			state.UpdateStatus = "success"
			state.UpdatedVersion = state.LatestVersion
			state.LastError = ""
		} else {
			state.UpdateStatus = "failed"
			state.UpdatedVersion = ""
			state.InstalledSHA256 = ""
			state.LastError = "上次更新未完成，可重新检查并更新；原程序备份保留在 .old 文件"
		}
	}
	if state.UpdatedVersion != "" && !installed {
		state.UpdatedVersion = ""
		state.InstalledSHA256 = ""
		state.UpdateStatus = "idle"
		state.LastError = ""
		state.LastCheckTime = time.Time{}
	}
	state.CurrentVersion = c.initDefaultUpdateState().CurrentVersion
	return state
}

func updateExecutablePath() (string, error) {
	if runningUpdateExecutableErr != nil {
		return "", runningUpdateExecutableErr
	}
	return filepath.EvalSymlinks(runningUpdateExecutable)
}

// 所有写入固定先锁程序、再锁状态文件；仅检查版本无需程序目录的写权限。
func (c *Root) lockUpdater(execPath string, install bool) (func(), error) {
	var executableLock *filelock.Lock
	if install {
		var err error
		executableLock, err = filelock.TryAcquire(execPath + ".update.lock")
		if err != nil {
			return nil, fmt.Errorf("无法获取程序更新锁，请稍后重试: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stateLock, err := filelock.Acquire(ctx, c.getUpdateStatePath()+".lock")
	if err != nil {
		if executableLock != nil {
			executableLock.Close()
		}
		return nil, fmt.Errorf("无法获取更新状态锁，请稍后重试: %w", err)
	}
	return func() {
		stateLock.Close()
		if executableLock != nil {
			executableLock.Close()
		}
	}, nil
}

// CheckForUpdatesPreRun 执行任务前阶段的版本检测与同步热替换
func (c *Root) CheckForUpdatesPreRun() {
	// 即使关闭联网检查，仍需提示有效缓存中的宽限期；任务结束时不重复输出。
	defer c.showUpgradeGraceNotice()
	// 获取配置中的 updater 控制参数 (支持配置文件与 NCMM_UPDATER_CHECK / NCMM_UPDATER_AUTO_UPDATE 环境变量)
	checkEnabled := true
	autoUpdateEnabled := true
	if c.Cfg != nil && c.Cfg.Updater != nil {
		if c.Cfg.Updater.Check != nil {
			checkEnabled = *c.Cfg.Updater.Check
		}
		if c.Cfg.Updater.AutoUpdate != nil {
			autoUpdateEnabled = *c.Cfg.Updater.AutoUpdate
		}
	}

	// 显式支持环境变量覆写判断 (如 NCMM_UPDATER_CHECK=false 或 0)
	if isEnvFalse("NCMM_UPDATER_CHECK") {
		checkEnabled = false
	}
	if isEnvFalse("NCMM_UPDATER_AUTO_UPDATE") {
		autoUpdateEnabled = false
	}

	if !checkEnabled {
		log.Debug("[updater] 版本自动检测与升级功能已关闭 (updater.check=false)")
		return
	}

	updaterMu.Lock()
	defer updaterMu.Unlock()
	execPath, err := updateExecutablePath()
	if err != nil {
		log.Warn("[updater] 获取程序路径失败: %v", err)
		return
	}
	needAutoUpdate := autoUpdateEnabled && (os.Getenv("NCMM_DOCKER_OFFICIAL") != "1" || selfupdate.RuntimeManaged(execPath))
	unlock, err := c.lockUpdater(execPath, needAutoUpdate)
	if err != nil {
		log.Warn("[updater] %v", err)
		return
	}
	defer unlock()
	statePath := c.getUpdateStatePath()
	state := c.loadUpdateState(statePath, execPath)
	currentVer := state.CurrentVersion
	if err := c.saveUpdateState(statePath, &state); err != nil {
		log.Warn("[updater] %v", err)
		return
	}

	// 1. 命中已有的状态记录：如果记录的最新版本大于当前版本
	if state.LatestVersion != "" && CompareVersions(currentVer, state.LatestVersion) < 0 {
		log.Info("[updater] 发现可用新版本: %s (当前版本: %s)", state.LatestVersion, currentVer)
		if needAutoUpdate && state.UpdatedVersion != state.LatestVersion && !(state.UpdateStatus == "failed" && state.InstalledSHA256 != "") {
			_ = c.performSelfUpdateFromState(&state, statePath, execPath)
			return
		}
	}

	// 2. 频率控制：如果距离上次 API 网络检测不足 24 小时，不重复发起网络请求
	if time.Since(state.LastCheckTime) < 24*time.Hour {
		return
	}

	// 3. 发起同步网络 API 检测并处理热替换
	state, err = c.checkNewVersionSync(statePath, currentVer, execPath)
	if err != nil {
		log.Warn("[updater] 检查失败: %v", err)
		return
	}
	if needAutoUpdate && CompareVersions(currentVer, state.LatestVersion) < 0 && state.UpdatedVersion != state.LatestVersion && !(state.UpdateStatus == "failed" && state.InstalledSHA256 != "") {
		_ = c.performSelfUpdateFromState(&state, statePath, execPath)
	}
}

func (c *Root) checkNewVersionSync(statePath string, currentVer string, execPath string) (state UpdateState, resultErr error) {
	state = c.loadUpdateState(statePath, execPath)
	defer func() {
		if resultErr != nil {
			state.LastCheckFailed = true
			if err := c.saveUpdateState(statePath, &state); err != nil {
				log.Warn("[updater] 保存检查失败状态: %v", err)
			}
		}
	}()
	proxyMirrors := []string{"https://gh-proxy.com/", "https://ghproxy.net/", "https://githubproxy.cc/"}
	if c.Cfg != nil && c.Cfg.Updater != nil && len(c.Cfg.Updater.ProxyMirrors) > 0 {
		proxyMirrors = c.Cfg.Updater.ProxyMirrors
	}

	apiURL := "https://api.github.com/repos/Grails125/ncmm/releases/latest"
	urlsToTry := []string{apiURL}
	for _, proxy := range proxyMirrors {
		if proxy != "" {
			urlsToTry = append(urlsToTry, proxy+apiURL)
		}
	}

	var resp *http.Response
	var responseCancel context.CancelFunc
	var lastErr error
	var succeeded bool

	for _, targetURL := range urlsToTry {
		log.Debug("[updater] 正在获取最新版本信息: %s", targetURL)

		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "ncmm-updater/"+currentVer)

		resp, lastErr = http.DefaultClient.Do(req)
		if lastErr == nil {
			if resp.StatusCode == http.StatusOK {
				responseCancel = cancel
				succeeded = true
				break
			}
			lastErr = fmt.Errorf("HTTP status error: %d", resp.StatusCode)
		}
		cancel()
		if resp != nil {
			resp.Body.Close()
		}
	}

	if !succeeded {
		log.Debug("[updater] 获取最新版本信息失败: %v", lastErr)
		return state, fmt.Errorf("获取最新版本信息失败: %w", lastErr)
	}
	defer responseCancel()
	defer resp.Body.Close()

	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		log.Warn("[updater] 解析版本信息失败: %v", err)
		return state, fmt.Errorf("解析版本信息失败: %w", err)
	}

	tag := strings.TrimSpace(rel.TagName)
	if tag == "" {
		return state, fmt.Errorf("最新版本响应缺少 tag_name")
	}

	osPart, archPart, ext := getPlatformInfo()
	targetName := fmt.Sprintf("ncmm_%s_%s%s", osPart, archPart, ext)
	var downloadURL string
	for _, asset := range rel.Assets {
		if strings.EqualFold(asset.Name, targetName) {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		downloadURL = fmt.Sprintf("https://github.com/Grails125/ncmm/releases/download/%s/%s", tag, targetName)
	}
	if !isNativeUpdate(tag, targetName, downloadURL) {
		return state, fmt.Errorf("此发布不属于 Grails125 原生集成版本，请从本分支源码构建")
	}

	state.LastCheckTime = time.Now()
	state.LastCheckFailed = false
	policyCache := upgradepolicy.Cache{RequiredVersion: state.RequiredVersion, RequiredPublishedAt: state.RequiredPublishedAt}
	policyCache.Observe(tag, rel.Body, rel.PublishedAt)
	state.RequiredVersion = policyCache.RequiredVersion
	state.RequiredPublishedAt = policyCache.RequiredPublishedAt
	state.CurrentVersion = currentVer
	if state.LatestVersion != tag && state.UpdateStatus == "failed" {
		state.InstalledSHA256 = ""
	}
	state.LatestVersion = tag
	state.ReleaseNotes = rel.Body
	state.OS = osPart
	state.Arch = archPart
	state.AssetFilename = targetName
	state.DownloadURL = downloadURL

	if CompareVersions(currentVer, tag) >= 0 && state.UpdateStatus != "success" {
		state.UpdateStatus = "idle"
		state.LastError = ""
	}
	if err := c.saveUpdateState(statePath, &state); err != nil {
		return state, err
	}

	if CompareVersions(currentVer, tag) >= 0 {
		return state, nil
	}

	log.Info("[updater] 发现可用新版本: %s (当前版本: %s)", tag, currentVer)

	return state, nil
}

// performSelfUpdateFromState 从状态结构中提取配置并执行全自动热替换
func (c *Root) performSelfUpdateFromState(state *UpdateState, statePath, execPath string) error {
	binaryBytes, err := c.downloadUpdate(state.LatestVersion, state.AssetFilename, state.DownloadURL)
	if err != nil {
		state.UpdateStatus = "failed"
		state.LastError = err.Error()
		if saveErr := c.saveUpdateState(statePath, state); saveErr != nil {
			log.Warn("[updater] %v", saveErr)
		}
		log.Warn("[updater] 下载更新失败: %v", err)
		return err
	}
	if c.CfgPath != "" && c.CfgPath != "default" {
		if _, err := config.BackupConfigFile(c.CfgPath, 5); err != nil {
			return fmt.Errorf("升级前备份配置失败: %w", err)
		}
	}
	state.UpdateStatus = "updating"
	state.UpdatedVersion = ""
	state.InstalledSHA256 = selfupdate.Digest(binaryBytes)
	state.LastError = ""
	if err := c.saveUpdateState(statePath, state); err != nil {
		return err
	}

	log.Info("[updater] 正在自动升级至 %s ...", state.LatestVersion)
	// 不同 home 可能共用同一个程序；已有相同文件时保留上次备份，只补本实例回执。
	if selfupdate.RuntimeManaged(execPath) {
		_, err = managedruntime.Stage(managedruntime.Directory(), state.LatestVersion, binaryBytes)
	} else if !selfupdate.Matches(execPath, state.InstalledSHA256) {
		err = selfupdate.Replace(execPath, binaryBytes)
	}
	if err != nil {
		state.UpdateStatus = "failed"
		state.LastError = err.Error()
		if saveErr := c.saveUpdateState(statePath, state); saveErr != nil {
			log.Warn("[updater] %v", saveErr)
		}
		log.Warn("[updater] 自动升级失败: %s", err)
		return err
	} else {
		state.UpdateStatus = "success"
		state.UpdatedVersion = state.LatestVersion
		state.LastUpdateTime = time.Now()
		state.LastError = ""
		if err := c.saveUpdateState(statePath, state); err != nil {
			return fmt.Errorf("程序已替换，保存成功状态失败；下次检查将核对安装文件: %w", err)
		}
		log.Info("[updater] ncmm 已成功升级至 %s 版本", state.LatestVersion)
	}
	if c.CfgPath != "" && c.CfgPath != "default" {
		if err := config.AutoUpgradeConfig(c.CfgPath); err != nil {
			log.Warn("[updater] 配置文件自动升级合并失败: %s", err)
		}
	}
	return nil
}

// CheckForUpdatesNow bypasses the daily throttle for an explicit user request.
func (c *Root) CheckForUpdatesNow() (UpdateState, error) {
	updaterMu.Lock()
	defer updaterMu.Unlock()
	execPath, err := updateExecutablePath()
	if err != nil {
		return c.initDefaultUpdateState(), err
	}
	unlock, err := c.lockUpdater(execPath, false)
	if err != nil {
		return c.initDefaultUpdateState(), err
	}
	defer unlock()
	return c.checkNewVersionSync(c.getUpdateStatePath(), c.initDefaultUpdateState().CurrentVersion, execPath)
}

// ApplyAvailableUpdate 每次主动检查本仓库最新发布，再在锁内安装。
func (c *Root) ApplyAvailableUpdate() (UpdateState, error) {
	execPath, err := updateExecutablePath()
	if err != nil {
		return c.initDefaultUpdateState(), err
	}
	if os.Getenv("NCMM_DOCKER_OFFICIAL") == "1" && !selfupdate.RuntimeManaged(execPath) {
		return c.initDefaultUpdateState(), fmt.Errorf("此 Docker 容器未由启动器管理，请重建本仓库镜像后更新")
	}
	return c.applyAvailableUpdateAt(execPath)
}

func (c *Root) applyAvailableUpdateAt(execPath string) (UpdateState, error) {
	updaterMu.Lock()
	defer updaterMu.Unlock()
	statePath := c.getUpdateStatePath()
	unlock, err := c.lockUpdater(execPath, true)
	if err != nil {
		return c.initDefaultUpdateState(), err
	}
	defer unlock()
	state, err := c.checkNewVersionSync(statePath, c.initDefaultUpdateState().CurrentVersion, execPath)
	if err != nil {
		return state, err
	}
	if CompareVersions(state.CurrentVersion, state.LatestVersion) >= 0 || state.UpdatedVersion == state.LatestVersion {
		return state, nil
	}
	if err := c.performSelfUpdateFromState(&state, statePath, execPath); err != nil {
		return state, err
	}
	return state, nil
}

// downloadUpdate 仅下载并解包本分支的目标资产，不改动运行程序。
func (c *Root) downloadUpdate(tag string, assetName string, rawDownloadURL string) ([]byte, error) {
	osPart, archPart, ext := getPlatformInfo()
	if assetName == "" {
		assetName = fmt.Sprintf("ncmm_%s_%s%s", osPart, archPart, ext)
	}
	if rawDownloadURL == "" {
		rawDownloadURL = fmt.Sprintf("https://github.com/Grails125/ncmm/releases/download/%s/%s", tag, assetName)
	}
	if !isNativeUpdate(tag, assetName, rawDownloadURL) {
		return nil, fmt.Errorf("拒绝安装其他来源的更新，请检查 Grails125/ncmm 原生集成版本")
	}

	proxyMirrors := []string{"https://gh-proxy.com/", "https://ghproxy.net/", "https://githubproxy.cc/"}
	if c.Cfg != nil && c.Cfg.Updater != nil && len(c.Cfg.Updater.ProxyMirrors) > 0 {
		proxyMirrors = c.Cfg.Updater.ProxyMirrors
	}

	urlsToTry := []string{rawDownloadURL}
	for _, proxy := range proxyMirrors {
		if proxy != "" {
			urlsToTry = append(urlsToTry, proxy+rawDownloadURL)
		}
	}

	var resp *http.Response
	var responseCancel context.CancelFunc
	var lastErr error
	var succeeded bool

	for _, targetURL := range urlsToTry {
		log.Debug("[updater] 正在下载更新包: %s", targetURL)

		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "ncmm-updater")

		resp, lastErr = http.DefaultClient.Do(req)
		if lastErr == nil {
			if resp.StatusCode == http.StatusOK {
				responseCancel = cancel
				succeeded = true
				break
			}
			lastErr = fmt.Errorf("HTTP status error: %d", resp.StatusCode)
		}
		cancel()
		if resp != nil {
			resp.Body.Close()
		}
		log.Warn("[updater] 从 %s 下载更新包失败，正在重试备用源...", targetURL)
	}

	if !succeeded {
		return nil, fmt.Errorf("下载更新包失败，已尝试所有下载方式。最后一次错误: %w", lastErr)
	}
	defer responseCancel()
	defer resp.Body.Close()

	archiveBytes, err := readUpdateBytes(resp.Body)
	if err != nil {
		return nil, err
	}

	var binaryBytes []byte
	binaryName := "ncmm"
	if runtime.GOOS == "windows" {
		binaryName = "ncmm.exe"
	}

	if ext == ".zip" {
		zipReader, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
		if err != nil {
			return nil, fmt.Errorf("zip.NewReader: %w", err)
		}
		for _, file := range zipReader.File {
			if strings.EqualFold(filepath.Base(file.Name), binaryName) {
				rc, err := file.Open()
				if err != nil {
					return nil, err
				}
				binaryBytes, err = readUpdateBytes(rc)
				rc.Close()
				if err != nil {
					return nil, err
				}
				break
			}
		}
	} else {
		// tar.gz
		gzipReader, err := gzip.NewReader(bytes.NewReader(archiveBytes))
		if err != nil {
			return nil, fmt.Errorf("gzip.NewReader: %w", err)
		}
		defer gzipReader.Close()

		tarReader := tar.NewReader(gzipReader)
		for {
			header, err := tarReader.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if strings.EqualFold(filepath.Base(header.Name), binaryName) {
				binaryBytes, err = readUpdateBytes(tarReader)
				if err != nil {
					return nil, err
				}
				break
			}
		}
	}

	if len(binaryBytes) == 0 {
		return nil, fmt.Errorf("在升级包中找不到可执行文件: %s", binaryName)
	}
	return binaryBytes, nil
}

func readUpdateBytes(r io.Reader) ([]byte, error) {
	const maxUpdateSize = 256 << 20
	data, err := io.ReadAll(io.LimitReader(r, maxUpdateSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxUpdateSize {
		return nil, fmt.Errorf("更新文件超过 256 MiB 限制")
	}
	return data, nil
}

func (c *Root) showUpgradeGraceNotice() {
	policy := upgradepolicy.Read(c.getHomePath(), c.AppVersion, time.Now())
	if policy.Required || policy.RequiredVersion == "" {
		return
	}
	c.upgradeGraceNoticeOnce.Do(func() {
		var output io.Writer = os.Stderr
		if c.cmd != nil {
			output = c.cmd.ErrOrStderr()
		}
		fmt.Fprintf(output, "版本升级提醒：当前版本 %s，发布方要求升级至 %s，请于 %s 前升级；宽限期内仍可执行任务。\n", policy.CurrentVersion, policy.RequiredVersion, policy.GraceUntil.Format(time.RFC3339))
	})
}

// CompareVersions 比对版本号。v1 > v2 返回 1，v1 < v2 返回 -1，相等返回 0
func isNativeUpdate(tag, assetName, downloadURL string) bool {
	if !strings.Contains(tag, "-grails.") || strings.ContainsAny(tag, "/\\?#") || assetName == "" || strings.ContainsAny(assetName, "/\\?#") {
		return false
	}
	return downloadURL == "https://github.com/Grails125/ncmm/releases/download/"+tag+"/"+assetName
}

func CompareVersions(v1, v2 string) int {
	p1 := parseVersion(v1)
	p2 := parseVersion(v2)

	for i := 0; i < len(p1) || i < len(p2); i++ {
		var val1, val2 int
		if i < len(p1) {
			val1 = p1[i]
		}
		if i < len(p2) {
			val2 = p2[i]
		}
		if val1 < val2 {
			return -1
		} else if val1 > val2 {
			return 1
		}
	}
	return 0
}

func parseVersion(v string) []int {
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	parts := strings.Split(v, ".")
	res := make([]int, 0, len(parts))
	for _, p := range parts {
		var digits string
		for _, r := range p {
			if r >= '0' && r <= '9' {
				digits += string(r)
			} else {
				break
			}
		}
		if digits != "" {
			val, _ := strconv.Atoi(digits)
			res = append(res, val)
		} else {
			res = append(res, 0)
		}
	}
	return res
}

func getPlatformInfo() (string, string, string) {
	osPart := runtime.GOOS
	switch runtime.GOOS {
	case "windows":
		osPart = "Windows"
	case "linux":
		osPart = "Linux"
	case "darwin":
		osPart = "Darwin"
	default:
		if len(osPart) > 0 {
			osPart = strings.ToUpper(osPart[:1]) + osPart[1:]
		}
	}

	archPart := runtime.GOARCH
	switch runtime.GOARCH {
	case "amd64":
		archPart = "x86_64"
	case "arm64":
		archPart = "arm64"
	case "arm":
		archPart = "armv6"
	}

	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}

	return osPart, archPart, ext
}

func isEnvFalse(key string) bool {
	val := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return val == "0" || val == "false" || val == "off" || val == "no"
}
