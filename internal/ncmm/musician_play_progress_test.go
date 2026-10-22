package ncmm

import (
	"bytes"
	"strings"
	"testing"

	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/config"
)

func musicianPlayTestCommand() (*Musician, *bytes.Buffer) {
	c := NewMusician(&Root{Cfg: &config.Config{Musician: &config.MusicianConf{}}}, nil)
	output := &bytes.Buffer{}
	c.cmd.SetOut(output)
	c.cmd.SetErr(output)
	return c, output
}

func TestMusicianPlayDoesNotTreatHistoricalTotalAsTaskProgress(t *testing.T) {
	for _, code := range []string{"mission_code_recently_play_count", "mission_code_recently_play_effect_count_v2"} {
		t.Run(code, func(t *testing.T) {
			c, output := musicianPlayTestCommand()
			c.root.Cfg.Musician.Play.IDs = "123"
			target := 650
			if strings.HasSuffix(code, "_v2") {
				target = 100
			}
			sub := eapi.MusicianVipSubTask{Name: "上传歌曲的近30日任务内有效播放达650次", MissionCode: code, MissionStatus: 50, TotalCompleteNum: target, ProgressRate: 7}
			// 不配置账号阻止真实播放；历史播放量很大也必须继续检查执行条件。
			err := c.handlePlayTask(t.Context(), nil, sub, 18189)
			if err == nil || !strings.Contains(err.Error(), "未配置任何账号") {
				t.Fatalf("unfinished task must reach account validation, err=%v, output=%s", err, output)
			}
			if strings.Contains(output.String(), "已达标") {
				t.Fatalf("historical total incorrectly marked task complete: %s", output)
			}
		})
	}
}

func TestMusicianVipPhaseRecognizesV2AndRespectsPlaySwitch(t *testing.T) {
	c, output := musicianPlayTestCommand()
	disabled := false
	c.root.Cfg.Musician.EnableVipPlay = &disabled
	resp := &eapi.MusicianVipTasksResp{}
	resp.Data.RecentPlayCount30 = 18189
	resp.Data.FurtherTask = &eapi.MusicianVipFurtherTask{Children: []eapi.MusicianVipSubTask{{
		Name: "上传歌曲的近30日任务内有效播放达650次", MissionCode: "mission_code_recently_play_effect_count_v2", MissionStatus: 50, TotalCompleteNum: 100, ProgressRate: 7,
	}}}
	c.doVipPhase(t.Context(), &musicianContext{resp: resp}, "test-account")
	if !strings.Contains(output.String(), "enableVipPlay = false") || strings.Contains(output.String(), "未知任务类型") {
		t.Fatalf("v2 task not routed to existing play switch: %s", output)
	}
}

func TestMusicianCompletedPlayDoesNotRequireSongConfiguration(t *testing.T) {
	c, output := musicianPlayTestCommand()
	err := c.handlePlayTask(t.Context(), nil, eapi.MusicianVipSubTask{MissionCode: "mission_code_recently_play_count", MissionStatus: 100}, 0)
	if err != nil {
		t.Fatalf("completed task should not require a song pool: %v, output=%s", err, output)
	}
}

func TestCalcNeededPlayCount(t *testing.T) {
	tests := []struct {
		name      string
		sub       eapi.MusicianVipSubTask
		want      int64
		wantError bool
	}{
		{name: "旧协议使用任务自身计数", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayCountCode, TotalCompleteNum: 650, ProgressRate: 7}, want: 643},
		{name: "旧协议已完成", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayCountCode, TotalCompleteNum: 650, ProgressRate: 650}},
		{name: "旧协议进度超出目标", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayCountCode, TotalCompleteNum: 650, ProgressRate: 700}},
		{name: "官方状态已完成", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayPercentCode, MissionStatus: 100}},
		{name: "新版真实样例向上取整", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayPercentCode, Name: "上传歌曲的近30日任务内有效播放达650次", TotalCompleteNum: 100, ProgressRate: 7}, want: 605},
		{name: "百分比零进度", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayPercentCode, Name: "有效播放达650次", TotalCompleteNum: 100}, want: 650},
		{name: "百分比满进度无需目标文案", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayPercentCode, TotalCompleteNum: 100, ProgressRate: 100}},
		{name: "百分比尾数向上取整", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayPercentCode, Name: "有效播放达650次", TotalCompleteNum: 100, ProgressRate: 99}, want: 7},
		{name: "描述中的目标", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayPercentCode, Name: "近30日播放任务", Desc: "有效播放次数达到 1,000 次", TotalCompleteNum: 100, ProgressRate: 25}, want: 750},
		{name: "相同目标不冲突", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayPercentCode, Name: "有效播放达650次", Desc: "播放量达到650次", TotalCompleteNum: 100, ProgressRate: 50}, want: 325},
		{name: "目标缺失不能默认650", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayPercentCode, Name: "近30日播放任务", TotalCompleteNum: 100, ProgressRate: 7}, wantError: true},
		{name: "目标文案冲突", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayPercentCode, Name: "有效播放达650次", Desc: "播放达1000次", TotalCompleteNum: 100, ProgressRate: 7}, wantError: true},
		{name: "负数进度", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayPercentCode, Name: "有效播放达650次", TotalCompleteNum: 100, ProgressRate: -1}, wantError: true},
		{name: "百分比越界", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayPercentCode, Name: "有效播放达650次", TotalCompleteNum: 100, ProgressRate: 101}, wantError: true},
		{name: "百分比基数变化需重新核实", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayPercentCode, Name: "有效播放达650次", TotalCompleteNum: 650, ProgressRate: 7}, wantError: true},
		{name: "零目标", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayPercentCode, Name: "有效播放达0次", TotalCompleteNum: 100}, wantError: true},
		{name: "错误千位分隔", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayPercentCode, Name: "有效播放达6,50次", TotalCompleteNum: 100}, wantError: true},
		{name: "目标溢出", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayPercentCode, Name: "有效播放达9223372036854775808次", TotalCompleteNum: 100}, wantError: true},
		{name: "大目标不溢出", sub: eapi.MusicianVipSubTask{MissionCode: musicianPlayPercentCode, Name: "有效播放达9223372036854775807次", TotalCompleteNum: 100, ProgressRate: 50}, want: 4611686018427387904},
		{name: "未知协议不猜测", sub: eapi.MusicianVipSubTask{MissionCode: "unknown", Name: "有效播放达650次", TotalCompleteNum: 100}, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := calcNeededPlayCount(tt.sub)
			if (err != nil) != tt.wantError || got != tt.want {
				t.Fatalf("calcNeededPlayCount = %d, %v; want %d, error=%v", got, err, tt.want, tt.wantError)
			}
		})
	}
}
