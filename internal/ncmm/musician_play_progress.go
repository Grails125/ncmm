package ncmm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/3899/ncmm/api/eapi"
)

const (
	musicianPlayCountCode   = "mission_code_recently_play_count"
	musicianPlayPercentCode = "mission_code_recently_play_effect_count_v2"
)

// v2 的总进度是 100，播放目标来自任务文案，不能把“近30日”中的 30 当目标。
var musicianPlayTargetPattern = regexp.MustCompile(`播放(?:量|次数)?\s*(?:达到|达|满|累计)?\s*([0-9][0-9,]*)\s*次`)
var musicianPlayTargetNumberPattern = regexp.MustCompile(`^(?:[1-9][0-9]*|[1-9][0-9]{0,2}(?:,[0-9]{3})+)$`)

func isMusicianPlayTask(code string) bool {
	return code == musicianPlayCountCode || code == musicianPlayPercentCode
}

func musicianPlayTarget(sub eapi.MusicianVipSubTask) (int64, error) {
	var target int64
	for _, text := range []string{sub.Name, sub.Desc, sub.TaskProgressText} {
		for _, match := range musicianPlayTargetPattern.FindAllStringSubmatch(text, -1) {
			if !musicianPlayTargetNumberPattern.MatchString(match[1]) {
				return 0, fmt.Errorf("播放任务目标格式无效，已跳过播放")
			}
			value, err := strconv.ParseInt(strings.ReplaceAll(match[1], ",", ""), 10, 64)
			if err != nil || value <= 0 {
				return 0, fmt.Errorf("播放任务目标超出有效范围，已跳过播放")
			}
			if target != 0 && target != value {
				return 0, fmt.Errorf("播放任务文案中的目标不一致，已跳过播放")
			}
			target = value
		}
	}
	if target == 0 {
		return 0, fmt.Errorf("无法识别新版播放任务的目标次数，已跳过播放")
	}
	return target, nil
}

// calcNeededPlayCount 只使用当前任务进度；历史总播放量不是本期有效播放数。
func calcNeededPlayCount(sub eapi.MusicianVipSubTask) (int64, error) {
	if !isMusicianPlayTask(sub.MissionCode) {
		return 0, fmt.Errorf("暂不支持该播放任务类型")
	}
	if sub.MissionStatus == 100 {
		return 0, nil
	}
	if sub.ProgressRate < 0 || sub.TotalCompleteNum <= 0 {
		return 0, fmt.Errorf("播放任务进度无效，已跳过播放")
	}
	if sub.MissionCode == musicianPlayCountCode {
		return max(int64(sub.TotalCompleteNum)-int64(sub.ProgressRate), 0), nil
	}
	if sub.TotalCompleteNum != 100 || sub.ProgressRate > 100 {
		return 0, fmt.Errorf("新版播放任务百分比进度无效，已跳过播放")
	}
	if sub.ProgressRate == 100 {
		return 0, nil
	}
	target, err := musicianPlayTarget(sub)
	if err != nil {
		return 0, err
	}
	remainingPercent := int64(100 - sub.ProgressRate)
	// 向上取整且避免大目标乘以百分比时溢出；结果只是本轮上报量估算。
	return target/100*remainingPercent + (target%100*remainingPercent+99)/100, nil
}
