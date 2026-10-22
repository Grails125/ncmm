package ncmm

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/pkg/database"
	"github.com/3899/ncmm/pkg/log"
)

func newMusicianAccountCacheDB(t *testing.T) database.Database {
	t.Helper()
	db, err := database.NewWithOptions(&database.Config{Driver: "badger", Path: filepath.Join(t.TempDir(), "badger")}, 1, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(context.Background()) })
	return db
}

func TestMusicianIdentityDoesNotTrustLegacyCookiePathCache(t *testing.T) {
	ctx := context.Background()
	db := newMusicianAccountCacheDB(t)
	f := newMusicianVipClaimFixture(t)
	path := filepath.Join(t.TempDir(), "cookie.json")
	if err := db.Set(ctx, "musician:identity:"+path, "0"); err != nil {
		t.Fatal(err)
	}
	_, hit, err := f.command.checkMusicianIdentityCache(ctx, db, 20002)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("a newly verified account must not inherit a legacy Cookie-path identity record")
	}
}

func TestMusicianVipClaimDoesNotTrustLegacyCookiePathSuccess(t *testing.T) {
	f := newMusicianVipClaimFixture(t)
	f.ctx.db = newMusicianAccountCacheDB(t)
	path := filepath.Join(t.TempDir(), "cookie.json")
	f.command.root.Cfg.Accounts.AntiCheatTokens[path] = "synthetic-token"
	key := fmt.Sprintf("musician:vip:claim:%s:%d", path, f.info.Data.FurtherVipGetTime)
	if err := f.ctx.db.Set(context.Background(), key, "1"); err != nil {
		t.Fatal(err)
	}
	f.run(path)
	if f.infoCalls != 1 || f.claimCalls != 1 {
		t.Fatalf("legacy path success blocked the current account: info=%d claim=%d", f.infoCalls, f.claimCalls)
	}
}

type musicianAccountInitFixture struct {
	command    *Musician
	path       string
	userCalls  int
	roleCalls  int
	userBody   string
	userError  error
	roleUserID int64
	isMusician bool
	tokens     map[string]int64
}

func newMusicianAccountInitFixture(t *testing.T) *musicianAccountInitFixture {
	t.Helper()
	weapi.ClearUserInfoCache()
	t.Cleanup(func() { weapi.ClearUserInfoCache() })
	home := t.TempDir()
	cfg, err := newDefaultRuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := normalizeRuntimePaths(cfg, home); err != nil {
		t.Fatal(err)
	}
	cfg.Network.Cookie.ReadOnly = true
	configPath := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(configPath, config.DefaultYAML(), 0600); err != nil {
		t.Fatal(err)
	}
	f := &musicianAccountInitFixture{path: cfg.Accounts.Main, isMusician: true, tokens: make(map[string]int64)}
	f.command = NewMusician(&Root{Cfg: cfg, CfgPath: configPath, Opts: RootOpts{Home: home}}, log.Default)
	var output bytes.Buffer
	f.command.Command().SetOut(&output)
	f.command.Command().SetErr(&output)
	f.command.newClient = func(network *api.Config, logger *log.Logger) (*api.Client, error) {
		client, err := api.NewClient(network, logger)
		if err != nil {
			return nil, err
		}
		uid := f.tokens[client.MusicU()]
		client.GetClient().Transport = musicianVipClaimTransport(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case "/weapi/w/nuser/account/get":
				f.userCalls++
				if f.userError != nil {
					return nil, f.userError
				}
				if f.userBody != "" {
					return giftJSONResponse(request, f.userBody), nil
				}
				return giftJSONResponse(request, fmt.Sprintf(`{"code":200,"account":{"id":%d},"profile":{"userId":%d}}`, uid, uid)), nil
			case "/eapi/nmusician/workbench/musician/role/get":
				f.roleCalls++
				roleUID := f.roleUserID
				if roleUID == 0 {
					roleUID = uid
				}
				return giftJSONResponse(request, fmt.Sprintf(`{"code":200,"data":{"userId":%d,"artistId":42,"isMusician":%t}}`, roleUID, f.isMusician)), nil
			default:
				t.Fatalf("unexpected musician initialization request: %s", request.URL.Path)
				return nil, nil
			}
		})
		return client, nil
	}
	return f
}

func (f *musicianAccountInitFixture) login(t *testing.T, uid int64, tokenSuffix string) {
	t.Helper()
	incoming := filepath.Join(f.command.root.Opts.Home, "incoming.json")
	token := fmt.Sprintf("synthetic-%d-%s", uid, tokenSuffix)
	f.tokens[token] = uid
	writeMusicianTestCookie(t, incoming, token)
	login := NewLogin(f.command.root, nil)
	login.isMain = true
	var output bytes.Buffer
	login.Command().SetOut(&output)
	if err := login.saveLoginResult(context.Background(), "合成账号", "", uid, incoming, "", ""); err != nil {
		t.Fatal(err)
	}
}

func writeMusicianTestCookie(t *testing.T, path, token string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	// 使用独立目标文件构造与真实登录相同的 Cookie jar 序列化格式。
	var network api.Config
	network.Cookie.Filepath = path
	client, err := api.NewClient(&network, log.Default)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("https://music.163.com")
	client.GetClient().Jar.SetCookies(origin, []*http.Cookie{{Name: "MUSIC_U", Value: token, Domain: ".music.163.com", Path: "/"}})
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (f *musicianAccountInitFixture) run(t *testing.T, path string) error {
	t.Helper()
	mctx, err := f.command.initMusicianContext(context.Background(), path, false)
	if mctx != nil {
		mctx.close(context.Background())
	}
	return err
}

func TestMusicianIdentitySwitchingUIDDoesNotInheritNegativeCache(t *testing.T) {
	f := newMusicianAccountInitFixture(t)
	f.isMusician = false
	f.login(t, 10001, "old-user")
	if err := f.run(t, f.path); err == nil {
		t.Fatal("non-musician fixture must be rejected")
	}
	f.isMusician = true
	f.login(t, 20002, "new-user")
	if err := f.run(t, f.path); err != nil {
		t.Fatalf("new UID inherited the previous account's identity: %v", err)
	}
	if f.userCalls != 2 || f.roleCalls != 2 {
		t.Fatalf("each account must verify its own identity: user=%d role=%d", f.userCalls, f.roleCalls)
	}
}

func TestMusicianIdentitySameUIDRenewsCookieAndMovesPathWithoutLosingCache(t *testing.T) {
	for _, days := range []int{0, 2, -1} {
		t.Run(fmt.Sprintf("cache_days_%d", days), func(t *testing.T) {
			f := newMusicianAccountInitFixture(t)
			f.command.root.Cfg.Musician.IdentityCacheDays = &days
			f.login(t, 10001, "first-cookie")
			if err := f.run(t, f.path); err != nil {
				t.Fatal(err)
			}
			f.login(t, 10001, "renewed-cookie")
			moved := filepath.Join(f.command.root.Opts.Home, "another.json")
			data, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(moved, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := f.run(t, moved); err != nil {
				t.Fatal(err)
			}
			wantRoles := 1
			if days == -1 {
				wantRoles = 2
			}
			if f.userCalls != 2 || f.roleCalls != wantRoles {
				t.Fatalf("UID cache lifetime changed: user=%d role=%d want_role=%d", f.userCalls, f.roleCalls, wantRoles)
			}
		})
	}
}

func TestMusicianIdentityExternalCookieReplacementAlsoUsesNewUID(t *testing.T) {
	f := newMusicianAccountInitFixture(t)
	f.isMusician = false
	f.login(t, 10001, "old-user")
	if err := f.run(t, f.path); err == nil {
		t.Fatal("non-musician fixture must be rejected")
	}
	f.isMusician = true
	token := "synthetic-external-replacement"
	f.tokens[token] = 20002
	writeMusicianTestCookie(t, f.path, token)
	if err := f.run(t, f.path); err != nil {
		t.Fatalf("external replacement inherited the previous account identity: %v", err)
	}
	if f.userCalls != 2 || f.roleCalls != 2 {
		t.Fatalf("replacement did not reverify UID: user=%d role=%d", f.userCalls, f.roleCalls)
	}
}

func TestMusicianIdentityUnverifiedUIDDoesNotTrustExistingRecords(t *testing.T) {
	for _, body := range []string{
		`{"code":403}`,
		`{"code":200,"account":{"id":0},"profile":{}}`,
		`{"code":200,"account":{"id":10001}}`,
		`{"code":200,"account":{"id":10001},"profile":{"userId":20002}}`,
	} {
		t.Run(body, func(t *testing.T) {
			f := newMusicianAccountInitFixture(t)
			f.login(t, 10001, "synthetic-cookie")
			db, err := database.NewWithOptions(f.command.root.Cfg.Database, 1, 0, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"musician:identity:" + f.path, musicianIdentityCacheKey(10001)} {
				if err := db.Set(context.Background(), key, "1"); err != nil {
					t.Fatal(err)
				}
			}
			_ = db.Close(context.Background())
			f.userBody = body
			if err := f.run(t, f.path); err == nil {
				t.Fatal("unverified UID must not use an existing identity record")
			}
			if f.userCalls != 1 || f.roleCalls != 0 {
				t.Fatalf("unverified UID continued to musician APIs: user=%d role=%d", f.userCalls, f.roleCalls)
			}
		})
	}
}

func TestMusicianRoleUIDMismatchDoesNotWriteIdentity(t *testing.T) {
	f := newMusicianAccountInitFixture(t)
	f.login(t, 10001, "synthetic-cookie")
	f.roleUserID = 20002
	if err := f.run(t, f.path); err == nil {
		t.Fatal("another account's role must not be accepted")
	}
	f.roleUserID = 10001
	if err := f.run(t, f.path); err != nil {
		t.Fatal(err)
	}
	if f.roleCalls != 2 {
		t.Fatal("mismatched role was incorrectly cached")
	}
}

func TestMusicianIdentityUIDLookupFailureDoesNotUseCachedIdentity(t *testing.T) {
	f := newMusicianAccountInitFixture(t)
	f.login(t, 10001, "first-cookie")
	if err := f.run(t, f.path); err != nil {
		t.Fatal(err)
	}
	f.login(t, 10001, "renewed-cookie")
	f.userError = fmt.Errorf("synthetic account lookup unavailable")
	if err := f.run(t, f.path); err == nil {
		t.Fatal("failed UID validation must not fall back to an existing identity")
	}
	if f.roleCalls != 1 {
		t.Fatal("failed UID validation continued to musician APIs")
	}
}

func TestMusicianVipClaimSamePathDifferentUIDDoesNotInheritSuccess(t *testing.T) {
	db := newMusicianAccountCacheDB(t)
	first := newMusicianVipClaimFixture(t)
	first.ctx.db = db
	first.run("account-a.json")
	if first.claimCalls != 1 {
		t.Fatal("first account did not claim")
	}
	second := newMusicianVipClaimFixture(t)
	second.userID = 20002
	second.ctx.db = db
	second.info.Data.FurtherVipGetTime = first.info.Data.FurtherVipGetTime
	second.run("account-a.json")
	if second.infoCalls != 1 || second.claimCalls != 1 {
		t.Fatalf("replacement UID inherited old success: info=%d claim=%d", second.infoCalls, second.claimCalls)
	}
	// 同一个真实账号换 Cookie 路径或续期仍使用本周期的成功记录。
	renewed := newMusicianVipClaimFixture(t)
	renewed.userID = second.userID
	renewed.ctx.db = db
	renewed.info.Data.FurtherVipGetTime = first.info.Data.FurtherVipGetTime
	renewed.run("account-b.json")
	if renewed.infoCalls != 1 || renewed.claimCalls != 0 {
		t.Fatalf("same UID lost cycle deduplication: info=%d claim=%d", renewed.infoCalls, renewed.claimCalls)
	}
}

func TestMusicianVipClaimUnverifiedUIDDoesNotTrustSuccessOrSubmit(t *testing.T) {
	for _, response := range []string{`{"code":403}`, `{"code":200,"account":{"id":0},"profile":{}}`, `{"code":200,"account":{"id":10001},"profile":{"userId":20002}}`} {
		t.Run(response, func(t *testing.T) {
			f := newMusicianVipClaimFixture(t)
			f.userBody = response
			key := musicianVipClaimCacheKey(f.userID, f.info.Data.FurtherVipGetTime)
			f.db.values[key] = "1"
			f.run("account-a.json")
			if f.userCalls != 1 || f.infoCalls != 0 || f.claimCalls != 0 || len(f.db.values) != 1 || strings.Contains(f.output.String(), "已领取") {
				t.Fatalf("unverified account trusted a success record or made writes: user=%d info=%d claim=%d", f.userCalls, f.infoCalls, f.claimCalls)
			}
		})
	}
}

func TestMusicianVipClaimUIDLookupFailureDoesNotReadSuccess(t *testing.T) {
	f := newMusicianVipClaimFixture(t)
	f.userError = fmt.Errorf("synthetic account lookup unavailable")
	f.db.values[musicianVipClaimCacheKey(f.userID, f.info.Data.FurtherVipGetTime)] = "1"
	f.run("account-a.json")
	if f.userCalls != 1 || f.infoCalls != 0 || f.claimCalls != 0 || strings.Contains(f.output.String(), "已领取") {
		t.Fatal("failed UID validation must not assume cached success or submit a claim")
	}
}
