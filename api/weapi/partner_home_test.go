package weapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/pkg/cookie"
)

func TestPartnerHomeUsesSensitiveProtocolAndNullablePoints(t *testing.T) {
	client, err := api.NewClient(&api.Config{Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	u, _ := url.Parse("https://music.163.com")
	client.SetCookies(u, []*http.Cookie{{Name: "MUSIC_U", Value: "test-home-session"}, {Name: "__csrf", Value: "test-home-csrf"}})
	status := http.StatusOK
	body := `{"code":200,"data":{"period":0,"week":2,"startDate":null,"integral":{"integral":0,"currentWeekIntegral":null}}}`
	calls := 0
	client.GetClient().Transport = partnerTestTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method != http.MethodPost || request.URL.Path != "/weapi/music/partner/home/get" || request.Referer() != "https://mp.music.163.com/" || request.URL.Query().Get("csrf_token") != "test-home-csrf" {
			t.Error("incorrect home request protocol")
		}
		if session, err := request.Cookie("MUSIC_U"); err != nil || session.Value != "test-home-session" {
			t.Error("home request lost selected account session")
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})
	partner := New(client)
	response, err := partner.PartnerHome(context.Background(), &PartnerHomeReq{})
	if err != nil || response == nil {
		t.Fatal("home request failed")
	}
	data := response.Data
	if data.Period == nil || *data.Period != 0 || data.Week == nil || *data.Week != 2 || data.StartDate != nil || data.EndDate != nil || data.Integral.Integral == nil || *data.Integral.Integral != 0 || data.Integral.CurrentWeekIntegral != nil {
		t.Fatal("nullable home fields lost zero or null semantics")
	}
	status = http.StatusForbidden
	body = `{"code":703,"message":"test-private-home-error","csrf_token":"test-private-home-error"}`
	response, err = partner.PartnerHome(context.Background(), &PartnerHomeReq{})
	var statusErr *api.HTTPStatusError
	if response != nil || !errors.As(err, &statusErr) || statusErr.StatusCode != 403 || statusErr.ServiceCode != 703 || strings.Contains(err.Error(), "test-private-home-error") {
		t.Fatal("home request did not use safe HTTP diagnostics")
	}
	if calls != 2 {
		t.Fatal("home request retried unexpectedly")
	}
}
