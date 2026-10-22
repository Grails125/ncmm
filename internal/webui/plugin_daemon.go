package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/3899/ncmm/internal/atomicfile"
)

type pluginDaemonSettings struct {
	AutoStart bool     `json:"autoStart"`
	Args      []string `json:"args,omitempty"`
}
type pluginDaemonState struct {
	Name      string   `json:"name"`
	Running   bool     `json:"running"`
	AutoStart bool     `json:"autoStart"`
	Args      []string `json:"args"`
	Error     string   `json:"error,omitempty"`
}
type pluginDaemonRun struct {
	process *managedProcess
}
type pluginDaemonManager struct {
	// 操作锁串行化停止、替换与恢复；状态读取不等待网络下载。
	opMu       sync.Mutex
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	home       string
	opts       Options
	plugins    PluginManager
	supervisor *processSupervisor
	settings   map[string]pluginDaemonSettings
	running    map[string]*pluginDaemonRun
	failures   map[string]string
	outputs    map[string]*tailBuffer
	closed     bool
}

func newPluginDaemonManager(ctx context.Context, opts Options, plugins PluginManager, supervisor *processSupervisor) (*pluginDaemonManager, error) {
	ctx, cancel := context.WithCancel(ctx)
	m := &pluginDaemonManager{ctx: ctx, cancel: cancel, home: opts.Home, opts: opts, plugins: plugins, supervisor: supervisor, settings: make(map[string]pluginDaemonSettings), running: make(map[string]*pluginDaemonRun), failures: make(map[string]string), outputs: make(map[string]*tailBuffer)}
	data, err := os.ReadFile(m.settingsPath())
	if err != nil && !os.IsNotExist(err) {
		cancel()
		return nil, fmt.Errorf("无法读取守护插件设置")
	}
	if err == nil {
		if len(data) > 1<<20 || json.Unmarshal(data, &m.settings) != nil || m.settings == nil {
			cancel()
			return nil, fmt.Errorf("守护插件设置损坏，请修复后启动")
		}
		for name, settings := range m.settings {
			if !validDaemonName(name) || validateDaemonSettings(settings) != nil {
				cancel()
				return nil, fmt.Errorf("守护插件设置无效")
			}
		}
	}
	return m, nil
}
func (m *pluginDaemonManager) settingsPath() string {
	return filepath.Join(m.home, "plugin-daemons.json")
}
func validDaemonName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 128 {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
func validateDaemonSettings(settings pluginDaemonSettings) error {
	if len(settings.Args) > 64 {
		return fmt.Errorf("守护插件参数过多")
	}
	for _, arg := range settings.Args {
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return fmt.Errorf("守护插件参数无效")
		}
	}
	return nil
}
func (m *pluginDaemonManager) find(name string) (PluginInfo, error) {
	if !validDaemonName(name) || m.plugins == nil {
		return PluginInfo{}, fmt.Errorf("守护插件不存在")
	}
	for _, p := range m.plugins.DiscoverPlugins() {
		if strings.EqualFold(p.Name, name) {
			if p.Type != "daemon" {
				return PluginInfo{}, fmt.Errorf("该插件不是守护类型")
			}
			return p, nil
		}
	}
	return PluginInfo{}, fmt.Errorf("守护插件不存在")
}
func (m *pluginDaemonManager) configure(name string, settings pluginDaemonSettings) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	plugin, err := m.find(name)
	if err != nil {
		return err
	}
	if err := validateDaemonSettings(settings); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("服务正在关闭")
	}
	previous, hadPrevious := m.settings[plugin.Name]
	settings.Args = append([]string(nil), settings.Args...)
	m.settings[plugin.Name] = settings
	if err := m.saveLocked(); err != nil {
		if hadPrevious {
			m.settings[plugin.Name] = previous
		} else {
			delete(m.settings, plugin.Name)
		}
		return err
	}
	return nil
}
func (m *pluginDaemonManager) saveLocked() error {
	data, err := json.MarshalIndent(m.settings, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.Write(m.settingsPath(), data, 0600); err != nil {
		return fmt.Errorf("无法保存守护插件设置")
	}
	return nil
}
func (m *pluginDaemonManager) state(name string) (pluginDaemonState, error) {
	plugin, err := m.find(name)
	if err != nil {
		return pluginDaemonState{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stateLocked(plugin.Name), nil
}
func (m *pluginDaemonManager) stateLocked(name string) pluginDaemonState {
	settings := m.settings[name]
	return pluginDaemonState{Name: name, Running: m.running[name] != nil, AutoStart: settings.AutoStart, Args: append([]string{}, settings.Args...), Error: m.failures[name]}
}
func (m *pluginDaemonManager) decorate(plugins []PluginInfo) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range plugins {
		if plugins[i].Type == "daemon" {
			state := m.stateLocked(plugins[i].Name)
			plugins[i].Running = state.Running
			plugins[i].AutoStart = state.AutoStart
			plugins[i].DaemonError = state.Error
		}
	}
}
func (m *pluginDaemonManager) start(name string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.startLocked(name)
}
func (m *pluginDaemonManager) startLocked(name string) error {
	plugin, err := m.find(name)
	if err != nil {
		return err
	}
	executable, err := filepath.Abs(plugin.Path)
	if err != nil {
		return fmt.Errorf("无法定位守护插件程序，请检查安装目录")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.ctx.Err() != nil {
		return fmt.Errorf("服务正在关闭")
	}
	if m.running[plugin.Name] != nil {
		return nil
	}
	settings := m.settings[plugin.Name]
	// 守护插件接收现有插件协议的最小运行上下文，不隐式选择账号或执行任务。
	input, _ := json.Marshal(struct {
		Version    string `json:"version"`
		Command    string `json:"command"`
		Home       string `json:"home"`
		ConfigPath string `json:"config_path"`
	}{m.opts.Version, "plugin daemon", m.home, m.opts.ConfigPath})
	output := newTailBuffer(defaultProcessOutputLimit)
	process, err := m.supervisor.Start(m.ctx, processSpec{Kind: "plugin-daemon", Command: executable, Args: append([]string(nil), settings.Args...), Dir: filepath.Dir(executable), Env: append(os.Environ(), "NCMM_PLUGIN=1", "NCMM_HOME="+m.home, "NCMM_CONFIG_PATH="+m.opts.ConfigPath), Stdin: bytes.NewReader(input), Stdout: output, Stderr: output})
	if err != nil {
		m.failures[plugin.Name] = "守护插件启动失败，请检查可执行文件及运行环境"
		return fmt.Errorf("%s", m.failures[plugin.Name])
	}
	run := &pluginDaemonRun{process: process}
	m.running[plugin.Name] = run
	m.outputs[plugin.Name] = output
	delete(m.failures, plugin.Name)
	go func() {
		err := process.Wait()
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.running[plugin.Name] == run {
			delete(m.running, plugin.Name)
			if !process.StopRequested() && process.ContextError() == nil {
				if err != nil {
					m.failures[plugin.Name] = "守护插件异常退出"
				} else {
					m.failures[plugin.Name] = "守护插件已自行退出"
				}
			}
		}
	}()
	return nil
}
func (m *pluginDaemonManager) stop(name string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	plugin, err := m.find(name)
	if err != nil {
		return err
	}
	m.stopLocked(plugin.Name)
	return nil
}
func (m *pluginDaemonManager) stopLocked(name string) bool {
	m.mu.Lock()
	run := m.running[name]
	m.mu.Unlock()
	if run == nil {
		return false
	}
	run.process.Stop()
	_ = run.process.Wait()
	m.mu.Lock()
	if m.running[name] == run {
		delete(m.running, name)
	}
	m.mu.Unlock()
	return true
}
func (m *pluginDaemonManager) restart(name string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	plugin, err := m.find(name)
	if err != nil {
		return err
	}
	m.stopLocked(plugin.Name)
	return m.startLocked(plugin.Name)
}
func (m *pluginDaemonManager) mutate(name string, resume bool, operation func() error) error {
	if m == nil {
		return operation()
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	// 普通任务插件也走此方法，因此只规范名称，不要求 daemon 类型。
	for _, plugin := range m.plugins.DiscoverPlugins() {
		if strings.EqualFold(plugin.Name, name) {
			name = plugin.Name
			break
		}
	}
	wasRunning := m.stopLocked(name)
	err := operation()
	if err == nil && !resume {
		m.mu.Lock()
		previous, exists := m.settings[name]
		delete(m.settings, name)
		delete(m.failures, name)
		delete(m.outputs, name)
		saveErr := m.saveLocked()
		if saveErr != nil && exists {
			m.settings[name] = previous
		}
		m.mu.Unlock()
		if saveErr != nil {
			return saveErr
		}
	}
	if wasRunning && (resume || err != nil) {
		if restartErr := m.startLocked(name); restartErr != nil {
			if err != nil {
				return fmt.Errorf("操作失败且守护插件恢复失败：%w", err)
			}
			return restartErr
		}
	}
	return err
}
func (m *pluginDaemonManager) startAutomatic() {
	if m == nil || m.plugins == nil {
		return
	}
	m.mu.Lock()
	var names []string
	for name, settings := range m.settings {
		if settings.AutoStart {
			names = append(names, name)
		}
	}
	m.mu.Unlock()
	sort.Strings(names)
	for _, name := range names {
		if err := m.start(name); err != nil {
			m.mu.Lock()
			m.failures[name] = err.Error()
			m.mu.Unlock()
		}
	}
}
func (m *pluginDaemonManager) close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.closed = true
	m.cancel()
	runs := make([]*pluginDaemonRun, 0, len(m.running))
	for _, run := range m.running {
		runs = append(runs, run)
	}
	m.mu.Unlock()
	for _, run := range runs {
		run.process.Stop()
	}
	for _, run := range runs {
		_ = run.process.Wait()
	}
	m.mu.Lock()
	clear(m.running)
	m.mu.Unlock()
}

func pluginTaskValidator(plugins PluginManager) func(Schedule) error {
	return func(job Schedule) error {
		if plugins == nil || job.Command != "plugin" || len(job.Args) < 2 || job.Args[0] != "run" {
			return nil
		}
		for _, plugin := range plugins.DiscoverPlugins() {
			if strings.EqualFold(plugin.Name, job.Args[1]) && plugin.Type == "daemon" {
				return fmt.Errorf("守护插件不能作为普通任务运行或调度，请使用插件页的启动操作")
			}
		}
		return nil
	}
}
func (s *Server) handlePluginDaemon(w http.ResponseWriter, r *http.Request) {
	if s.daemons == nil {
		writeError(w, http.StatusBadRequest, "守护插件管理不可用")
		return
	}
	name := r.PathValue("name")
	if r.Method == http.MethodPut {
		var settings pluginDaemonSettings
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&settings); err != nil {
			writeError(w, http.StatusBadRequest, "守护插件设置格式无效")
			return
		}
		if err := s.daemons.configure(name, settings); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	state, err := s.daemons.state(name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, state)
}
func (s *Server) handlePluginDaemonAction(w http.ResponseWriter, r *http.Request) {
	if s.daemons == nil {
		writeError(w, http.StatusBadRequest, "守护插件管理不可用")
		return
	}
	name := r.PathValue("name")
	var err error
	switch r.PathValue("action") {
	case "start":
		err = s.daemons.start(name)
	case "stop":
		err = s.daemons.stop(name)
	case "restart":
		err = s.daemons.restart(name)
	default:
		writeError(w, http.StatusNotFound, "未知守护插件操作")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	state, err := s.daemons.state(name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, state)
}
func (m *pluginDaemonManager) logs(name string) (string, error) {
	plugin, err := m.find(name)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	output := m.outputs[plugin.Name]
	m.mu.Unlock()
	if output == nil {
		return "", nil
	}
	return string(output.Bytes()), nil
}
func (s *Server) handlePluginDaemonLog(w http.ResponseWriter, r *http.Request) {
	if s.daemons == nil {
		writeError(w, http.StatusBadRequest, "守护插件管理不可用")
		return
	}
	output, err := s.daemons.logs(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": r.PathValue("name"), "output": output})
}
