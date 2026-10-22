// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package config

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/internal/atomicfile"
	"github.com/3899/ncmm/internal/filelock"
	"github.com/3899/ncmm/pkg/database"
	"github.com/3899/ncmm/pkg/log"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

var HomeDir string

var (
	//go:embed config.yaml
	defaultConfigByte []byte
	defaultConfig     *Config
)

func init() {
	var err error
	if os.Getenv("NCMM_DOCKER_OFFICIAL") == "1" {
		HomeDir = "/data"
	} else {
		var exePath string
		exePath, err = os.Executable()
		if err == nil {
			HomeDir = filepath.Dir(exePath)
		} else {
			HomeDir = "."
		}
	}
	if err := yaml.Unmarshal(defaultConfigByte, &defaultConfig); err != nil {
		panic(fmt.Sprintf("defaultConfig.Unmarshal: %s", err))
	}
	defaultConfig.syncMainCookie()
	// defaultConfig.ReplaceMagicVariables("HOME", HomeDir)
	if err := defaultConfig.Validate(); err != nil {
		panic(fmt.Sprintf("defaultConfig.Validate: %s", err))
	}
}

func decodeB64(s string) string {
	b, _ := base64.StdEncoding.DecodeString(s)
	return string(b)
}

type AccountsConf struct {
	Main            string            `json:"main" yaml:"main"`
	Primary         string            `json:"primary" yaml:"primary"` // 兼容旧版
	Secondary       []string          `json:"secondary" yaml:"secondary"`
	AntiCheatTokens map[string]string `json:"antiCheatTokens" yaml:"antiCheatTokens"`
}

// AntiCheatTokenFor returns the antiCheatToken for the given cookie file path.
// It tries both the original path and the absolute path for matching.
func (a *AccountsConf) AntiCheatTokenFor(cookiePath string) string {
	if a == nil || a.AntiCheatTokens == nil {
		return ""
	}
	candidates := []string{cookiePath}
	if abs, err := filepath.Abs(cookiePath); err == nil && abs != cookiePath {
		candidates = append(candidates, abs)
	}

	for _, candidate := range candidates {
		if v := lookupAntiCheatToken(a.AntiCheatTokens, candidate); v != "" {
			return v
		}
	}
	return ""
}

func lookupAntiCheatToken(tokens map[string]string, cookiePath string) string {
	if v, ok := tokens[cookiePath]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	cleanPath := filepath.Clean(cookiePath)
	for key, value := range tokens {
		token := strings.TrimSpace(value)
		if token == "" {
			continue
		}
		if strings.EqualFold(key, cookiePath) || strings.EqualFold(filepath.Clean(key), cleanPath) {
			return token
		}
	}
	return ""
}

type YunbeiTaskConf struct {
	EnableViewVipCenter      bool `json:"enableViewVipCenter" yaml:"enableViewVipCenter"`
	EnableLikeComment        bool `json:"enableLikeComment" yaml:"enableLikeComment"`
	EnableListenIndie        bool `json:"enableListenIndie" yaml:"enableListenIndie"`
	EnableReserve            bool `json:"enableReserve" yaml:"enableReserve"`
	EnableFollowArtist       bool `json:"enableFollowArtist" yaml:"enableFollowArtist"`
	EnableLikeSong           bool `json:"enableLikeSong" yaml:"enableLikeSong"`
	EnableCollectSong        bool `json:"enableCollectSong" yaml:"enableCollectSong"`
	EnablePublishNote        bool `json:"enablePublishNote" yaml:"enablePublishNote"`
	EnableShareSong          bool `json:"enableShareSong" yaml:"enableShareSong"`
	EnablePlayDailyRecommend bool `json:"enablePlayDailyRecommend" yaml:"enablePlayDailyRecommend"`
}

type SignConf struct {
	EnableMain        bool            `json:"enableMain" yaml:"enableMain"`
	EnablePrimary     bool            `json:"enablePrimary" yaml:"enablePrimary"` // 兼容旧版
	EnableSecondaries bool            `json:"enableSecondaries" yaml:"enableSecondaries"`
	YunbeiTask        *YunbeiTaskConf `json:"yunbeiTask" yaml:"yunbeiTask"`
	Automatic         bool            `json:"automatic" yaml:"automatic"`
	EnableVipTask     *bool           `json:"enableVipTask" yaml:"enableVipTask"`
}

type TaskConf struct {
	Sign           bool     `json:"sign" yaml:"sign"`
	PlayIds        bool     `json:"playids" yaml:"playids"`
	MusicianSign   bool     `json:"musician-sign" yaml:"musician-sign"`
	MusicianVip    bool     `json:"musician-vip" yaml:"musician-vip"`
	Note           bool     `json:"note" yaml:"note"`
	FansGroup      bool     `json:"fansgroup" yaml:"fansgroup"`
	DailySongShare bool     `json:"daily-song-share" yaml:"daily-song-share"`
	VipMemberGift  bool     `json:"vip-member-gift" yaml:"vip-member-gift"`
	MusicPartner   bool     `json:"music-partner" yaml:"music-partner"`
	Mode           string   `json:"mode" yaml:"mode"`
	FastTasks      []string `json:"fast_tasks" yaml:"fast_tasks"`
	SlowTasks      []string `json:"slow_tasks" yaml:"slow_tasks"`
}

// MusicPartnerConf 音乐合伙人随机评分及乐评配置，不分析音频。
type MusicPartnerConf struct {
	EnableMain        bool                `json:"enableMain" yaml:"enableMain"`
	EnableSecondaries bool                `json:"enableSecondaries" yaml:"enableSecondaries"`
	Score             int                 `json:"score,omitempty" yaml:"score,omitempty"` // 兼容旧版策略，仅在缺少分值范围时读取。
	ScoreMin          int                 `json:"scoreMin" yaml:"scoreMin"`
	ScoreMax          int                 `json:"scoreMax" yaml:"scoreMax"`
	ExtraTasks        bool                `json:"extraTasks" yaml:"extraTasks"`
	FullExtraTasks    bool                `json:"fullExtraTasks" yaml:"fullExtraTasks"`
	ExtraLimit        int                 `json:"extraLimit,omitempty" yaml:"extraLimit,omitempty"` // 兼容旧版固定目标。
	ExtraLimitMin     int                 `json:"extraLimitMin" yaml:"extraLimitMin"`
	ExtraLimitMax     int                 `json:"extraLimitMax" yaml:"extraLimitMax"`
	WaitMin           int                 `json:"waitMin" yaml:"waitMin"`
	WaitMax           int                 `json:"waitMax" yaml:"waitMax"`
	SyncYunCircle     bool                `json:"syncYunCircle" yaml:"syncYunCircle"`
	Comments          map[string][]string `json:"comments" yaml:"comments"`
}

// Validate 补全缺省范围、兼容旧字段，并校验用户乐评；不改写乐评文本。
func (p *MusicPartnerConf) Validate() error {
	if p.ScoreMin == 0 && p.ScoreMax == 0 {
		switch p.Score {
		case 0:
			p.ScoreMin, p.ScoreMax = 3, 4
		case 1, 2, 3:
			p.ScoreMin, p.ScoreMax = p.Score, p.Score+1
		case 4:
			p.ScoreMin, p.ScoreMax = 4, 4
		default:
			return fmt.Errorf("音乐合伙人旧版评分策略必须为 1 至 4")
		}
	}
	if p.ScoreMin < 1 || p.ScoreMax < p.ScoreMin || p.ScoreMax > 5 {
		return fmt.Errorf("音乐合伙人评分范围必须完整填写 1 至 5 分，且上限不得小于下限")
	}
	if p.ExtraLimitMin == 0 && p.ExtraLimitMax == 0 {
		target := p.ExtraLimit
		if target == 0 {
			target = 7
		}
		p.ExtraLimitMin, p.ExtraLimitMax = target, target
	}
	if p.ExtraLimitMin < 1 || p.ExtraLimitMax < p.ExtraLimitMin || p.ExtraLimitMax > 500 {
		return fmt.Errorf("音乐合伙人额外评分目标范围必须完整填写 1 至 500 首，且上限不得小于下限")
	}
	if p.WaitMin == 0 && p.WaitMax == 0 {
		p.WaitMin, p.WaitMax = 15, 20
	}
	if p.WaitMin < 1 || p.WaitMax < p.WaitMin || p.WaitMax > 300 {
		return fmt.Errorf("音乐合伙人等待范围必须完整填写 1 至 300 秒，且上限不得小于下限")
	}
	if p.Comments == nil {
		p.Comments = make(map[string][]string)
	}
	for score, comments := range p.Comments {
		if len(score) != 1 || score[0] < '1' || score[0] > '5' {
			return fmt.Errorf("音乐合伙人乐评分值键只能为 1、2、3、4、5")
		}
		if len(comments) > 50 {
			return fmt.Errorf("音乐合伙人每个分值最多配置 50 条乐评")
		}
		for _, comment := range comments {
			if strings.TrimSpace(comment) == "" || utf8.RuneCountInString(comment) > 500 {
				return fmt.Errorf("音乐合伙人每条乐评必须为 1 至 500 个字符，不能仅包含空白")
			}
		}
	}
	return nil
}

func validateMusicPartnerRangePresence(rawValue func(string) (string, bool)) error {
	for _, bounds := range []struct {
		minKey, maxKey string
	}{
		{"scoreMin", "scoreMax"},
		{"extraLimitMin", "extraLimitMax"},
		{"waitMin", "waitMax"},
	} {
		minValue, hasMin := rawValue(bounds.minKey)
		maxValue, hasMax := rawValue(bounds.maxKey)
		min, minErr := strconv.Atoi(minValue)
		max, maxErr := strconv.Atoi(maxValue)
		if hasMin != hasMax || (hasMin && (minErr != nil || maxErr != nil || min <= 0 || max <= 0)) {
			return fmt.Errorf("音乐合伙人 %s 和 %s 必须同时填写有效的正整数", bounds.minKey, bounds.maxKey)
		}
	}
	return nil
}

// FansGroupConf 乐迷团任务配置
type FansGroupConf struct {
	EnableMain        bool          `json:"enableMain" yaml:"enableMain"`
	EnableSecondaries bool          `json:"enableSecondaries" yaml:"enableSecondaries"`
	GroupIDs          StringOrSlice `json:"groupIds" yaml:"groupIds"`
	AutoDeleteNote    *bool         `json:"autoDeleteNote" yaml:"autoDeleteNote"`
}

type MixPlayConf struct {
	Enabled             bool    `json:"enabled" yaml:"enabled"`
	DailyRecommendRatio float64 `json:"dailyRecommendRatio" yaml:"dailyRecommendRatio"`
	CountTarget         bool    `json:"countTarget" yaml:"countTarget"`
}

// StringOrSlice supports unmarshaling from either a single string or an array of strings.
type StringOrSlice []string

// UnmarshalYAML implements yaml.Unmarshaler
func (s *StringOrSlice) UnmarshalYAML(value *yaml.Node) error {
	var str string
	if err := value.Decode(&str); err == nil {
		*s = []string{str}
		return nil
	}
	var slice []string
	if err := value.Decode(&slice); err == nil {
		*s = slice
		return nil
	}
	return fmt.Errorf("failed to unmarshal StringOrSlice at line %d", value.Line)
}

type PlayIdsConfig struct {
	DailyMin          int64         `json:"daily_min" yaml:"daily_min"`
	DailyMax          int64         `json:"daily_max" yaml:"daily_max"`
	RunMin            int64         `json:"run_min" yaml:"run_min"`
	RunMax            int64         `json:"run_max" yaml:"run_max"`
	GapMin            int64         `json:"gap_min" yaml:"gap_min"`
	GapMax            int64         `json:"gap_max" yaml:"gap_max"`
	IDs               string        `json:"ids" yaml:"ids"`
	IDsFile           StringOrSlice `json:"idsFile" yaml:"idsFile"`
	PlaylistIDs       StringOrSlice `json:"playlistIds" yaml:"playlistIds"`
	EnableMain        bool          `json:"enableMain" yaml:"enableMain"`
	EnablePrimary     bool          `json:"enablePrimary" yaml:"enablePrimary"` // 兼容旧版
	EnableSecondaries bool          `json:"enableSecondaries" yaml:"enableSecondaries"`
}

type UpdaterConf struct {
	Check        *bool    `json:"check" yaml:"check"`
	AutoUpdate   *bool    `json:"auto_update" yaml:"auto_update"`
	AutoRestart  *bool    `json:"auto_restart" yaml:"auto_restart"`
	ProxyMirrors []string `json:"proxy_mirrors" yaml:"proxy_mirrors"`
}

// NotifyConf 通知策略配置（通道凭证在独立 notify.yaml 中，见 File）。
type NotifyConf struct {
	Enabled     bool   `json:"enabled" yaml:"enabled"`
	OnSkip      *bool  `json:"on_skip" yaml:"on_skip"` // 是否推送 skip，默认 true
	TitlePrefix string `json:"title_prefix" yaml:"title_prefix"`
	Timeout     string `json:"timeout" yaml:"timeout"` // 单通道超时，如 10s
	File        string `json:"file" yaml:"file"`       // 通道配置文件路径，默认 notify.yaml
}

// OnSkipEnabled 返回 skip 是否需要推送（默认 true）。
func (n *NotifyConf) OnSkipEnabled() bool {
	if n == nil || n.OnSkip == nil {
		return true
	}
	return *n.OnSkip
}

type Config struct {
	v              *viper.Viper
	Version        string              `json:"version" yaml:"version"`
	Accounts       *AccountsConf       `json:"accounts" yaml:"accounts"`
	Log            *log.Config         `json:"log" yaml:"log"`
	Network        *api.Config         `json:"network" yaml:"network"`
	Database       *database.Config    `json:"database" yaml:"database"`
	PlayIds        *PlayIdsConfig      `json:"playids" yaml:"playids"`
	Sign           *SignConf           `json:"sign" yaml:"sign"`
	MixPlay        *MixPlayConf        `json:"mixPlay" yaml:"mixPlay"`
	Note           *NoteConf           `json:"note" yaml:"note"`
	Musician       *MusicianConf       `json:"musician" yaml:"musician"`
	FansGroup      *FansGroupConf      `json:"fansgroup" yaml:"fansgroup"`
	DailySongShare *DailySongShareConf `json:"dailySongShare" yaml:"dailySongShare"`
	VipMemberGift  *VipMemberGiftConf  `json:"vipMemberGift" yaml:"vipMemberGift"`
	MusicPartner   *MusicPartnerConf   `json:"musicPartner" yaml:"musicPartner"`
	Task           *TaskConf           `json:"task" yaml:"task"`
	Updater        *UpdaterConf        `json:"updater" yaml:"updater"`
	Notify         *NotifyConf         `json:"notify" yaml:"notify"`
}

// MusicianConf 音乐人任务配置
type MusicianConf struct {
	EnableMain        bool             `json:"enableMain" yaml:"enableMain"`
	EnableSecondaries bool             `json:"enableSecondaries" yaml:"enableSecondaries"`
	IdentityCacheDays *int             `json:"identityCacheDays" yaml:"identityCacheDays"`
	EnableVipNote     *bool            `json:"enableVipNote" yaml:"enableVipNote"`
	EnableVipPlay     *bool            `json:"enableVipPlay" yaml:"enableVipPlay"`
	EnableVipClaim    *bool            `json:"enableVipClaim" yaml:"enableVipClaim"`
	Play              MusicianPlayConf `json:"play" yaml:"play"`
}

func (m *MusicianConf) IsVipClaimEnabled() bool {
	if m == nil || m.EnableVipClaim == nil {
		return true
	}
	return *m.EnableVipClaim
}

// NoteConf 笔记发布公共配置
type NoteConf struct {
	Titles       []string      `json:"titles" yaml:"titles"`
	TitlesFile   StringOrSlice `json:"titlesFile" yaml:"titlesFile"`
	Messages     []string      `json:"messages" yaml:"messages"`
	MessagesFile StringOrSlice `json:"messagesFile" yaml:"messagesFile"`
	ImageURLs    StringOrSlice `json:"imageUrls" yaml:"imageUrls"`
	Type         int           `json:"type" yaml:"type"`
	AutoDelete   *bool         `json:"autoDelete" yaml:"autoDelete"`
}

// DailySongShareConf 每日推歌发布配置。
type DailySongShareConf struct {
	EnableMain        bool                       `json:"enableMain" yaml:"enableMain"`
	EnableSecondaries bool                       `json:"enableSecondaries" yaml:"enableSecondaries"`
	SongId            string                     `json:"songId" yaml:"songId"`
	PlaylistId        string                     `json:"playlistId" yaml:"playlistId"`
	ImageMode         string                     `json:"imageMode" yaml:"imageMode"`
	ImageURLs         StringOrSlice              `json:"imageUrls" yaml:"imageUrls"`
	TitleMode         string                     `json:"titleMode" yaml:"titleMode"`
	Titles            []string                   `json:"titles" yaml:"titles"`
	TitlesFile        StringOrSlice              `json:"titlesFile" yaml:"titlesFile"`
	Messages          []string                   `json:"messages" yaml:"messages"`
	MessagesFile      StringOrSlice              `json:"messagesFile" yaml:"messagesFile"`
	Topics            []DailySongShareTopicConf  `json:"topics" yaml:"topics"`
	AutoDelete        *bool                      `json:"autoDelete" yaml:"autoDelete"`
	Lottery           *DailySongShareLotteryConf `json:"lottery" yaml:"lottery"`
}

type DailySongShareTopicConf struct {
	Id        string `json:"id" yaml:"id"`
	Name      string `json:"name" yaml:"name"`
	Type      int    `json:"type" yaml:"type"`
	SubType   int    `json:"subType" yaml:"subType"`
	Selected  *bool  `json:"selected" yaml:"selected"`
	CanChange *bool  `json:"canChange" yaml:"canChange"`
}

type DailySongShareLotteryConf struct {
	Enabled      bool   `json:"enabled" yaml:"enabled"`
	ActivityId   string `json:"activityId" yaml:"activityId"`
	AutoRegister *bool  `json:"autoRegister" yaml:"autoRegister"`
}

// VipMemberGiftConf configures the VIP member gift task.
type VipMemberGiftConf struct {
	EnableMain            bool                   `json:"enableMain" yaml:"enableMain"`
	EnableSecondaries     bool                   `json:"enableSecondaries" yaml:"enableSecondaries"`
	EnableGift            bool                   `json:"enableGift" yaml:"enableGift"`
	EnableGiftSecondaries bool                   `json:"enableGiftSecondaries" yaml:"enableGiftSecondaries"`
	EnableClaim           bool                   `json:"enableClaim" yaml:"enableClaim"`
	Refer                 string                 `json:"refer" yaml:"refer"`
	Cloud                 VipMemberGiftCloudConf `json:"cloud" yaml:"cloud"`
}

type VipMemberGiftCloudConf struct {
	BaseURL string `json:"baseUrl" yaml:"baseUrl"`
	Token   string `json:"token" yaml:"token"`
}

// MusicianPlayConf 播放任务配置
type MusicianPlayConf struct {
	IDs      string        `json:"ids" yaml:"ids"`
	IDsFile  StringOrSlice `json:"idsFile" yaml:"idsFile"`
	DailyMin int64         `json:"daily_min" yaml:"daily_min"`
	DailyMax int64         `json:"daily_max" yaml:"daily_max"`
	RunMin   int64         `json:"run_min" yaml:"run_min"`
	RunMax   int64         `json:"run_max" yaml:"run_max"`
	GapMin   int64         `json:"gap_min" yaml:"gap_min"`
	GapMax   int64         `json:"gap_max" yaml:"gap_max"`
}

func (c *Config) Validate() error {
	if err := validateSourceVersion(c.Version); err != nil {
		return err
	}
	if c.MusicPartner == nil {
		c.MusicPartner = &MusicPartnerConf{EnableMain: true, ExtraTasks: true}
	}
	if err := c.MusicPartner.Validate(); err != nil {
		return err
	}
	if c.Updater == nil {
		c.Updater = &UpdaterConf{}
	}
	if c.Updater.Check == nil {
		chk := true
		c.Updater.Check = &chk
	}
	if c.Updater.AutoUpdate == nil {
		auto := false
		c.Updater.AutoUpdate = &auto
	}
	if c.Updater.AutoRestart == nil {
		enabled := true
		c.Updater.AutoRestart = &enabled
	}
	if len(c.Updater.ProxyMirrors) == 0 {
		c.Updater.ProxyMirrors = []string{
			"https://gh-proxy.com/",
			"https://ghproxy.net/",
			"https://githubproxy.cc/",
		}
	}

	if c.Accounts != nil {
		if c.Accounts.Main == "" && c.Accounts.Primary != "" {
			c.Accounts.Main = c.Accounts.Primary
		}
		if c.Accounts.AntiCheatTokens == nil {
			c.Accounts.AntiCheatTokens = make(map[string]string)
		}
	}
	if c.PlayIds != nil {
		if !c.PlayIds.EnableMain && c.PlayIds.EnablePrimary {
			c.PlayIds.EnableMain = c.PlayIds.EnablePrimary
		}
	}
	if c.Sign != nil {
		if !c.Sign.EnableMain && c.Sign.EnablePrimary {
			c.Sign.EnableMain = c.Sign.EnablePrimary
		}
		if c.Sign.EnableVipTask == nil {
			enable := true
			c.Sign.EnableVipTask = &enable
		}
		if c.Sign.EnableVipTask == nil {
			enable := true
			c.Sign.EnableVipTask = &enable
		}
		if c.Sign.YunbeiTask == nil {
			c.Sign.YunbeiTask = &YunbeiTaskConf{
				EnableViewVipCenter:      true,
				EnableLikeComment:        true,
				EnableListenIndie:        true,
				EnableReserve:            true,
				EnableFollowArtist:       true,
				EnableLikeSong:           true,
				EnableCollectSong:        true,
				EnablePublishNote:        true,
				EnableShareSong:          true,
				EnablePlayDailyRecommend: false,
			}
		}
	}
	if c.Musician != nil {
		if c.Musician.IdentityCacheDays == nil {
			days := 30
			c.Musician.IdentityCacheDays = &days
		}
		if c.Musician.EnableVipNote == nil {
			enable := true
			c.Musician.EnableVipNote = &enable
		}
		if c.Musician.EnableVipPlay == nil {
			enable := true
			c.Musician.EnableVipPlay = &enable
		}
	}
	if c.DailySongShare != nil {
		if c.DailySongShare.PlaylistId == "" {
			c.DailySongShare.PlaylistId = "13848930701"
		}
		if c.DailySongShare.ImageMode == "" {
			c.DailySongShare.ImageMode = "songCover"
		}
		if c.DailySongShare.TitleMode == "" {
			c.DailySongShare.TitleMode = "note"
		}
		if c.DailySongShare.Lottery == nil {
			c.DailySongShare.Lottery = &DailySongShareLotteryConf{}
		}
		if c.DailySongShare.Lottery.ActivityId == "" {
			c.DailySongShare.Lottery.ActivityId = "11066304"
		}
		if c.DailySongShare.Lottery.AutoRegister == nil {
			autoRegister := true
			c.DailySongShare.Lottery.AutoRegister = &autoRegister
		}
		for i := range c.DailySongShare.Topics {
			if c.DailySongShare.Topics[i].Type == 0 {
				if c.DailySongShare.Topics[i].SubType == 11 || strings.Contains(c.DailySongShare.Topics[i].Name, "乐迷团") {
					c.DailySongShare.Topics[i].Type = 3
				} else {
					c.DailySongShare.Topics[i].Type = 2
				}
			}
			if c.DailySongShare.Topics[i].Selected == nil {
				selected := true
				c.DailySongShare.Topics[i].Selected = &selected
			}
			if c.DailySongShare.Topics[i].CanChange == nil {
				canChange := true
				c.DailySongShare.Topics[i].CanChange = &canChange
			}
		}
	}
	if c.VipMemberGift != nil {
		if strings.TrimSpace(c.VipMemberGift.Cloud.BaseURL) == "" {
			c.VipMemberGift.Cloud.BaseURL = decodeB64("aHR0cHM6Ly9uY21tLmhhb3ouY2MuY2Q=")
		}
		if strings.TrimSpace(c.VipMemberGift.Cloud.Token) == "" {
			c.VipMemberGift.Cloud.Token = decodeB64("eXVqaWVyZGFOQjY2Ng==")
		}
	}
	if c.Task != nil {
		if c.Task.Mode == "" {
			c.Task.Mode = "by-task-group"
		}
		if len(c.Task.FastTasks) == 0 {
			c.Task.FastTasks = []string{
				"VipTask", "Reserve", "ViewVipCenter", "LikeComment",
				"FollowArtist", "LikeSong", "CollectSong", "PublishNote",
				"musician-sign", "note", "daily-song-share", "vip-member-gift", "fansgroup",
			}
		}
		if len(c.Task.SlowTasks) == 0 {
			c.Task.SlowTasks = []string{
				"ListenIndie", "PlayDailyRecommend", "playids", "musician-vip", "music-partner",
			}
		}
	}
	if c.Notify == nil {
		c.Notify = &NotifyConf{}
	}
	if c.Notify.OnSkip == nil {
		onSkip := true
		c.Notify.OnSkip = &onSkip
	}
	if strings.TrimSpace(c.Notify.TitlePrefix) == "" {
		c.Notify.TitlePrefix = "ncmm"
	}
	if strings.TrimSpace(c.Notify.Timeout) == "" {
		c.Notify.Timeout = "10s"
	}
	if strings.TrimSpace(c.Notify.File) == "" {
		c.Notify.File = "notify.yaml"
	}
	return nil
}

func GetDefault() *Config {
	return defaultConfig
}

// DefaultYAML returns a copy of the embedded default configuration.
func DefaultYAML() []byte {
	data := make([]byte, len(defaultConfigByte))
	copy(data, defaultConfigByte)
	return data
}

func New(cfgPath ...string) (*Config, error) {
	v := newConfigViper()
	if len(cfgPath) > 0 {
		v.SetConfigFile(cfgPath[0])
	}
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("ReadInConfig: %w", err)
	}
	return decodeConfig(v)
}

func newConfigViper() *viper.Viper {
	v := viper.NewWithOptions(viper.KeyDelimiter("::"))
	v.SetTypeByDefaultValue(true)
	v.SetEnvPrefix("ncmm")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	v.AllowEmptyEnv(true)
	v.SetConfigType("yaml")
	return v
}

func decodeConfig(v *viper.Viper) (*Config, error) {
	var (
		conf Config
		opts = func(m *mapstructure.DecoderConfig) {
			m.TagName = "yaml"
			m.DecodeHook = mapstructure.ComposeDecodeHookFunc(
				mapstructure.StringToTimeDurationHookFunc(),
				mapstructure.StringToSliceHookFunc(","),
				func(f reflect.Type, t reflect.Type, data interface{}) (interface{}, error) {
					if t == reflect.TypeOf(StringOrSlice{}) {
						switch v := data.(type) {
						case string:
							return StringOrSlice{v}, nil
						case []interface{}:
							var res StringOrSlice
							for _, item := range v {
								if s, ok := item.(string); ok {
									res = append(res, s)
								} else {
									return nil, fmt.Errorf("invalid element type in StringOrSlice: %T", item)
								}
							}
							return res, nil
						case []string:
							return StringOrSlice(v), nil
						}
					}
					return data, nil
				},
			)
		}
	)
	if err := v.UnmarshalExact(&conf, opts); err != nil {
		return nil, fmt.Errorf("UnmarshalExact: %w", err)
	}
	partnerValues := v.GetStringMap("musicPartner")
	if err := validateMusicPartnerRangePresence(func(key string) (string, bool) {
		// IsSet 将显式 null 视为未设置，键存在性还需从配置映射读取。
		_, present := partnerValues[strings.ToLower(key)]
		key = "musicPartner::" + key
		return fmt.Sprint(v.Get(key)), present || v.IsSet(key)
	}); err != nil {
		return nil, err
	}
	conf.syncMainCookie()
	if err := conf.Validate(); err != nil {
		return nil, err
	}
	return &conf, nil
}

// ReplaceMagicVariables 替换配置文件中的魔法变量。注意该方法只能调用一次再次调用则不会生效.
func (c *Config) ReplaceMagicVariables(name, value string) (*Config, bool) {

	var (
		isset   bool
		mapping = func(k string) string {
			switch k {
			case name:
				isset = true
				return value
			}
			return ""
		}
	)

	c.Log.Rotate.Filename = os.Expand(c.Log.Rotate.Filename, mapping)
	c.Network.Cookie.Filepath = os.Expand(c.Network.Cookie.Filepath, mapping)
	c.Database.Path = os.Expand(c.Database.Path, mapping)
	if c.Accounts != nil {
		c.Accounts.Main = os.Expand(c.Accounts.Main, mapping)
		c.Accounts.Primary = os.Expand(c.Accounts.Primary, mapping)
		for i, sec := range c.Accounts.Secondary {
			c.Accounts.Secondary[i] = os.Expand(sec, mapping)
		}
	}
	if c.PlayIds != nil {
		for i, file := range c.PlayIds.IDsFile {
			c.PlayIds.IDsFile[i] = os.Expand(file, mapping)
		}
	}
	if c.Musician != nil {
		for i, file := range c.Musician.Play.IDsFile {
			c.Musician.Play.IDsFile[i] = os.Expand(file, mapping)
		}
	}
	if c.Note != nil {
		for i, file := range c.Note.MessagesFile {
			c.Note.MessagesFile[i] = os.Expand(file, mapping)
		}
		for i, file := range c.Note.TitlesFile {
			c.Note.TitlesFile[i] = os.Expand(file, mapping)
		}
		for i, file := range c.Note.ImageURLs {
			c.Note.ImageURLs[i] = os.Expand(file, mapping)
		}
	}
	if c.DailySongShare != nil {
		for i, file := range c.DailySongShare.MessagesFile {
			c.DailySongShare.MessagesFile[i] = os.Expand(file, mapping)
		}
		for i, file := range c.DailySongShare.TitlesFile {
			c.DailySongShare.TitlesFile[i] = os.Expand(file, mapping)
		}
		for i, file := range c.DailySongShare.ImageURLs {
			c.DailySongShare.ImageURLs[i] = os.Expand(file, mapping)
		}
	}
	return c, isset
}

func (c *Config) syncMainCookie() {
	if c.Accounts != nil && c.Accounts.Main != "" {
		if c.Network != nil {
			c.Network.Cookie.Filepath = c.Accounts.Main
		}
	}
}

type verStruct struct {
	Version string `yaml:"version"`
}

// AutoUpgradeConfigIfNeeded 检查配置文件版本，并在版本不符时自动执行迁移和结构升级合并
func AutoUpgradeConfigIfNeeded(cfgPath string) error {
	lock, err := acquireConfigWriteLock(cfgPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(cfgPath)
	_ = lock.Close()
	if err != nil {
		return err
	}
	var v verStruct
	if err := yaml.Unmarshal(data, &v); err != nil {
		return err
	}
	if err := validateSourceVersion(v.Version); err != nil {
		return err
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}
	partnerUpgrade, err := migrateMusicPartnerRangeNodes(&root)
	if err != nil {
		return err
	}
	if v.Version != defaultConfig.Version || partnerUpgrade {
		if _, err := BackupConfigFile(cfgPath, 2); err != nil {
			log.Warn("[config] 升级前自动备份配置文件失败: %v", err)
		}
		// 先执行原有的字段名重命名迁移
		_ = MigrateConfigFile(cfgPath)
		// 再执行 AST 级的新配置模板字段与注释合并升级
		return AutoUpgradeConfig(cfgPath)
	}
	return nil
}

// AutoUpgradeConfig 读取旧配置文件，将其与嵌入的新配置模板进行 AST 级递归合并，保存最新结构与中文注释的同时保留用户值
func AutoUpgradeConfig(cfgPath string) error {
	lock, err := acquireConfigWriteLock(cfgPath)
	if err != nil {
		return err
	}
	defer lock.Close()

	if _, err := BackupConfigFile(cfgPath, 2); err != nil {
		log.Warn("[config] 升级前自动备份配置文件失败: %v", err)
	}

	userData, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	var version verStruct
	if err := yaml.Unmarshal(userData, &version); err != nil {
		return err
	}
	if err := validateSourceVersion(version.Version); err != nil {
		return err
	}

	var userRoot yaml.Node
	if err := yaml.Unmarshal(userData, &userRoot); err != nil {
		return err
	}
	if _, err := migrateMusicPartnerRangeNodes(&userRoot); err != nil {
		return err
	}

	var defaultRoot yaml.Node
	if err := yaml.Unmarshal(defaultConfigByte, &defaultRoot); err != nil {
		return err
	}

	if len(defaultRoot.Content) > 0 && len(userRoot.Content) > 0 {
		mergeYAMLNodes(defaultRoot.Content[0], userRoot.Content[0])
	}

	output, err := yaml.Marshal(&defaultRoot)
	if err != nil {
		return err
	}

	return atomicfile.Write(cfgPath, output, 0644)
}

// 合并新模板前先展开旧字段，防止模板默认范围覆盖用户原有策略和目标。
func migrateMusicPartnerRangeNodes(root *yaml.Node) (bool, error) {
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	node := mappingNodeValue(root, "musicPartner")
	if node == nil || node.Tag == "!!null" {
		return false, nil
	}
	var partner MusicPartnerConf
	if err := node.Decode(&partner); err != nil {
		return false, fmt.Errorf("音乐合伙人配置格式无效: %w", err)
	}
	if err := validateMusicPartnerRangePresence(func(key string) (string, bool) {
		value := mappingNodeValue(node, key)
		if value == nil {
			return "", false
		}
		return value.Value, true
	}); err != nil {
		return false, err
	}
	if err := partner.Validate(); err != nil {
		return false, err
	}
	changed := false
	for _, field := range []struct {
		key   string
		value int
	}{{"scoreMin", partner.ScoreMin}, {"scoreMax", partner.ScoreMax}, {"extraLimitMin", partner.ExtraLimitMin}, {"extraLimitMax", partner.ExtraLimitMax}} {
		if mappingNodeValue(node, field.key) == nil {
			ensureMappingNodeValue(node, field.key, yaml.ScalarNode, "!!int").Value = strconv.Itoa(field.value)
			changed = true
		}
	}
	return changed, nil
}

// 本分支恢复自 1.2.2，拒绝将未公开源码版本的配置降级写回。
func validateSourceVersion(version string) error {
	core := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(version), "v"), "-", 2)[0]
	if core == "" {
		return nil
	}
	parts := strings.Split(core, ".")
	if len(parts) > 3 {
		return fmt.Errorf("无法识别配置版本，请为本分支使用独立数据目录")
	}
	baseline := []int{1, 2, 2}
	for i := 0; i < 3; i++ {
		n := 0
		if i < len(parts) {
			var err error
			n, err = strconv.Atoi(parts[i])
			if err != nil || n < 0 {
				return fmt.Errorf("无法识别配置版本，请为本分支使用独立数据目录")
			}
		}
		if n > baseline[i] {
			return fmt.Errorf("当前源码基于 1.2.2，不能降级读取较新版本配置，请使用独立数据目录")
		}
		if n < baseline[i] {
			return nil
		}
	}
	return nil
}

func mergeYAMLNodes(dest, src *yaml.Node) {
	if dest.Kind != src.Kind {
		return
	}
	switch dest.Kind {
	case yaml.DocumentNode:
		if len(dest.Content) > 0 && len(src.Content) > 0 {
			mergeYAMLNodes(dest.Content[0], src.Content[0])
		}
	case yaml.MappingNode:
		for i := 0; i < len(dest.Content); i += 2 {
			destKey := dest.Content[i].Value
			destVal := dest.Content[i+1]

			var srcVal *yaml.Node
			for j := 0; j < len(src.Content); j += 2 {
				if src.Content[j].Value == destKey {
					srcVal = src.Content[j+1]
					break
				}
			}

			if srcVal != nil {
				if destKey == "version" {
					continue
				}
				if destKey == "antiCheatTokens" || destKey == "comments" {
					destVal.Kind = srcVal.Kind
					destVal.Tag = srcVal.Tag
					destVal.Style = srcVal.Style
					destVal.Value = srcVal.Value
					destVal.Content = srcVal.Content
					destVal.FootComment = srcVal.FootComment
					continue
				}
				if destVal.Kind == yaml.MappingNode && srcVal.Kind == yaml.MappingNode {
					mergeYAMLNodes(destVal, srcVal)
				} else if destVal.Kind == yaml.SequenceNode && srcVal.Kind == yaml.SequenceNode {
					destVal.Content = srcVal.Content
				} else {
					destVal.Value = srcVal.Value
					destVal.Tag = srcVal.Tag
					destVal.Kind = srcVal.Kind
					destVal.Content = srcVal.Content
				}
			}
		}
	}
}

// MigrateConfigFile 自动将旧的 primary / enablePrimary 配置字段升级迁移为 main / enableMain
func MigrateConfigFile(cfgPath string) error {
	lock, err := acquireConfigWriteLock(cfgPath)
	if err != nil {
		return err
	}
	defer lock.Close()

	if _, err := BackupConfigFile(cfgPath, 2); err != nil {
		log.Warn("[config] 升级前自动备份配置文件失败: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}

	modified := migrateNode(&root)
	if !modified {
		return nil
	}

	output, err := yaml.Marshal(&root)
	if err != nil {
		return err
	}
	return atomicfile.Write(cfgPath, output, 0644)
}

func migrateNode(node *yaml.Node) bool {
	var modified bool
	if node.Kind == yaml.DocumentNode {
		for _, child := range node.Content {
			if migrateNode(child) {
				modified = true
			}
		}
		return modified
	}

	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valNode := node.Content[i+1]

			// Case 1: accounts mapping
			if keyNode.Value == "accounts" && valNode.Kind == yaml.MappingNode {
				for j := 0; j < len(valNode.Content); j += 2 {
					subKey := valNode.Content[j]
					if subKey.Value == "primary" {
						subKey.Value = "main"
						modified = true
					}
				}
			}

			// Case 2: playids mapping
			if keyNode.Value == "playids" && valNode.Kind == yaml.MappingNode {
				for j := 0; j < len(valNode.Content); j += 2 {
					subKey := valNode.Content[j]
					if subKey.Value == "enablePrimary" {
						subKey.Value = "enableMain"
						modified = true
					}
				}
			}

			// Case 3: sign mapping
			if keyNode.Value == "sign" && valNode.Kind == yaml.MappingNode {
				for j := 0; j < len(valNode.Content); j += 2 {
					subKey := valNode.Content[j]
					if subKey.Value == "enablePrimary" {
						subKey.Value = "enableMain"
						modified = true
					}
				}
			}

			// Case 4: Rename musicianVip to musician at top-level
			if keyNode.Value == "musicianVip" {
				keyNode.Value = "musician"
				modified = true
			}

			// Case 5: Rename old task keys
			if keyNode.Value == "task" && valNode.Kind == yaml.MappingNode {
				for j := 0; j < len(valNode.Content); j += 2 {
					subKey := valNode.Content[j]
					switch subKey.Value {
					case "musicianVip", "musician":
						subKey.Value = "musician-sign"
						modified = true
					case "fansGroup":
						subKey.Value = "fansgroup"
						modified = true
					}
				}
			}

			// Case 6: Rename top-level fansGroup to fansgroup
			if keyNode.Value == "fansGroup" {
				keyNode.Value = "fansgroup"
				modified = true
			}

			if migrateNode(valNode) {
				modified = true
			}
		}
	}

	if node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			if migrateNode(child) {
				modified = true
			}
		}
	}

	return modified
}

// BackupConfigFile 在配置文件的同级 backups 子目录下创建备份，并自动保留最新的 maxBackups 份
func BackupConfigFile(cfgPath string, maxBackups int) (string, error) {
	if cfgPath == "" || cfgPath == "default" {
		return "", nil
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return "", nil
	}

	if maxBackups <= 0 {
		maxBackups = 2
	}

	cfgDir := filepath.Dir(cfgPath)
	backupDir := filepath.Join(cfgDir, "backups")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}

	baseName := filepath.Base(cfgPath)
	ext := filepath.Ext(baseName)
	prefix := strings.TrimSuffix(baseName, ext)

	// 检查已有的备份文件
	pattern := filepath.Join(backupDir, fmt.Sprintf("%s_*%s", prefix, ext))
	matches, _ := filepath.Glob(pattern)

	// 检查最新备份内容是否与当前文件完全一致（避免同一更新流程中重复生成冗余备份）
	if len(matches) > 0 {
		sort.Strings(matches)
		latestBackup := matches[len(matches)-1]
		if latestData, err := os.ReadFile(latestBackup); err == nil {
			if bytes.Equal(latestData, data) {
				return latestBackup, nil
			}
		}
	}

	timestamp := time.Now().Format("20060102_150405")
	backupName := fmt.Sprintf("%s_%s%s", prefix, timestamp, ext)
	backupPath := filepath.Join(backupDir, backupName)

	if _, err := os.Stat(backupPath); err == nil {
		backupName = fmt.Sprintf("%s_%s_%03d%s", prefix, timestamp, time.Now().Nanosecond()/1e6, ext)
		backupPath = filepath.Join(backupDir, backupName)
	}

	if err := atomicfile.Write(backupPath, data, 0644); err != nil {
		return "", fmt.Errorf("write backup file: %w", err)
	}

	log.Info("[config] 升级前已自动备份配置文件至: %s", backupPath)

	cleanOldBackups(backupDir, prefix, ext, maxBackups)

	return backupPath, nil
}

func cleanOldBackups(backupDir, prefix, ext string, maxBackups int) {
	pattern := filepath.Join(backupDir, fmt.Sprintf("%s_*%s", prefix, ext))
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) <= maxBackups {
		return
	}

	type fileItem struct {
		path    string
		modTime time.Time
	}
	items := make([]fileItem, 0, len(matches))
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err == nil {
			items = append(items, fileItem{path: m, modTime: fi.ModTime()})
		}
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].modTime.Equal(items[j].modTime) {
			return items[i].path < items[j].path
		}
		return items[i].modTime.Before(items[j].modTime)
	})

	toDelete := len(items) - maxBackups
	for i := 0; i < toDelete; i++ {
		_ = os.Remove(items[i].path)
	}
}

// UpdateAccountsInFile 更新配置文件中的 accounts 并为每个账号添加昵称注释，同时保持原有文件的注释和排版
func UpdateAccountsInFile(cfgPath string, mainPath string, mainNickname string, secondaryPaths []string, secondaryNicknames []string) error {
	lock, err := acquireConfigWriteLock(cfgPath)
	if err != nil {
		return err
	}
	defer lock.Close()
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}

	var docMapping *yaml.Node
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		docMapping = root.Content[0]
	}

	if docMapping == nil || docMapping.Kind != yaml.MappingNode {
		docMapping = &yaml.Node{Kind: yaml.MappingNode}
		root.Kind = yaml.DocumentNode
		root.Content = []*yaml.Node{docMapping}
	}

	// 查找或创建 'accounts' 映射
	var accountsMapping *yaml.Node
	for i := 0; i < len(docMapping.Content); i += 2 {
		if docMapping.Content[i].Value == "accounts" {
			if docMapping.Content[i+1].Kind == yaml.MappingNode {
				accountsMapping = docMapping.Content[i+1]
			}
			break
		}
	}

	if accountsMapping == nil {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: "accounts"}
		accountsMapping = &yaml.Node{Kind: yaml.MappingNode}
		docMapping.Content = append(docMapping.Content, keyNode, accountsMapping)
	}

	// 收集现有的注释以便复用
	oldComments := make(map[string]string)
	for i := 0; i < len(accountsMapping.Content); i += 2 {
		key := accountsMapping.Content[i].Value
		valNode := accountsMapping.Content[i+1]
		if key == "main" || key == "primary" {
			if valNode.LineComment != "" {
				oldComments["main"] = valNode.LineComment
			}
		}
		if key == "secondary" && valNode.Kind == yaml.SequenceNode {
			for _, item := range valNode.Content {
				if item.LineComment != "" {
					oldComments[item.Value] = item.LineComment
				}
			}
		}
	}

	// 更新 'main' 字段
	var mainKeyNode, mainValNode *yaml.Node
	for i := 0; i < len(accountsMapping.Content); i += 2 {
		if accountsMapping.Content[i].Value == "main" || accountsMapping.Content[i].Value == "primary" {
			mainKeyNode = accountsMapping.Content[i]
			mainValNode = accountsMapping.Content[i+1]
			mainKeyNode.Value = "main" // 确保是 main
			break
		}
	}

	if mainKeyNode == nil {
		mainKeyNode = &yaml.Node{Kind: yaml.ScalarNode, Value: "main"}
		mainValNode = &yaml.Node{Kind: yaml.ScalarNode}
		accountsMapping.Content = append(accountsMapping.Content, mainKeyNode, mainValNode)
	}

	mainValNode.Kind = yaml.ScalarNode
	mainValNode.Value = mainPath
	if mainNickname != "" {
		mainValNode.LineComment = "# 昵称: " + mainNickname
	} else if oldComm, ok := oldComments["main"]; ok {
		mainValNode.LineComment = oldComm
	} else {
		mainValNode.LineComment = ""
	}

	// 更新 'secondary' 字段
	var secKeyNode, secValNode *yaml.Node
	for i := 0; i < len(accountsMapping.Content); i += 2 {
		if accountsMapping.Content[i].Value == "secondary" {
			secKeyNode = accountsMapping.Content[i]
			secValNode = accountsMapping.Content[i+1]
			break
		}
	}

	if secKeyNode == nil {
		secKeyNode = &yaml.Node{Kind: yaml.ScalarNode, Value: "secondary"}
		secValNode = &yaml.Node{Kind: yaml.SequenceNode}
		accountsMapping.Content = append(accountsMapping.Content, secKeyNode, secValNode)
	}

	secValNode.Kind = yaml.SequenceNode
	secValNode.Content = nil
	for i, path := range secondaryPaths {
		itemNode := &yaml.Node{Kind: yaml.ScalarNode, Value: path}
		var comment string
		if i < len(secondaryNicknames) && secondaryNicknames[i] != "" {
			comment = "# 昵称: " + secondaryNicknames[i]
		} else if oldComm, ok := oldComments[path]; ok {
			comment = oldComm
		}

		if comment != "" {
			trimmed := strings.TrimSpace(comment)
			if !strings.HasPrefix(trimmed, "#") {
				itemNode.LineComment = "# " + trimmed
			} else {
				itemNode.LineComment = comment
			}
		}
		secValNode.Content = append(secValNode.Content, itemNode)
	}

	output, err := yaml.Marshal(&root)
	if err != nil {
		return err
	}
	return atomicfile.Write(cfgPath, output, 0644)
}

// UpdateAccountInFile merges one login result into the latest config while holding the cross-process write lock.
func UpdateAccountInFile(cfgPath, accountPath, nickname string, main bool) error {
	lock, err := acquireConfigWriteLock(cfgPath)
	if err != nil {
		return err
	}
	defer lock.Close()
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return err
	}
	if err := ApplyAccountUpdate(&document, accountPath, nickname, main); err != nil {
		return err
	}
	output, err := yaml.Marshal(&document)
	if err != nil {
		return err
	}
	return atomicfile.Write(cfgPath, output, 0644)
}

// ApplyAccountUpdate changes only the target account entry and preserves unrelated YAML nodes and comments.
func ApplyAccountUpdate(document *yaml.Node, accountPath, nickname string, main bool) error {
	return ApplyAccountProfileUpdate(document, accountPath, nickname, "", main)
}

// ApplyAccountProfileUpdate changes only the target account entry and preserves unrelated YAML nodes and comments.
func ApplyAccountProfileUpdate(document *yaml.Node, accountPath, nickname, avatarURL string, main bool) error {
	if document == nil || document.Kind != yaml.DocumentNode || len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("config root must be a mapping")
	}
	root := document.Content[0]
	accounts := mappingNodeValue(root, "accounts")
	if accounts == nil {
		accounts = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "accounts"}, accounts)
	}
	if accounts.Kind != yaml.MappingNode {
		return fmt.Errorf("accounts must be a mapping")
	}
	if main {
		value := ensureMappingNodeValue(accounts, "main", yaml.ScalarNode, "!!str")
		value.Value = accountPath
		setAccountProfile(value, nickname, avatarURL)
		return nil
	}
	secondary := ensureMappingNodeValue(accounts, "secondary", yaml.SequenceNode, "!!seq")
	for _, item := range secondary.Content {
		if item.Kind == yaml.ScalarNode && sameAccountPath(item.Value, accountPath) {
			setAccountProfile(item, nickname, avatarURL)
			return nil
		}
	}
	item := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: accountPath}
	setAccountProfile(item, nickname, avatarURL)
	secondary.Content = append(secondary.Content, item)
	return nil
}

func mappingNodeValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func ensureMappingNodeValue(mapping *yaml.Node, key string, kind yaml.Kind, tag string) *yaml.Node {
	if value := mappingNodeValue(mapping, key); value != nil {
		if value.Kind != kind {
			value.Kind = kind
			value.Tag = tag
			value.Value = ""
			value.Content = nil
		}
		return value
	}
	value := &yaml.Node{Kind: kind, Tag: tag}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
	return value
}

func setAccountNickname(node *yaml.Node, nickname string) {
	setAccountProfile(node, nickname, "")
}

func setAccountProfile(node *yaml.Node, nickname, avatarURL string) {
	existingAvatar := accountCommentValue(node.LineComment, "头像")
	nickname = sanitizeAccountCommentValue(nickname)
	avatarURL = sanitizeAccountCommentValue(avatarURL)
	if avatarURL == "" {
		avatarURL = existingAvatar
	}
	parts := make([]string, 0, 2)
	if nickname != "" {
		parts = append(parts, "昵称: "+nickname)
	}
	if avatarURL != "" {
		parts = append(parts, "头像: "+avatarURL)
	}
	if len(parts) > 0 {
		node.LineComment = "# " + strings.Join(parts, " | ")
	}
}

func sanitizeAccountCommentValue(value string) string {
	value = strings.NewReplacer("\r", " ", "\n", " ", "|", " ").Replace(strings.TrimSpace(value))
	return strings.Join(strings.Fields(value), " ")
}

func accountCommentValue(comment, key string) string {
	comment = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(comment), "#"))
	for _, part := range strings.Split(comment, "|") {
		part = strings.TrimSpace(part)
		for _, separator := range []string{":", "："} {
			if label, value, ok := strings.Cut(part, separator); ok && strings.TrimSpace(label) == key {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func sameAccountPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func acquireConfigWriteLock(path string) (*filelock.Lock, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lock, err := filelock.Acquire(ctx, path+".lock")
	if err != nil {
		return nil, fmt.Errorf("acquire config lock: %w", err)
	}
	return lock, nil
}
