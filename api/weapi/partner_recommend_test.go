package weapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/log"
)

func TestPartnerRecommendResourcesTransport(t *testing.T) {
	client, err := api.NewClient(&api.Config{Cookie: cookie.Config{Filepath: filepath.Join(t.TempDir(), "cookie.json")}}, log.Default)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	calls := 0
	client.GetClient().Transport = partnerTestTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodPost || req.URL.Scheme != "https" || req.URL.Host != "interface.music.163.com" || req.URL.Path != "/weapi/music/partner/rcmd/resource/list" || req.Referer() != "https://mp.music.163.com/" {
			t.Fatalf("推荐列表传输错误: %s %s", req.Method, req.URL)
		}
		if err := req.ParseForm(); err != nil || req.PostForm.Get("params") == "" || req.PostForm.Get("encSecKey") == "" {
			t.Fatal("推荐列表必须沿用已有 WEAPI 加密")
		}
		body := `{"code":200,"data":{"resources":[{"work":{"id":123,"resourceId":456,"name":"测试作品"},"publishEvent":true,"publishComment":true,"finishTaskNum":2,"totalTaskNum":3,"relatedTopics":[{"topicId":"13827903","topicName":"测试话题"}]}],"cursor":"","hasMore":false,"rcmdScore":3}}`
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	reply, err := New(client).PartnerRecommendResources(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || reply.Code != 200 || len(reply.Data.Resources) != 1 || reply.Data.RcmdScore != 3 {
		t.Fatalf("推荐响应解析错误: %+v", reply)
	}
	item := reply.Data.Resources[0]
	if !item.PublishEvent || !item.PublishComment || item.FinishTaskNum != 2 || item.TotalTaskNum != 3 || item.RelatedTopics[0].TopicID.String() != "13827903" {
		t.Fatalf("丢失推荐任务完成标记或话题: %+v", item)
	}
}

func TestPartnerRecommendResourcePreservesDailyCompatibility(t *testing.T) {
	var reply PartnerTaskResp
	if err := json.Unmarshal([]byte(`{"code":200,"data":{"recResources":[{"work":{"id":123,"resourceId":456},"canInteract":true,"publishEvent":true,"publishComment":true,"relatedTopics":[{"topicId":13827903,"topicName":"话题"}]}]}}`), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Data.RecResources) != 1 || !reply.Data.RecResources[0].CanInteract || reply.Data.RecResources[0].RelatedTopics[0].TopicID.String() != "13827903" {
		t.Fatal("每日推荐资源模型应兼容原有字段和数字话题 ID")
	}
	var reported PartnerExtraReportResp
	if err := json.Unmarshal([]byte(`{"code":200,"data":{"interactResult":true,"failedReason":"发布成功，积分+1"}}`), &reported); err != nil || !reported.Data.InteractResult || reported.DataNull {
		t.Fatal("成功提示非空不能覆盖 interactResult 成功标记")
	}
}
