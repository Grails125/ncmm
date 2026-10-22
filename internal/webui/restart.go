package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"gopkg.in/yaml.v3"
)

type restartActivity struct {
	Running int `json:"running"`
	Queued  int `json:"queued"`
}

// 与任务提交、延迟到期及队列启动共用同一把锁，消除空闲检查后的启动竞态。
func (m *runManager) reserveRestart(force bool) (restartActivity, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	activity := restartActivity{Running: len(m.running), Queued: len(m.queued)}
	if !force && (activity.Running > 0 || activity.Queued > 0) {
		return activity, false
	}
	m.restarting = true
	return activity, true
}

func (s *Server) handleSystemRestart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Force bool `json:"force"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	err := decoder.Decode(&req)
	if err != nil && err != io.EOF {
		writeError(w, 400, "重启参数无效")
		return
	}
	if err == nil && decoder.Decode(new(any)) != io.EOF {
		writeError(w, 400, "重启参数无效")
		return
	}
	if !s.updateMu.TryLock() {
		writeError(w, 409, "正在检查或应用更新，请完成后再重启")
		return
	}
	defer s.updateMu.Unlock()
	activity, accepted := s.runner.reserveRestart(req.Force)
	if !accepted {
		writeJSON(w, 409, struct {
			restartActivity
			Error                string `json:"error"`
			ConfirmationRequired bool   `json:"confirmationRequired"`
		}{activity, "当前仍有运行或排队任务，请确认是否停止任务并重启", true})
		return
	}
	writeJSON(w, 202, map[string]any{"restarting": true, "instanceId": s.instance.InstanceID})
	s.signalRestart()
}

func (s *Server) signalRestart() {
	// HTTP Shutdown 会等待已接受的请求完成，先给响应写回留出时间。
	time.AfterFunc(150*time.Millisecond, func() {
		select {
		case s.restartCh <- struct{}{}:
		default:
		}
	})
}

func (s *Server) autoRestartLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.tryAutoRestart() {
				return
			}
		}
	}
}

func (s *Server) tryAutoRestart() bool {
	if !s.updateMu.TryLock() {
		return false
	}
	defer s.updateMu.Unlock()
	if time.Now().Before(s.autoRestartAfter) {
		return false
	}
	document, err := s.config.get()
	if err != nil || document.ParseError != "" {
		return false
	}
	var settings struct {
		Updater struct {
			AutoRestart *bool `yaml:"auto_restart"`
		} `yaml:"updater"`
	}
	if yaml.Unmarshal([]byte(document.Raw), &settings) != nil || (settings.Updater.AutoRestart != nil && !*settings.Updater.AutoRestart) {
		return false
	}
	state, err := s.readUpdateState()
	if err != nil || !s.makeUpdateView(state).RestartRequired {
		return false
	}
	if _, accepted := s.runner.reserveRestart(false); !accepted {
		return false
	}
	s.opts.Output("[webui] 更新已就绪，当前无运行及排队任务，自动重启生效\n")
	s.signalRestart()
	return true
}
