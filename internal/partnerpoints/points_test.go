package partnerpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/pkg/cookie"
)

type fakeReader func(context.Context, *weapi.PartnerHomeReq) (*weapi.PartnerHomeResp, error)

func (f fakeReader) PartnerHome(ctx context.Context, req *weapi.PartnerHomeReq) (*weapi.PartnerHomeResp, error) {
	return f(ctx, req)
}

func TestReadPreservesZeroAndUnavailableFields(t *testing.T) {
	for _, test := range []struct {
		name, payload    string
		unavailable      bool
		integral, weekly *int64
	}{
		{"both_zero", `{"code":200,"data":{"period":73,"week":2,"integral":{"integral":0,"currentWeekIntegral":0}}}`, false, pointer(0), pointer(0)},
		{"period_only", `{"code":200,"data":{"integral":{"integral":118,"currentWeekIntegral":null}}}`, false, pointer(118), nil},
		{"weekly_only", `{"code":200,"data":{"integral":{"currentWeekIntegral":61}}}`, false, nil, pointer(61)},
		{"null_data", `{"code":200,"data":null}`, true, nil, nil},
		{"missing_integral", `{"code":200,"data":{}}`, true, nil, nil},
		{"null_integral", `{"code":200,"data":{"integral":null}}`, true, nil, nil},
		{"null_points", `{"code":200,"data":{"integral":{"integral":null,"currentWeekIntegral":null}}}`, true, nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var response weapi.PartnerHomeResp
			if err := json.Unmarshal([]byte(test.payload), &response); err != nil {
				t.Fatal(err)
			}
			before := time.Now()
			got, err := Read(context.Background(), fakeReader(func(context.Context, *weapi.PartnerHomeReq) (*weapi.PartnerHomeResp, error) { return &response, nil }))
			if test.unavailable {
				if err == nil || got != nil {
					t.Fatal("unavailable points became a zero snapshot")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !samePointerValue(got.Integral, test.integral) || !samePointerValue(got.CurrentWeekIntegral, test.weekly) || got.UpdatedAt.Before(before) || got.UpdatedAt.After(time.Now()) {
				t.Fatal("incorrect points snapshot")
			}
			if got.StartDate != nil || got.EndDate != nil {
				t.Fatal("missing dates became zero")
			}
			if test.name == "both_zero" && (got.Period == nil || *got.Period != 73 || got.Week == nil || *got.Week != 2) {
				t.Fatal("period metadata lost")
			}
			encoded, err := json.Marshal(got)
			if err != nil || !strings.Contains(string(encoded), `"updatedAt"`) || !strings.Contains(string(encoded), `"startDate":null`) {
				t.Fatal("snapshot JSON lost nullable contract")
			}
		})
	}
}

func TestReadReturnsSafeErrors(t *testing.T) {
	for _, test := range []struct {
		name     string
		response *weapi.PartnerHomeResp
		err      error
		code     int64
	}{
		{"not_partner", homeResponse(t, `{"code":703,"message":"MUSIC_U=test-private-message"}`), nil, 703},
		{"rate_limit", homeResponse(t, `{"code":405,"message":"csrf_token=test-private-message"}`), nil, 405},
		{"nil_response", nil, nil, 0},
		{"transport", nil, errors.New("https://example.invalid/?token=test-private-transport"), 0},
		{"http", nil, &api.HTTPStatusError{StatusCode: 403, ServiceCode: 405}, 405},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := Read(context.Background(), fakeReader(func(context.Context, *weapi.PartnerHomeReq) (*weapi.PartnerHomeResp, error) {
				return test.response, test.err
			}))
			if err == nil || got != nil || strings.Contains(err.Error(), "test-private") || strings.Contains(err.Error(), "https://") {
				t.Fatal("unsafe or missing error")
			}
			if test.code != 0 {
				var serviceErr *ServiceError
				if !errors.As(err, &serviceErr) || serviceErr.Code != test.code {
					t.Fatal("safe service code lost")
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	_, err := Read(ctx, fakeReader(func(context.Context, *weapi.PartnerHomeReq) (*weapi.PartnerHomeResp, error) {
		called = true
		return nil, nil
	}))
	if !errors.Is(err, context.Canceled) || called {
		t.Fatal("cancelled read performed a request")
	}
}

func TestNewReadClientIsIsolatedAndNeverWritesCookie(t *testing.T) {
	path := writeCookie(t, true, true)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, timeout := range []time.Duration{0, 5 * time.Second, time.Minute} {
		network := &api.Config{Debug: true, Retry: 3, Timeout: timeout, Cookie: cookie.Config{Filepath: "unrelated-original.json", Interval: time.Nanosecond}}
		client, err := NewReadClient(network, path)
		if err != nil {
			t.Fatal(err)
		}
		if client.GetClient().Timeout <= 0 || client.GetClient().Timeout > 15*time.Second || (timeout == 5*time.Second && client.GetClient().Timeout != timeout) {
			t.Fatal("unbounded timeout")
		}
		transport, ok := client.GetClient().Transport.(*http.Transport)
		if !ok || transport.TLSClientConfig == nil || transport.TLSClientConfig.InsecureSkipVerify {
			t.Fatal("TLS verification disabled")
		}
		u, _ := url.Parse("https://music.163.com/")
		client.SetCookies(u, []*http.Cookie{{Name: "MUSIC_U", Value: "test-in-memory-update", Path: "/"}})
		if err := client.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		current, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(original, current) {
			t.Fatal("read client modified source cookies")
		}
		if !network.Debug || network.Retry != 3 || network.Timeout != timeout || network.Cookie.ReadOnly || network.Cookie.Filepath != "unrelated-original.json" {
			t.Fatal("read client mutated shared network config")
		}
	}
}

func TestNewReadClientRejectsUnavailableCredentialsSafely(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "must-not-create")
	missing := filepath.Join(dir, "test-private-path.json")
	malformed := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(malformed, []byte(`test-private-malformed-cookie`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", missing, malformed, writeCookie(t, false, true)} {
		client, err := NewReadClient(&api.Config{}, path)
		if err == nil || client != nil || strings.Contains(err.Error(), "test-private") {
			t.Fatal("missing credentials accepted or leaked")
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("missing cookie directory created")
	}
	if client, err := NewReadClient(nil, missing); err == nil || client != nil {
		t.Fatal("nil network accepted")
	}
}

func writeCookie(t *testing.T, session, csrf bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cookie.json")
	jar, err := cookie.NewCookie(cookie.WithFilePath(path), cookie.WithSyncInterval(0))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("https://music.163.com/")
	var cookies []*http.Cookie
	if session {
		cookies = append(cookies, &http.Cookie{Name: "MUSIC_U", Value: "test-session", Path: "/"})
	}
	if csrf {
		cookies = append(cookies, &http.Cookie{Name: "__csrf", Value: "test-csrf", Path: "/"})
	}
	jar.SetCookies(u, cookies)
	if err := jar.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	return path
}

func homeResponse(t *testing.T, payload string) *weapi.PartnerHomeResp {
	t.Helper()
	var response weapi.PartnerHomeResp
	if err := json.Unmarshal([]byte(payload), &response); err != nil {
		t.Fatal(err)
	}
	return &response
}

func pointer(value int64) *int64 { return &value }
func samePointerValue(a, b *int64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
