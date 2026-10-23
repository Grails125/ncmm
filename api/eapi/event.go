// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

// Event (动态) API — 发送/删除动态
// Ported from https://github.com/XiaoMengXinX/Music163Api-Go
// Endpoints:
//   - /api/share/friends/resource (发送动态, EAPI)
//   - /api/event/delete (删除动态, EAPI)

package eapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/types"
	"github.com/3899/ncmm/pkg/utils"
)

// ===================== 发送动态 (EventPublish) =====================

// EventPublishReq 发送动态请求
type EventPublishReq struct {
	// Title 动态标题 (新版图文笔记支持标题)
	Title string `json:"title,omitempty"`
	// Msg 动态文本内容
	Msg string `json:"msg"`
	// Type 动态类型, 发纯文本用 "noresource"
	Type string `json:"type"`
	// Uuid 唯一标识 (32位hex), 为空时自动生成
	Uuid string `json:"uuid"`
	// Pics 图片信息JSON字符串, 由 EventUploadImage 生成; 无图片时留空
	Pics string `json:"pics,omitempty"`
	// AddComment 是否添加评论, 默认 false
	AddComment bool `json:"addComment"`
	// PrivacySetting 隐私设置
	PrivacySetting string `json:"privacySetting,omitempty"`
	// SocialSpaceVisible 是否可见空间 默认 1
	SocialSpaceVisible int `json:"socialSpaceVisible,omitempty"`
	// ActivityInfoList 活动信息列表JSON字符串 (乐迷团发布笔记时需要)
	// 格式: [{"id":"13827903","type":3,"subType":11,"name":"音乐合伙人的乐迷团","selected":true,"canChange":true}]
	ActivityInfoList string `json:"activityInfoList,omitempty"`
}

// EventPublishResp 发送动态响应
type EventPublishResp struct {
	types.RespCommon[any]
	UserId int   `json:"userId"`
	Id     int64 `json:"id"`
	Event  struct {
		DiscussId    string `json:"discussId"`
		ForwardCount int    `json:"forwardCount"`
		Json         string `json:"json"`
		Uuid         string `json:"uuid"`
		EventTime    int64  `json:"eventTime"`
	} `json:"event"`
}

// EventPublish 发送动态 (支持图片)
// 接口: /api/share/friends/resource
// 加密: EAPI
// 需要登录
func (a *Api) EventPublish(ctx context.Context, req *EventPublishReq) (*EventPublishResp, error) {
	// 自动生成 UUID
	if req.Uuid == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("generate uuid: %w", err)
		}
		req.Uuid = hex.EncodeToString(b)
	}
	if req.Type == "" {
		req.Type = "noresource"
	}
	if req.PrivacySetting == "" {
		req.PrivacySetting = "0"
	}
	if req.SocialSpaceVisible == 0 {
		req.SocialSpaceVisible = 1
	}

	var (
		url   = "https://interface3.music.163.com/eapi/note/share/friends/resource"
		reply EventPublishResp
		opts  = api.NewOptions()
	)
	opts.CryptoMode = api.CryptoModeEAPI

	resp, err := a.client.Request(ctx, url, req, &reply, opts)
	if err != nil {
		return nil, fmt.Errorf("Request: %w", err)
	}
	_ = resp
	return &reply, nil
}

// ===================== 删除动态 (EventDelete) =====================

// EventDeleteReq 删除动态请求
type EventDeleteReq struct {
	// Id 动态ID
	Id int64 `json:"id"`
}

// EventDeleteResp 删除动态响应
type EventDeleteResp struct {
	types.RespCommon[any]
}

// EventDelete 删除动态
// 接口: /api/event/delete
// 加密: EAPI
// 需要登录
func (a *Api) EventDelete(ctx context.Context, req *EventDeleteReq) (*EventDeleteResp, error) {
	if req == nil || req.Id <= 0 {
		return nil, fmt.Errorf("待删除动态 ID 无效")
	}
	var (
		url   = "https://interface3.music.163.com/eapi/event/delete"
		reply EventDeleteResp
		opts  = api.NewOptions()
	)
	opts.CryptoMode = api.CryptoModeEAPI
	opts.Sensitive = true
	opts.NoRetry = true

	resp, err := a.client.Request(ctx, url, req, &reply, opts)
	if err != nil {
		return nil, fmt.Errorf("Request: %w", err)
	}
	_ = resp
	return &reply, nil
}

// ===================== 图片上传 (Event) =====================

// eventImgPicInfo 事件图片信息 (用于动态图片参数)
type eventImgPicInfo struct {
	OriginId      string `json:"originId"`
	SquareId      string `json:"squareId"`
	RectangleId   string `json:"rectangleId"`
	PcSquareId    string `json:"pcSquareId"`
	PcRectangleId string `json:"pcRectangleId"`
	OriginJpgId   string `json:"originJpgId"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	Index         int    `json:"index"`
}

// eventNosTokenResp Nos Token 分配响应
type eventNosTokenResp struct {
	types.RespCommon[any]
	Result struct {
		Bucket    string `json:"bucket"`
		DocId     string `json:"docId"`
		ObjectKey string `json:"objectKey"`
		Token     string `json:"token"`
	} `json:"result"`
}

// eventUploadImgResp 上传事件图片信息响应
type eventUploadImgResp struct {
	types.RespCommon[any]
	PicSubtype string `json:"picSubtype"`
	PicInfo    struct {
		OriginId    int64  `json:"originId"`
		SquareId    int64  `json:"squareId"`
		RectangleId int64  `json:"rectangleId"`
		Format      string `json:"format"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
	} `json:"picInfo"`
}

// EventUploadImage 上传动态图片
// 完成三步操作: 获取nos token → 上传文件 → 获取图片信息
// 返回值为 EventPublishReq.Pics 所需的 JSON 字符串
func (a *Api) EventUploadImage(ctx context.Context, filePath string) (string, error) {
	// 读取文件
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", imageUploadFailure(ctx, "读取图片", "文件读取失败", 0, 0, err)
	}
	contentType, ext, err := eventImageFormat(data)
	if err != nil {
		return "", imageUploadFailure(ctx, "识别格式", "图片格式不支持", 0, 0, err)
	}

	md5, err := utils.MD5Hex(data)
	if err != nil {
		return "", imageUploadFailure(ctx, "计算摘要", "编码失败", 0, 0, err)
	}

	filename := filepath.Base(filePath)
	fileSize := int64(len(data))

	// Step 1: 获取 Nos Token
	var (
		tokenURL = "https://music.163.com/eapi/nos/token/alloc"
		tokenReq = struct {
			Filename   string `json:"filename"`
			Local      string `json:"local"`
			NosProduct int    `json:"nos_product"`
			FileSize   int64  `json:"fileSize"`
			Md5        string `json:"md5"`
			Ext        string `json:"ext"`
			Type       string `json:"type"`
		}{
			Filename:   filename,
			Local:      "false",
			NosProduct: 0,
			FileSize:   fileSize,
			Md5:        md5,
			Ext:        ext,
			Type:       "image",
		}
		tokenReply eventNosTokenResp
		tokenOpts  = api.NewOptions()
	)
	tokenOpts.CryptoMode = api.CryptoModeEAPI
	tokenOpts.Sensitive = true

	resp, err := a.client.Request(ctx, tokenURL, tokenReq, &tokenReply, tokenOpts)
	if err != nil {
		return "", imageUploadFailure(ctx, "分配凭据", "请求失败", 0, 0, err)
	}
	_ = resp

	if tokenReply.Code != 200 {
		return "", imageUploadFailure(ctx, "分配凭据", "服务拒绝", 200, tokenReply.Code, nil)
	}
	if tokenReply.Result.DocId == "" || tokenReply.Result.Token == "" || tokenReply.Result.Bucket == "" || tokenReply.Result.ObjectKey == "" {
		return "", imageUploadFailure(ctx, "分配凭据", "凭据缺失", 200, tokenReply.Code, nil)
	}

	// Step 2: 上传文件到 NOS
	uploadNode, err := a.getUploadNode(ctx, tokenReply.Result.Bucket)
	if err != nil {
		return "", imageUploadFailure(ctx, "查询节点", "请求失败", 0, 0, err)
	}

	uploadURL, err := eventImageUploadURL(uploadNode, tokenReply.Result.Bucket, tokenReply.Result.ObjectKey)
	if err != nil {
		return "", imageUploadFailure(ctx, "构造地址", "路径无效", 0, 0, err)
	}

	headers := map[string]string{
		"X-Nos-Token":  tokenReply.Result.Token,
		"Content-Type": contentType,
	}

	if err := a.rawUpload(ctx, uploadURL, headers, data); err != nil {
		return "", imageUploadFailure(ctx, "上传文件", "请求失败", 0, 0, err)
	}

	// Step 3: 获取事件图片信息
	var (
		imgURL = "https://music.163.com/eapi/upload/event/img/v1"
		imgReq = struct {
			ImgId  string `json:"imgid"`
			Format string `json:"format"`
		}{
			ImgId:  tokenReply.Result.DocId,
			Format: ext,
		}
		imgReply eventUploadImgResp
		imgOpts  = api.NewOptions()
	)
	imgOpts.CryptoMode = api.CryptoModeEAPI
	imgOpts.Sensitive = true

	resp, err = a.client.Request(ctx, imgURL, imgReq, &imgReply, imgOpts)
	if err != nil {
		return "", imageUploadFailure(ctx, "读取图片信息", "请求失败", 0, 0, err)
	}
	_ = resp

	if imgReply.Code != 200 {
		return "", imageUploadFailure(ctx, "读取图片信息", "服务拒绝", 200, imgReply.Code, nil)
	}
	if err := validateEventImageInfo(imgReply); err != nil {
		return "", imageUploadFailure(ctx, "读取图片信息", "元数据缺失", 200, imgReply.Code, err)
	}

	// 构建 pics JSON
	originIdStr := strconv.FormatInt(imgReply.PicInfo.OriginId, 10)
	squareIdStr := strconv.FormatInt(imgReply.PicInfo.SquareId, 10)
	rectangleIdStr := strconv.FormatInt(imgReply.PicInfo.RectangleId, 10)

	picInfo := eventImgPicInfo{
		OriginId:      originIdStr,
		SquareId:      squareIdStr,
		RectangleId:   rectangleIdStr,
		PcSquareId:    squareIdStr,
		PcRectangleId: rectangleIdStr,
		OriginJpgId:   originIdStr,
		Width:         imgReply.PicInfo.Width,
		Height:        imgReply.PicInfo.Height,
		Index:         0,
	}
	picsBytes, err := json.Marshal([]eventImgPicInfo{picInfo})
	if err != nil {
		return "", imageUploadFailure(ctx, "编码图片信息", "编码失败", 0, 0, err)
	}

	return string(picsBytes), nil
}

// EventUploadImages 批量上传动态图片
// 返回值为 EventPublishReq.Pics 所需的 JSON 字符串
func (a *Api) EventUploadImages(ctx context.Context, filePaths []string) (string, error) {
	var pics []eventImgPicInfo
	for _, fp := range filePaths {
		data, err := os.ReadFile(fp)
		if err != nil {
			return "", fmt.Errorf("read file %s: %w", fp, err)
		}
		contentType, ext, err := eventImageFormat(data)
		if err != nil {
			return "", err
		}

		md5, err := utils.MD5Hex(data)
		if err != nil {
			return "", fmt.Errorf("MD5Hex: %w", err)
		}

		filename := filepath.Base(fp)
		fileSize := int64(len(data))

		// 获取 Nos Token
		var (
			tokenURL = "https://music.163.com/eapi/nos/token/alloc"
			tokenReq = struct {
				Filename   string `json:"filename"`
				Local      string `json:"local"`
				NosProduct int    `json:"nos_product"`
				FileSize   int64  `json:"fileSize"`
				Md5        string `json:"md5"`
				Ext        string `json:"ext"`
				Type       string `json:"type"`
			}{
				Filename:   filename,
				Local:      "false",
				NosProduct: 0,
				FileSize:   fileSize,
				Md5:        md5,
				Ext:        ext,
				Type:       "image",
			}
			tokenReply eventNosTokenResp
			tokenOpts  = api.NewOptions()
		)
		tokenOpts.CryptoMode = api.CryptoModeEAPI
		tokenOpts.Sensitive = true

		if _, err := a.client.Request(ctx, tokenURL, tokenReq, &tokenReply, tokenOpts); err != nil {
			return "", fmt.Errorf("get nos token for %s: %w", fp, err)
		}
		if tokenReply.Code != 200 {
			return "", fmt.Errorf("get nos token failed for %s: code=%d", fp, tokenReply.Code)
		}
		if tokenReply.Result.DocId == "" || tokenReply.Result.Token == "" {
			return "", fmt.Errorf("NOS 未返回完整的图片上传凭据")
		}

		// 上传文件
		uploadNode, err := a.getUploadNode(ctx, tokenReply.Result.Bucket)
		if err != nil {
			return "", fmt.Errorf("get upload node: %w", err)
		}

		uploadURL, err := eventImageUploadURL(uploadNode, tokenReply.Result.Bucket, tokenReply.Result.ObjectKey)
		if err != nil {
			return "", fmt.Errorf("构造图片上传地址: %w", err)
		}

		headers := map[string]string{
			"X-Nos-Token":  tokenReply.Result.Token,
			"Content-Type": contentType,
		}

		if err := a.rawUpload(ctx, uploadURL, headers, data); err != nil {
			return "", fmt.Errorf("upload %s: %w", fp, err)
		}

		// 获取图片信息
		var (
			imgURL = "https://music.163.com/eapi/upload/event/img/v1"
			imgReq = struct {
				ImgId  string `json:"imgid"`
				Format string `json:"format"`
			}{
				ImgId:  tokenReply.Result.DocId,
				Format: ext,
			}
			imgReply eventUploadImgResp
			imgOpts  = api.NewOptions()
		)
		imgOpts.CryptoMode = api.CryptoModeEAPI
		imgOpts.Sensitive = true

		if _, err := a.client.Request(ctx, imgURL, imgReq, &imgReply, imgOpts); err != nil {
			return "", fmt.Errorf("get event img info for %s: %w", fp, err)
		}
		if imgReply.Code != 200 {
			return "", fmt.Errorf("get event img info failed for %s: code=%d", fp, imgReply.Code)
		}
		if err := validateEventImageInfo(imgReply); err != nil {
			return "", err
		}

		originIdStr := strconv.FormatInt(imgReply.PicInfo.OriginId, 10)
		squareIdStr := strconv.FormatInt(imgReply.PicInfo.SquareId, 10)
		rectangleIdStr := strconv.FormatInt(imgReply.PicInfo.RectangleId, 10)

		pics = append(pics, eventImgPicInfo{
			OriginId:      originIdStr,
			SquareId:      squareIdStr,
			RectangleId:   rectangleIdStr,
			PcSquareId:    squareIdStr,
			PcRectangleId: rectangleIdStr,
			OriginJpgId:   originIdStr,
			Width:         imgReply.PicInfo.Width,
			Height:        imgReply.PicInfo.Height,
			Index:         len(pics),
		})
	}

	picsBytes, err := json.Marshal(pics)
	if err != nil {
		return "", fmt.Errorf("marshal pics: %w", err)
	}
	return string(picsBytes), nil
}

// 下载的临时文件可能统一命名为 .jpg，上传类型和后续图片处理格式都必须以内容为准。
func eventImageFormat(data []byte) (string, string, error) {
	contentType := http.DetectContentType(data)
	switch contentType {
	case "image/jpeg":
		return contentType, "jpg", nil
	case "image/png":
		return contentType, "png", nil
	case "image/gif":
		return contentType, "gif", nil
	case "image/webp":
		return contentType, "webp", nil
	case "image/bmp":
		return contentType, "bmp", nil
	default:
		return "", "", fmt.Errorf("文件内容不是支持的图片格式（JPEG、PNG、GIF、WebP、BMP）")
	}
}

func validateEventImageInfo(reply eventUploadImgResp) error {
	if reply.PicInfo.OriginId <= 0 || reply.PicInfo.SquareId <= 0 || reply.PicInfo.RectangleId <= 0 || reply.PicInfo.Width <= 0 || reply.PicInfo.Height <= 0 {
		return fmt.Errorf("网易云未返回有效的动态图片信息")
	}
	return nil
}
