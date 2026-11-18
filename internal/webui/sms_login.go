package webui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/internal/loginresult"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

var smsPhonePattern = regexp.MustCompile(`^[0-9]{5,15}$`)
var smsCaptchaPattern = regexp.MustCompile(`^[0-9]{4,8}$`)

type smsLoginClient interface {
	send(context.Context, string, int64) error
	login(context.Context, string, int64, string) (loginresult.Result, []byte, error)
	close()
}

type smsLoginView struct {
	ID         string `json:"id"`
	RetryAfter int    `json:"retryAfter"`
}
type smsLoginSession struct {
	phone, filename, revision string
	countryCode               int64
	main                      bool
	expires                   time.Time
	attempts                  int
	client                    smsLoginClient
}

type smsLoginManager struct {
	mu       sync.Mutex
	now      func() time.Time
	factory  func(context.Context) (smsLoginClient, error)
	sessions map[string]*smsLoginSession
	nextSend map[string]time.Time
}

type smsLoginError struct {
	status     int
	message    string
	retryAfter int
}

func (e *smsLoginError) Error() string { return e.message }
func smsError(status int, message string) error {
	return &smsLoginError{status: status, message: message}
}

func newSMSLoginManager(factory func(context.Context) (smsLoginClient, error)) *smsLoginManager {
	return &smsLoginManager{now: time.Now, factory: factory, sessions: make(map[string]*smsLoginSession), nextSend: make(map[string]time.Time)}
}

func (m *smsLoginManager) pruneLocked() {
	now := m.now()
	for id, session := range m.sessions {
		if !now.Before(session.expires) {
			session.client.close()
			delete(m.sessions, id)
		}
	}
	for phone, next := range m.nextSend {
		if !now.Before(next) {
			delete(m.nextSend, phone)
		}
	}
}

func (m *smsLoginManager) send(ctx context.Context, phone string, countryCode int64, filename string, main bool, revision string) (smsLoginView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	if err := ctx.Err(); err != nil {
		return smsLoginView{}, err
	}
	if !smsPhonePattern.MatchString(phone) || countryCode < 1 || countryCode > 999 {
		return smsLoginView{}, smsError(400, "请输入有效的手机号和国家区号")
	}
	key := fmt.Sprintf("%d:%s", countryCode, phone)
	if next := m.nextSend[key]; m.now().Before(next) {
		return smsLoginView{}, &smsLoginError{status: 429, message: "验证码发送过于频繁，请稍后重试", retryAfter: int(next.Sub(m.now()).Seconds()) + 1}
	}
	if len(m.sessions) >= 8 {
		return smsLoginView{}, smsError(429, "待验证登录过多，请稍后重试")
	}
	normalizedFilename, err := validateCookieFilename(filename)
	if err != nil {
		return smsLoginView{}, smsError(400, "Cookie 文件名无效")
	}
	filename = normalizedFilename
	// 发送结果未知时也保留冷却，避免重复发送短信；失败不生成可登录会话。
	m.nextSend[key] = m.now().Add(time.Minute)
	client, err := m.factory(ctx)
	if err != nil {
		return smsLoginView{}, smsError(503, "短信登录暂不可用，请稍后重试")
	}
	if err = client.send(ctx, phone, countryCode); err != nil {
		client.close()
		return smsLoginView{}, smsError(502, "验证码发送未成功，请稍后重试或使用扫码登录")
	}
	if err := ctx.Err(); err != nil {
		client.close()
		return smsLoginView{}, err
	}
	for id, old := range m.sessions {
		if old.phone == phone && old.countryCode == countryCode {
			old.client.close()
			delete(m.sessions, id)
		}
	}
	id := uuid.NewString()
	m.sessions[id] = &smsLoginSession{phone: phone, countryCode: countryCode, filename: filename, main: main, revision: revision, expires: m.now().Add(5 * time.Minute), client: client}
	return smsLoginView{ID: id, RetryAfter: 60}, nil
}

func (m *smsLoginManager) login(ctx context.Context, id, captcha string, commit func(string, loginresult.Result, []byte) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	session := m.sessions[id]
	if session == nil {
		return smsError(410, "短信登录已过期，请重新获取验证码")
	}
	if err := ctx.Err(); err != nil {
		session.client.close()
		delete(m.sessions, id)
		return err
	}
	if !smsCaptchaPattern.MatchString(captcha) {
		return smsError(400, "请输入 4～8 位数字验证码")
	}
	if session.attempts >= 5 {
		return smsError(429, "验证次数过多，请重新获取验证码")
	}
	session.attempts++
	result, data, err := session.client.login(ctx, session.phone, session.countryCode, captcha)
	// 外部登录可能已返回，但用户请求取消或会话在网络等待期间到期时不得覆盖账号。
	if contextErr := ctx.Err(); contextErr != nil {
		session.client.close()
		delete(m.sessions, id)
		return contextErr
	}
	if !m.now().Before(session.expires) {
		session.client.close()
		delete(m.sessions, id)
		return smsError(410, "短信登录已过期，请重新获取验证码")
	}
	if err != nil || result.UID <= 0 || len(data) == 0 {
		return smsError(400, "验证码登录未成功，请检查验证码或改用扫码登录")
	}
	result.AccountPath = "./" + session.filename
	result.Main = session.main
	if err := ctx.Err(); err != nil {
		session.client.close()
		delete(m.sessions, id)
		return err
	}
	if err := commit(session.revision, result, data); err != nil {
		session.client.close()
		delete(m.sessions, id)
		return err
	}
	session.client.close()
	delete(m.sessions, id)
	return nil
}

func (m *smsLoginManager) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		s.client.close()
		delete(m.sessions, id)
	}
}

func (m *smsLoginManager) cancel(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if session := m.sessions[id]; session != nil {
		session.client.close()
		delete(m.sessions, id)
	}
}

func (s *Server) handleSMSLoginCancel(w http.ResponseWriter, r *http.Request) {
	s.sms.cancel(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

type remoteSMSClient struct {
	client    *api.Client
	path, dir string
	identity  eapi.SMSLoginIdentity
}

func (s *Server) newSMSClient(ctx context.Context) (smsLoginClient, error) {
	document, err := s.config.get()
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Network *api.Config `yaml:"network"`
	}
	if err := yaml.Unmarshal([]byte(document.Raw), &cfg); err != nil || cfg.Network == nil {
		return nil, errors.New("network unavailable")
	}
	dir, err := os.MkdirTemp(s.opts.Home, ".sms-login-*")
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0700)
	path := filepath.Join(dir, "cookie.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	network := *cfg.Network
	network.Debug = false
	network.Retry = 0
	network.Timeout = 20 * time.Second
	network.UserAgent.AutoUpdate = false
	network.Cookie.Filepath = path
	network.Cookie.ReadOnly = false
	network.Cookie.Interval = 0
	client, err := api.NewClient(&network, nil)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &remoteSMSClient{client: client, path: path, dir: dir, identity: eapi.NewSMSLoginIdentity()}, nil
}
func (c *remoteSMSClient) send(ctx context.Context, phone string, country int64) error {
	response, err := eapi.New(c.client).SMSCaptchaSend(ctx, phone, country, c.identity)
	if err != nil {
		return err
	}
	if response == nil || response.Code != 200 || strings.TrimSpace(string(response.Data)) != "true" {
		return errors.New("sms rejected")
	}
	return nil
}
func (c *remoteSMSClient) login(ctx context.Context, phone string, country int64, captcha string) (loginresult.Result, []byte, error) {
	request := eapi.New(c.client)
	response, err := request.SMSCellphoneLogin(ctx, phone, country, captcha, c.identity)
	if err != nil {
		return loginresult.Result{}, nil, err
	}
	if response == nil || response.Code != 200 || c.client.MusicU() == "" {
		return loginresult.Result{}, nil, errors.New("login rejected")
	}
	user, err := request.GetUserInfo(ctx, &eapi.GetUserInfoReq{})
	if err != nil {
		return loginresult.Result{}, nil, err
	}
	if user == nil || user.Code != 200 || user.Account.Id <= 0 || user.Profile.UserId != user.Account.Id || user.Account.Id != response.Account.Id {
		return loginresult.Result{}, nil, errors.New("account not verified")
	}
	// Cookie 的同步写入只发生在独立临时会话，已配置账号在最终提交前保持原样。
	data, err := os.ReadFile(c.path)
	return loginresult.Result{UID: user.Account.Id, Nickname: user.Profile.Nickname, AvatarURL: user.Profile.AvatarUrl}, data, err
}
func (c *remoteSMSClient) close() { _ = c.client.Close(context.Background()); _ = os.RemoveAll(c.dir) }

func (s *Server) handleSMSLoginSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone       string `json:"phone"`
		CountryCode int64  `json:"countryCode"`
		Filename    string `json:"filename"`
		Main        bool   `json:"main"`
	}
	if !decodeJSON(w, r, &req, 4<<10) {
		return
	}
	if req.CountryCode == 0 {
		req.CountryCode = 86
	}
	if req.Filename == "" {
		if req.Main {
			req.Filename = "cookie.json"
		} else {
			req.Filename = "fan1.json"
		}
	}
	document, err := s.config.get()
	if err != nil {
		s.writeSMSError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	view, err := s.sms.send(ctx, req.Phone, req.CountryCode, req.Filename, req.Main, document.Revision)
	if err != nil {
		s.writeSMSError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, view)
}
func (s *Server) handleSMSLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string `json:"id"`
		Captcha string `json:"captcha"`
	}
	if !decodeJSON(w, r, &req, 4<<10) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	err := s.sms.login(ctx, req.ID, req.Captcha, func(revision string, result loginresult.Result, data []byte) error {
		result.CookiePath = filepath.Join(s.opts.Home, filepath.Base(result.AccountPath))
		_, err := s.config.updateAccountWithCookieContext(ctx, revision, result, data)
		return err
	})
	if err != nil {
		s.writeSMSError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]string{"message": "短信登录成功，账号已保存"})
}
func (s *Server) writeSMSError(w http.ResponseWriter, err error) {
	status, message := 500, "短信登录未完成，请稍后重试"
	var safe *smsLoginError
	if errors.As(err, &safe) {
		status, message = safe.status, safe.message
		if safe.retryAfter > 0 {
			w.Header().Set("Retry-After", fmt.Sprint(safe.retryAfter))
		}
	}
	if errors.Is(err, errConfigRevisionConflict) {
		status, message = 409, "账号配置已变化，请刷新后重新登录"
	}
	if s.opts.Output != nil {
		s.opts.Output("[webui] 短信登录未完成：状态=%d，错误类型=%T\n", status, err)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeError(w, status, message)
}
