package webui

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/3899/ncmm/internal/rewardevent"
)

var (
	accountStartPattern         = regexp.MustCompile(`^\[(?:sign|musician(?: sign| vip)?)\]\s+>+\s+开始(?:主账号|辅助账号).*?\((.+)\)\s+<+$`)
	taskAccountPattern          = regexp.MustCompile(`^\[task\]\s+>+\s+账号\s+\((.+)\)\s+开始执行\s+\[[^]]+\]\s+(?:(?:子任务组|任务)\s+)?<+$`)
	partnerAccountPattern       = regexp.MustCompile(`\[music-partner\]\s+>+\s+账号\s+\((.+)\)\s+开始评分\s+<+$`)
	partnerLegacyAccountPattern = regexp.MustCompile(`\[music-partner\]\s+开始处理账号\s+(.+)$`)
	partnerDeltaPattern         = regexp.MustCompile(`\[music-partner\]\s+本期积分：(-?\d+) -> (-?\d+)，变化 ([+-]\d+)（平台账面变化）`)
	accountInfoPattern          = regexp.MustCompile(`\[当前账号信息\]\s*Uid:\s*([^|]+?)\s*\|\s*昵称:\s*([^|]+?)\s*\|\s*等级:\s*(.+?)(?:\s*\|\s*头像:\s*(\S+))?\s*$`)
	yunbeiRewardPattern         = regexp.MustCompile(`获得云贝(?:数量)?[：:]\s*(\d+)`)
	yunbeiBalancePattern        = regexp.MustCompile(`\[云贝余额\]\s*当前可用云贝:\s*(\d+)`)
	dailyEarningsPattern        = regexp.MustCompile(`^\[今日收益\]\s*云贝\s*(\+\d+|--)\s*/\s*(\d+|--)(?:\s*\|\s*(.+))?$`)
	growthSummaryPattern        = regexp.MustCompile(`^成长值\s*(\d+)\s*/\s*(\d+)$`)
	growthFinalPattern          = regexp.MustCompile(`黑胶成长值最终状态:\s*今日已获得\s*(\d+)[，,]\s*当前成长值\s*(\d+)`)
	effectivePlayPattern        = regexp.MustCompile(`当前进度:\s*(\d+)/(\d+)[，,]\s*今日尚缺有效播放`)
)

func parseRunRewards(path string) ([]RunReward, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return parseRunRewardsReader(file), nil
}

func parseRunRewardsReader(reader io.Reader) []RunReward {
	byAccount := make(map[string]*RunReward)
	yunbeiSummarySeen := make(map[string]bool)
	structuredYunbeiSeen := make(map[string]bool)
	structuredVIPSeen := make(map[string]bool)
	structuredPartnerSeen := make(map[string]bool)
	partnerUnknown := make(map[string]bool)
	var order []string
	current := ""
	ensureAccount := func(account string) (string, *RunReward) {
		key := normalizeRewardAccount(account)
		if _, exists := byAccount[key]; !exists {
			byAccount[key] = &RunReward{Account: account}
			order = append(order, key)
		}
		return key, byAccount[key]
	}
	getCurrent := func() *RunReward {
		if current == "" {
			return nil
		}
		return byAccount[current]
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if event, matched, err := rewardevent.ParseLine(line); matched {
			if err != nil {
				continue
			}
			key, reward := ensureAccount(event.Account)
			current = key
			switch event.Domain {
			case rewardevent.DomainYunbei:
				reward.Yunbei = int(event.Yunbei.Today)
				reward.YunbeiKnown = event.Yunbei.TodayKnown
				reward.YunbeiCumulative = true
				reward.YunbeiBalance = int(event.Yunbei.Balance)
				reward.YunbeiBalanceKnown = event.Yunbei.BalanceKnown
				yunbeiSummarySeen[key] = true
				structuredYunbeiSeen[key] = true
			case rewardevent.DomainVIP:
				reward.VIP = event.VIP.Enabled
				reward.VIPKnown = event.VIP.StatusKnown
				reward.GrowthToday = int(event.VIP.Today)
				reward.GrowthTotal = int(event.VIP.Total)
				reward.GrowthKnown = event.VIP.GrowthKnown
				structuredVIPSeen[key] = true
			case rewardevent.DomainMusicPartner:
				reward.musicPartnerObserved = true
				if !structuredPartnerSeen[key] {
					reward.MusicPartnerDelta = 0
					partnerUnknown[key] = false
				}
				structuredPartnerSeen[key] = true
				if event.MusicPartner.Known {
					reward.MusicPartnerDelta += event.MusicPartner.Delta
				} else {
					partnerUnknown[key] = true
				}
				reward.MusicPartnerKnown = !partnerUnknown[key]
			}
			continue
		}
		account := ""
		if match := accountStartPattern.FindStringSubmatch(line); match != nil {
			account = strings.TrimSpace(match[1])
		} else if match := taskAccountPattern.FindStringSubmatch(line); match != nil {
			account = strings.TrimSpace(match[1])
		} else if match := partnerAccountPattern.FindStringSubmatch(line); match != nil {
			account = strings.TrimSpace(match[1])
		} else if match := partnerLegacyAccountPattern.FindStringSubmatch(line); match != nil {
			account = strings.TrimSpace(match[1])
		}
		if account != "" {
			key, _ := ensureAccount(account)
			current = key
			continue
		}

		reward := getCurrent()
		if reward == nil {
			continue
		}
		if !structuredPartnerSeen[current] {
			if match := partnerDeltaPattern.FindStringSubmatch(line); match != nil {
				before, beforeErr := strconv.ParseInt(match[1], 10, 64)
				after, afterErr := strconv.ParseInt(match[2], 10, 64)
				delta, deltaErr := strconv.ParseInt(match[3], 10, 64)
				if beforeErr == nil && afterErr == nil && deltaErr == nil && after-before == delta {
					reward.musicPartnerObserved = true
					reward.MusicPartnerDelta += delta
					reward.MusicPartnerKnown = !partnerUnknown[current]
				}
				continue
			}
			if strings.Contains(line, "[music-partner]") && (strings.Contains(line, "本期积分：") && strings.Contains(line, "差值未计算") || strings.Contains(line, "评分后积分未查询")) {
				reward.musicPartnerObserved = true
				partnerUnknown[current] = true
				reward.MusicPartnerKnown = false
			}
		}
		if match := accountInfoPattern.FindStringSubmatch(line); match != nil {
			reward.UID = strings.TrimSpace(match[1])
			reward.Nickname = strings.TrimSpace(match[2])
			reward.Identity = strings.TrimSpace(match[3])
			if len(match) > 4 {
				reward.AvatarURL = strings.TrimSpace(match[4])
			}
			reward.CookieValid = true
			reward.CookieKnown = true
			continue
		}
		if strings.Contains(line, "Cookie 已失效") || strings.Contains(line, "Cookie失效") {
			reward.CookieValid = false
			reward.CookieKnown = true
			continue
		}
		if strings.Contains(line, "已认证音乐人") || strings.Contains(line, "[账号身份] 音乐人") {
			reward.Musician = true
			reward.MusicianKnown = true
			continue
		}
		if strings.Contains(line, "当前账号不是音乐人") {
			reward.Musician = false
			reward.MusicianKnown = true
			continue
		}
		if match := dailyEarningsPattern.FindStringSubmatch(line); match != nil {
			if !structuredYunbeiSeen[current] {
				reward.Yunbei = 0
				reward.YunbeiKnown = match[1] != "--"
				if reward.YunbeiKnown {
					reward.Yunbei, _ = strconv.Atoi(strings.TrimPrefix(match[1], "+"))
				}
				reward.YunbeiCumulative = true
				reward.YunbeiBalance = 0
				reward.YunbeiBalanceKnown = match[2] != "--"
				if reward.YunbeiBalanceKnown {
					reward.YunbeiBalance, _ = strconv.Atoi(match[2])
				}
				yunbeiSummarySeen[current] = true
			}

			if !structuredVIPSeen[current] {
				suffix := strings.TrimSpace(match[3])
				if growth := growthSummaryPattern.FindStringSubmatch(suffix); growth != nil {
					reward.GrowthToday, _ = strconv.Atoi(growth[1])
					reward.GrowthTotal, _ = strconv.Atoi(growth[2])
					reward.GrowthKnown = true
					reward.VIP = true
					reward.VIPKnown = true
				} else if suffix == "VIP 账号" {
					reward.GrowthToday = 0
					reward.GrowthTotal = 0
					reward.GrowthKnown = false
					reward.VIP = true
					reward.VIPKnown = true
				} else if suffix == "非 VIP 账号" {
					reward.GrowthToday = 0
					reward.GrowthTotal = 0
					reward.GrowthKnown = false
					reward.VIP = false
					reward.VIPKnown = true
				}
			}
			continue
		}
		if match := yunbeiRewardPattern.FindStringSubmatch(line); match != nil {
			if yunbeiSummarySeen[current] {
				continue
			}
			value, _ := strconv.Atoi(match[1])
			reward.Yunbei += value
			reward.YunbeiKnown = true
			continue
		}
		if match := yunbeiBalancePattern.FindStringSubmatch(line); match != nil {
			reward.YunbeiBalance, _ = strconv.Atoi(match[1])
			reward.YunbeiBalanceKnown = true
			continue
		}
		if strings.Contains(line, "云贝签到成功") || strings.Contains(line, "云贝今天已签到过") {
			reward.Signed = true
			reward.SignKnown = true
			continue
		}
		if strings.Contains(line, "云贝签到失败") {
			reward.SignKnown = true
			continue
		}
		if match := growthFinalPattern.FindStringSubmatch(line); match != nil && !structuredVIPSeen[current] {
			reward.GrowthToday, _ = strconv.Atoi(match[1])
			reward.GrowthTotal, _ = strconv.Atoi(match[2])
			reward.GrowthKnown = true
			reward.VIP = true
			reward.VIPKnown = true
			continue
		}
		if strings.Contains(line, "暂无会员权益") && strings.Contains(line, "VIP 状态: 0") && !structuredVIPSeen[current] {
			reward.VIP = false
			reward.VIPKnown = true
			continue
		}
		if match := effectivePlayPattern.FindStringSubmatch(line); match != nil {
			reward.EffectivePlays, _ = strconv.Atoi(match[1])
			reward.EffectiveTarget, _ = strconv.Atoi(match[2])
			reward.EffectiveKnown = true
		}
	}

	result := make([]RunReward, 0, len(order))
	for _, key := range order {
		result = append(result, *byAccount[key])
	}
	return result
}

func normalizeRewardAccount(account string) string {
	account = strings.ReplaceAll(strings.TrimSpace(account), `\`, "/")
	if account == "" {
		return ""
	}
	account = path.Clean(account)
	// 只有明确带盘符的 Windows 路径忽略大小写；Unix 和相对路径区分大小写。
	if len(account) >= 3 && account[1:3] == ":/" && (account[0] >= 'A' && account[0] <= 'Z' || account[0] >= 'a' && account[0] <= 'z') {
		return strings.ToLower(account)
	}
	return account
}

// 旧日志可能只保留部分阶段，回填新增收益时保留已缓存的账号资料和其他已知结果。
func mergeRunRewards(cached, parsed []RunReward) []RunReward {
	result := append([]RunReward(nil), cached...)
	positions := make(map[string]int, len(result))
	for i, reward := range result {
		positions[normalizeRewardAccount(reward.Account)] = i
	}
	for _, reward := range parsed {
		key := normalizeRewardAccount(reward.Account)
		if index, ok := positions[key]; ok {
			previous := result[index]
			reward.Account = previous.Account
			if reward.UID == "" {
				reward.UID = previous.UID
			}
			if reward.Nickname == "" {
				reward.Nickname = previous.Nickname
			}
			if reward.AvatarURL == "" {
				reward.AvatarURL = previous.AvatarURL
			}
			if reward.Identity == "" {
				reward.Identity = previous.Identity
			}
			if !reward.CookieKnown {
				reward.CookieKnown, reward.CookieValid = previous.CookieKnown, previous.CookieValid
			}
			if !reward.VIPKnown {
				reward.VIPKnown, reward.VIP = previous.VIPKnown, previous.VIP
			}
			if !reward.MusicianKnown {
				reward.MusicianKnown, reward.Musician = previous.MusicianKnown, previous.Musician
			}
			if !reward.YunbeiKnown {
				reward.YunbeiKnown, reward.Yunbei, reward.YunbeiCumulative = previous.YunbeiKnown, previous.Yunbei, previous.YunbeiCumulative
			}
			if !reward.YunbeiBalanceKnown {
				reward.YunbeiBalanceKnown, reward.YunbeiBalance = previous.YunbeiBalanceKnown, previous.YunbeiBalance
			}
			if !reward.GrowthKnown {
				reward.GrowthKnown, reward.GrowthToday, reward.GrowthTotal = previous.GrowthKnown, previous.GrowthToday, previous.GrowthTotal
			}
			if !reward.EffectiveKnown {
				reward.EffectiveKnown, reward.EffectivePlays, reward.EffectiveTarget = previous.EffectiveKnown, previous.EffectivePlays, previous.EffectiveTarget
			}
			if !reward.SignKnown {
				reward.SignKnown, reward.Signed = previous.SignKnown, previous.Signed
			}
			if !reward.musicPartnerObserved {
				reward.MusicPartnerKnown, reward.MusicPartnerDelta = previous.MusicPartnerKnown, previous.MusicPartnerDelta
			}
			reward.musicPartnerObserved = false
			result[index] = reward
		} else {
			reward.musicPartnerObserved = false
			positions[key] = len(result)
			result = append(result, reward)
		}
	}
	return result
}

func hideStructuredRewardEvents(data []byte) []byte {
	lines := bytes.Split(data, []byte{'\n'})
	visible := lines[:0]
	for _, line := range lines {
		if _, matched, _ := rewardevent.ParseLine(string(line)); matched {
			continue
		}
		visible = append(visible, line)
	}
	return bytes.Join(visible, []byte{'\n'})
}
