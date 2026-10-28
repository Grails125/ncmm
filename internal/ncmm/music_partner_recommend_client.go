package ncmm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/types"
	"github.com/3899/ncmm/api/weapi"
)

type partnerRecommendClient struct {
	*weapi.Api
	publish *eapi.Api
	uid     int64
	media   *http.Client
	token   string
	mode    api.CryptoMode
}

// 发布协议在首次写入前确定，不在结果未知时切换协议重发。
func partnerRecommendPublishMode(client *api.Client, network *api.Config) (api.CryptoMode, error) {
	platform := vipMemberGiftPlatformFromText(vipMemberGiftCookieValue(client, "os"))
	if platform == "" && network != nil {
		platform = vipMemberGiftPlatformFromText(network.UserAgent.XEAPI)
	}
	if platform == "" && network != nil {
		platform = vipMemberGiftPlatformFromText(network.UserAgent.EAPI)
	}
	switch platform {
	case "ios":
		return api.CryptoModeEAPI, nil
	case "android":
		if strings.TrimSpace(client.UserAgent(api.CryptoModeXEAPI)) == "" {
			return "", fmt.Errorf("Android 推荐发布需要填写 network.user_agent.xeapi，尚未发出发布请求")
		}
		return api.CryptoModeXEAPI, nil
	default:
		return "", fmt.Errorf("无法确定推荐发布的移动端协议，请检查账号会话与移动端 UA，尚未发出发布请求")
	}
}

// 媒体请求独立于账号会话，不向图片或音频 CDN 发送登录 Cookie。
func partnerMediaClient(client *api.Client) *http.Client {
	return &http.Client{Transport: client.GetClient().Transport, Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !partnerMediaURL(req.URL) {
			return fmt.Errorf("媒体重定向不在允许的网易云域名内")
		}
		return nil
	}}
}

func partnerMediaURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return strings.HasSuffix(host, ".music.126.net") || strings.HasSuffix(host, ".music.163.com") || host == "music.163.com"
}

// 通过网易云 CDN 获取适合动态的封面尺寸，避免原始大图在图片处理接口超时。
func partnerRecommendCoverURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("歌曲封面地址无效")
	}
	if u.Scheme == "http" {
		u.Scheme = "https"
	}
	if !partnerMediaURL(u) {
		return "", fmt.Errorf("歌曲封面地址不在允许的网易云域名内")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", fmt.Errorf("歌曲封面地址参数无效")
	}
	query.Set("param", "1080y1080")
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (c *partnerRecommendClient) mediaGet(ctx context.Context, raw string) (*http.Response, error) {
	u, err := url.Parse(raw)
	if err == nil && u.Scheme == "http" {
		u.Scheme = "https"
	}
	if err != nil || !partnerMediaURL(u) {
		return nil, fmt.Errorf("媒体地址无效或不在允许的网易云域名内")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("无法创建媒体请求")
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		response, err := c.media.Do(req)
		if err == nil && response.StatusCode == http.StatusOK {
			return response, nil
		}
		status := 0
		if response != nil {
			status = response.StatusCode
			response.Body.Close()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		kind, retry := partnerMediaFailureKind(err, status)
		if retry && attempt < 2 {
			// 只重试无账号凭据的只读 CDN GET；发布和互动上报不走此路径。
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(250 * time.Millisecond):
				continue
			}
		}
		failure := fmt.Errorf("获取媒体失败（CDN=%s，分类=%s，HTTP=%d，尝试=%d/2）", u.Hostname(), kind, status, attempt)
		if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests {
			return nil, errors.Join(failure, musicPartnerHTTPError("获取媒体", &api.HTTPStatusError{StatusCode: status}))
		}
		return nil, failure
	}
	return nil, fmt.Errorf("获取媒体失败")
}

// 原始网络错误通常带有签名 URL；只输出受控分类和已验证的 CDN 域名。
func partnerMediaFailureKind(err error, status int) (string, bool) {
	var dns *net.DNSError
	var network net.Error
	var connection *net.OpError
	var certificate *tls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	var record tls.RecordHeaderError
	switch {
	case errors.As(err, &dns):
		return "域名解析失败", dns.Timeout() || dns.Temporary()
	case errors.As(err, &certificate), errors.As(err, &authority), errors.As(err, &invalid), errors.As(err, &hostname), errors.As(err, &record):
		return "TLS 证书或握手失败", false
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &network) && network.Timeout():
		return "网络请求超时", true
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.As(err, &connection):
		return "连接中断", true
	case status >= 500 && status <= 599:
		return "CDN 暂时不可用", true
	case status > 0:
		return "HTTP 拒绝或资源不可用", false
	default:
		return "请求或重定向失败", false
	}
}

func (c *partnerRecommendClient) PrepareAudio(ctx context.Context, work weapi.PartnerWork) error {
	audioURL := work.PlayUrl
	if audioURL == "" {
		response, err := c.SongPlayerV1(ctx, &weapi.SongPlayerV1Req{Ids: types.IntsString([]int64{work.ResourceId}), Level: types.LevelStandard})
		if err != nil {
			return partnerRecommendRequestError(ctx, "读取推荐音频", err)
		}
		if response == nil {
			return fmt.Errorf("未取得可用的推荐歌曲音频")
		}
		if response.Code != 200 {
			return musicPartnerResponseError("读取推荐音频", response.Code, "")
		}
		for _, item := range response.Data {
			if item.Id == work.ResourceId {
				audioURL = item.Url
				break
			}
		}
	}
	response, err := c.mediaGet(ctx, audioURL)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	kind := strings.ToLower(response.Header.Get("Content-Type"))
	if !strings.HasPrefix(kind, "audio/") && !strings.HasPrefix(kind, "application/octet-stream") {
		return fmt.Errorf("推荐音频返回了非音频数据")
	}
	count, err := io.Copy(io.Discard, io.LimitReader(response.Body, 32*1024*1024+1))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || count == 0 || count > 32*1024*1024 {
		return fmt.Errorf("推荐音频不完整或超过大小限制")
	}
	return nil
}

func (c *partnerRecommendClient) PrepareNote(ctx context.Context, item weapi.PartnerRecommendResource, content string) (*eapi.PartnerNotePublishReq, error) {
	coverURL, err := partnerRecommendCoverURL(item.Work.CoverUrl)
	if err != nil {
		return nil, err
	}
	response, err := c.mediaGet(ctx, coverURL)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 10*1024*1024+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(data) == 0 || len(data) > 10*1024*1024 || !strings.HasPrefix(http.DetectContentType(data), "image/") {
		return nil, fmt.Errorf("歌曲封面不是有效图片或超过大小限制")
	}
	file, err := os.CreateTemp("", "ncmm-partner-cover-*")
	if err != nil {
		return nil, fmt.Errorf("无法准备歌曲封面")
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return nil, fmt.Errorf("歌曲封面写入失败")
	}
	pics, err := c.publish.EventUploadImage(ctx, file.Name())
	if err != nil {
		return nil, partnerRecommendRequestError(ctx, "上传歌曲封面", err)
	}
	title := []rune("推荐《" + item.Work.Name + "》")
	if len(title) > 100 {
		title = title[:100]
	}
	topics := make([]eapi.PartnerNoteTopic, 0, len(item.RelatedTopics))
	for _, topic := range item.RelatedTopics {
		if id, err := strconv.ParseInt(string(topic.TopicID), 10, 64); err == nil && id > 0 && topic.TopicName != "" {
			topics = append(topics, eapi.PartnerNoteTopic{ID: string(topic.TopicID), Name: topic.TopicName, Type: 2})
		}
	}
	return &eapi.PartnerNotePublishReq{SongID: item.Work.ResourceId, UserID: c.uid, Title: string(title), Content: content, Pics: pics, Topics: topics, CheckToken: c.token, CryptoMode: c.mode}, nil
}

func (c *partnerRecommendClient) PublishNote(ctx context.Context, request *eapi.PartnerNotePublishReq) (int64, error) {
	reply, err := c.publish.PartnerNotePublish(ctx, request)
	if err != nil {
		diagnostic := partnerRecommendRequestError(ctx, "发布推荐笔记", err)
		if reply != nil {
			if hint := partnerRecommendResponseHint(reply.Message, reply.Msg, reply.ErrDetail); hint != "" {
				diagnostic = fmt.Errorf("%w；接口提示：%s", diagnostic, hint)
			}
		}
		return 0, diagnostic
	}
	if reply != nil && reply.Code != 200 {
		return 0, musicPartnerResponseError("发布推荐笔记", int64(reply.Code), reply.Message)
	}
	return reply.ConfirmedEventID()
}

func (c *partnerRecommendClient) PublishComment(ctx context.Context, item weapi.PartnerRecommendResource, content string) (int64, error) {
	reply, err := c.publish.PartnerCommentAdd(ctx, &eapi.PartnerCommentReq{ResourceID: item.Work.ResourceId, Content: content, CheckToken: c.token, CryptoMode: c.mode})
	if err != nil {
		diagnostic := partnerRecommendRequestError(ctx, "发布推荐乐评", err)
		if reply != nil {
			if hint := partnerRecommendResponseHint(reply.Message, reply.Msg, reply.ErrDetail); hint != "" {
				diagnostic = fmt.Errorf("%w；接口提示：%s", diagnostic, hint)
			}
		}
		return 0, diagnostic
	}
	if reply == nil {
		return 0, fmt.Errorf("乐评发布回执为空")
	}
	if reply.Code != 200 {
		return 0, musicPartnerResponseError("发布推荐乐评", int64(reply.Code), reply.Message)
	}
	if reply.Comment.CommentID <= 0 {
		return 0, fmt.Errorf("乐评发布缺少有效回执")
	}
	return reply.Comment.CommentID, nil
}

func partnerRecommendResponseHint(message, msg string, detail json.RawMessage) string {
	parts := []string{message, msg}
	var text string
	if json.Unmarshal(detail, &text) == nil {
		parts = append(parts, text)
	} else {
		var value struct {
			Message string `json:"message"`
			Msg     string `json:"msg"`
			Reason  string `json:"reason"`
		}
		if json.Unmarshal(detail, &value) == nil {
			parts = append(parts, value.Message, value.Msg, value.Reason)
		}
	}
	return musicPartnerSafeText(strings.Join(parts, " "))
}

func (c *partnerRecommendClient) DeleteNote(ctx context.Context, id int64) error {
	result, err := c.publish.EventDelete(ctx, &eapi.EventDeleteReq{Id: id})
	if err != nil {
		return partnerRecommendRequestError(ctx, "删除推荐笔记", err)
	}
	if result == nil {
		return fmt.Errorf("删除推荐笔记响应为空")
	}
	if result.Code != 200 {
		return musicPartnerResponseError("删除推荐笔记", result.Code, result.Message+result.Msg)
	}
	return nil
}

func (c *partnerRecommendClient) DeleteComment(ctx context.Context, resourceID, commentID int64) error {
	result, err := c.publish.PartnerCommentDelete(ctx, &eapi.PartnerCommentDeleteReq{ResourceID: resourceID, CommentID: commentID})
	if err != nil {
		return partnerRecommendRequestError(ctx, "删除推荐乐评", err)
	}
	if result == nil {
		return fmt.Errorf("删除推荐乐评响应为空")
	}
	if result.Code != 200 {
		return musicPartnerResponseError("删除推荐乐评", result.Code, result.Message)
	}
	return nil
}

// 只保留稳定的状态码分类，不把带签名 URL、Cookie 或原始响应写入任务日志。
func partnerRecommendRequestError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var imageUpload *eapi.ImageUploadError
	if errors.As(err, &imageUpload) {
		if imageUpload.HTTPStatus == http.StatusUnauthorized || imageUpload.HTTPStatus == http.StatusForbidden || imageUpload.HTTPStatus == http.StatusTooManyRequests {
			return errors.Join(imageUpload, musicPartnerHTTPError(operation, &api.HTTPStatusError{StatusCode: imageUpload.HTTPStatus, ServiceCode: imageUpload.ServiceCode}))
		}
		if imageUpload.ServiceCode > 0 && imageUpload.ServiceCode != 200 {
			return errors.Join(imageUpload, musicPartnerResponseError(operation, imageUpload.ServiceCode, ""))
		}
		return fmt.Errorf("%s：%w", operation, imageUpload)
	}
	var publication *eapi.PartnerPublishError
	if errors.As(err, &publication) {
		if publication.HTTPStatus == http.StatusUnauthorized || publication.HTTPStatus == http.StatusForbidden || publication.HTTPStatus == http.StatusTooManyRequests {
			return errors.Join(publication, musicPartnerHTTPError(operation, &api.HTTPStatusError{StatusCode: publication.HTTPStatus, ServiceCode: publication.ServiceCode}))
		}
		if publication.ServiceCode > 0 && publication.ServiceCode != 200 {
			return errors.Join(publication, musicPartnerResponseError(operation, publication.ServiceCode, ""))
		}
		return fmt.Errorf("%s：%w", operation, publication)
	}
	var status *api.HTTPStatusError
	if errors.As(err, &status) {
		if status.StatusCode == http.StatusUnauthorized || status.StatusCode == http.StatusForbidden || status.StatusCode == http.StatusTooManyRequests {
			return musicPartnerHTTPError(operation, status)
		}
		if status.ServiceCode > 0 {
			return musicPartnerResponseError(operation, int64(status.ServiceCode), "")
		}
	}
	return fmt.Errorf("%s请求失败（错误类型 %T）", operation, err)
}
