package weapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/pkg/cookie"
)

func TestResourceCommentWebProtocol(t *testing.T) {
	c, err := api.NewClient(&api.Config{Retry: 3, Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	var paths []string
	c.GetClient().Transport = partnerTestTransport(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		_ = r.ParseForm()
		if r.Host != "music.163.com" || r.Form.Get("params") == "" || len(r.Form.Get("encSecKey")) != 256 {
			t.Fatal("expected webpage WEAPI request")
		}
		if r.Header.Get("X-antiCheatToken") != "" {
			t.Fatal("web checkToken mixed into mobile header")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":200,"comment":{"commentId":987}}`)), Request: r}, nil
	})
	a := New(c)
	added, err := a.ResourceCommentAdd(context.Background(), &ResourceCommentReq{ThreadID: "R_VI_62_abcdef", Content: "test-only", CheckToken: "web-only"})
	if err != nil || added.Comment.CommentID != 987 {
		t.Fatalf("response %v %v", added, err)
	}
	if _, err := a.ResourceCommentDelete(context.Background(), &ResourceCommentReq{ThreadID: "R_VI_62_abcdef", CommentID: 987}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != "/weapi/resource/comments/add,/weapi/resource/comments/delete" {
		t.Fatalf("paths=%v", paths)
	}
	payload, _ := json.Marshal(ResourceCommentReq{ThreadID: "R_VI_62_abcdef", Content: "test", CheckToken: "web-only"})
	if !strings.Contains(string(payload), `"checkToken":"web-only"`) {
		t.Fatal("web checkToken body field missing")
	}
}

func TestResourceCommentDoesNotRetryAmbiguousTransportFailure(t *testing.T) {
	c, err := api.NewClient(&api.Config{Retry: 3, Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	requests := 0
	c.GetClient().Transport = partnerTestTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("private-request-detail")
	})
	_, err = New(c).ResourceCommentAdd(context.Background(), &ResourceCommentReq{ThreadID: "R_VI_62_abcdef", Content: "test", CheckToken: "private-web-token"})
	if err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe error: %v", err)
	}
	if requests != 1 {
		t.Fatalf("comment retried %d times", requests)
	}
}
