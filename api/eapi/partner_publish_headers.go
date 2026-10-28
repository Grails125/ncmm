package eapi

import (
	"regexp"
	"strings"

	"github.com/3899/ncmm/api"
)

var partnerIOSAppVersion = regexp.MustCompile(`(?i)\bneteasemusic(?:/|\s+)([0-9]+(?:\.[0-9]+){1,3})(?:/([0-9]+))?`)
var partnerIOSOSVersion = regexp.MustCompile(`(?i)\b(?:iOS|iPhone OS|CPU (?:iPhone )?OS)\s+([0-9]+(?:[._][0-9]+){0,3})`)
var partnerIOSNumericVersion = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*$`)

type partnerIOSIdentity struct {
	os, osVersion, appVersion, buildVersion string
}

type partnerIOSPublishHeader struct {
	OS         string `json:"os"`
	OSVersion  string `json:"osver"`
	AppVersion string `json:"appver"`
	Build      string `json:"buildver,omitempty"`
	DeviceID   string `json:"deviceId"`
	MusicU     string `json:"MUSIC_U,omitempty"`
	CSRF       string `json:"__csrf,omitempty"`
}

func partnerIOSIdentityFromUA(ua string) partnerIOSIdentity {
	var identity partnerIOSIdentity
	lower := strings.ToLower(ua)
	switch {
	case strings.Contains(lower, "ipad"):
		identity.os = "iPad"
	case strings.Contains(lower, "iphone"), strings.Contains(lower, "ios"):
		identity.os = "iPhone OS"
	}
	if identity.os == "" {
		return identity
	}
	if match := partnerIOSAppVersion.FindStringSubmatch(ua); len(match) > 0 {
		parts := strings.Split(match[1], ".")
		identity.appVersion = match[1]
		identity.buildVersion = match[2]
		if len(parts) == 4 {
			identity.appVersion = strings.Join(parts[:3], ".")
			if identity.buildVersion == "" {
				identity.buildVersion = parts[3]
			}
		}
	}
	if match := partnerIOSOSVersion.FindStringSubmatch(ua); len(match) > 0 {
		identity.osVersion = strings.ReplaceAll(match[1], "_", ".")
	}
	return identity
}

// 推荐发布只从所选 iOS UA 构造移动身份，不把 PC 扫码 Cookie 的版本当作 iOS 版本。
// 登录和设备凭据仍由已有会话头构造；相同 iOS 平台 Cookie 只补充 UA 缺少的字段。
func (a *Api) partnerPublishHeaders(opts *api.Options, mode api.CryptoMode, payloadHeader *interface{}) bool {
	a.applyDailySongShareHeaders(opts, mode)
	if mode != api.CryptoModeEAPI {
		return true
	}
	ua := strings.TrimSpace(a.client.UserAgent(mode))
	identity := partnerIOSIdentityFromUA(ua)
	cookieOS := vipMemberGiftCookieValue(a.client, "os")
	if vipMemberGiftPlatformFromText(cookieOS) == "ios" {
		if identity.os == "" {
			if strings.Contains(strings.ToLower(cookieOS), "ipad") {
				identity.os = "iPad"
			} else {
				identity.os = "iPhone OS"
			}
		}
		for _, field := range []struct {
			value *string
			name  string
		}{{&identity.osVersion, "osver"}, {&identity.appVersion, "appver"}, {&identity.buildVersion, "buildver"}} {
			if *field.value == "" {
				value := vipMemberGiftCookieValue(a.client, field.name)
				if partnerIOSNumericVersion.MatchString(value) {
					*field.value = value
				}
			}
		}
	}
	if identity.os == "" || identity.osVersion == "" || identity.appVersion == "" {
		// 不推测缺失版本，也不让全局头补全重新引入 PC Cookie 的版本。
		return false
	}
	opts.SetHeader("User-Agent", ua)
	opts.SetHeader("x-os", identity.os)
	opts.SetHeader("x-osver", identity.osVersion)
	opts.SetHeader("x-appver", identity.appVersion)
	opts.SetHeader("x-buildver", identity.buildVersion)
	if payloadHeader != nil {
		csrf, _ := a.client.GetCSRF("https://music.163.com/")
		*payloadHeader = partnerIOSPublishHeader{
			OS: identity.os, OSVersion: identity.osVersion,
			AppVersion: identity.appVersion, Build: identity.buildVersion,
			DeviceID: opts.Headers["x-deviceid"], MusicU: a.client.MusicU(), CSRF: csrf,
		}
	}
	return true
}
