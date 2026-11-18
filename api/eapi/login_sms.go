package eapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/types"
	"github.com/3899/ncmm/api/weapi"
	"github.com/google/uuid"
)

const smsPCVersion = "3.1.42.205594"
const smsPCOSVersion = "Microsoft-Windows-10-Standard-Edition-build-20348-64bit"

// 短信发送与登录复用同一 PC 身份。接口和字段核对自上游 v1.2.10 发布程序的 sms.go。
type SMSLoginIdentity struct{ DeviceID, ClientSign string }

func NewSMSLoginIdentity() SMSLoginIdentity {
	deviceID := strings.ReplaceAll(uuid.NewString(), "-", "")
	mac := strings.ToUpper(deviceID[len(deviceID)-12:])
	pairs := make([]string, 0, 6)
	for i := 0; i < len(mac); i += 2 {
		pairs = append(pairs, mac[i:i+2])
	}
	nonce := uuid.NewString()[:18]
	sum := sha256.Sum256([]byte(deviceID + "@@@" + nonce + "@@@salt"))
	return SMSLoginIdentity{DeviceID: deviceID, ClientSign: strings.Join(pairs, ":") + "@@@" + nonce + "@@@@@@" + hex.EncodeToString(sum[:])}
}

type smsPCHeader struct {
	ClientSign string `json:"clientSign"`
	OS         string `json:"os"`
	AppVer     string `json:"appver"`
	DeviceID   string `json:"deviceId"`
	RequestID  int64  `json:"requestId"`
	OSVer      string `json:"osver"`
}

func smsPCOptions(identity SMSLoginIdentity) (string, *api.Options, error) {
	if identity.DeviceID == "" || identity.ClientSign == "" {
		return "", nil, fmt.Errorf("短信登录会话身份为空")
	}
	header, err := json.Marshal(smsPCHeader{ClientSign: identity.ClientSign, OS: "pc", AppVer: smsPCVersion, DeviceID: identity.DeviceID, RequestID: time.Now().UnixMilli(), OSVer: smsPCOSVersion})
	if err != nil {
		return "", nil, err
	}
	opts := api.NewOptions()
	opts.CryptoMode = api.CryptoModeEAPI
	opts.Sensitive = true
	opts.NoRetry = true
	opts.Headers = map[string]string{"User-Agent": "Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Safari/537.36 Chrome/91.0.4472.164 NeteaseMusicDesktop/" + smsPCVersion, "x-client-enc-state": "DEGRADE", "x-degrade-reason": "EncryptFailed", "x-os": "pc", "x-osver": smsPCOSVersion, "x-appver": smsPCVersion, "x-deviceid": identity.DeviceID}
	opts.SetCookies(&http.Cookie{Name: "os", Value: "pc"}, &http.Cookie{Name: "appver", Value: smsPCVersion}, &http.Cookie{Name: "osver", Value: smsPCOSVersion}, &http.Cookie{Name: "channel", Value: "netease"}, &http.Cookie{Name: "deviceId", Value: identity.DeviceID}, &http.Cookie{Name: "clientSign", Value: identity.ClientSign}, &http.Cookie{Name: "__remember_me", Value: "true"})
	return string(header), opts, nil
}

type SMSCaptchaResp struct {
	types.RespCommon[json.RawMessage]
}

func (a *Api) SMSCaptchaSend(ctx context.Context, phone string, country int64, identity SMSLoginIdentity) (*SMSCaptchaResp, error) {
	header, opts, err := smsPCOptions(identity)
	if err != nil {
		return nil, err
	}
	if country <= 0 {
		country = 86
	}
	payload := struct {
		Scene     string `json:"scene"`
		Cellphone string `json:"cellphone"`
		CtCode    string `json:"ctcode"`
		Secrete   string `json:"secrete"`
		ER        bool   `json:"e_r"`
		Header    string `json:"header"`
	}{"0", phone, strconv.FormatInt(country, 10), "music_middleuser_pclogin", true, header}
	var response SMSCaptchaResp
	_, err = a.client.Request(ctx, "https://interfacepc.music.163.com/eapi/middle/captcha/sent/v1", payload, &response, opts)
	return &response, err
}

func (a *Api) SMSCellphoneLogin(ctx context.Context, phone string, country int64, captcha string, identity SMSLoginIdentity) (*weapi.LoginCellphoneResp, error) {
	header, opts, err := smsPCOptions(identity)
	if err != nil {
		return nil, err
	}
	if country <= 0 {
		country = 86
	}
	payload := struct {
		Type        string `json:"type"`
		Phone       string `json:"phone"`
		Remember    string `json:"remember"`
		HTTPS       string `json:"https"`
		CountryCode string `json:"countrycode"`
		ER          bool   `json:"e_r"`
		Header      string `json:"header"`
		Captcha     string `json:"captcha"`
	}{"1", phone, "true", "true", strconv.FormatInt(country, 10), true, header, captcha}
	var response weapi.LoginCellphoneResp
	_, err = a.client.Request(ctx, "https://interfacepc.music.163.com/eapi/w/login/cellphone", payload, &response, opts)
	return &response, err
}
