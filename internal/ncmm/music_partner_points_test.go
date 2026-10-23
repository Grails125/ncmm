package ncmm

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/internal/partnerpoints"
	"github.com/3899/ncmm/internal/rewardevent"
)

func partnerPoint(value int64) *int64 { return &value }

func partnerPointsSnapshot(period, week, integral, weekly int64) *partnerpoints.Snapshot {
	return &partnerpoints.Snapshot{Period: partnerPoint(period), Week: partnerPoint(week), Integral: partnerPoint(integral), CurrentWeekIntegral: partnerPoint(weekly)}
}

func TestMusicPartnerStructuredPointChanges(t *testing.T) {
	t.Setenv("NCMM_WEB_CHILD", "1")
	for _, tt := range []struct {
		name          string
		before, after *partnerpoints.Snapshot
		delta         int64
		known         bool
	}{
		{"increase", partnerPointsSnapshot(73, 2, 100, 40), partnerPointsSnapshot(73, 2, 109, 49), 9, true},
		{"decrease", partnerPointsSnapshot(73, 2, 100, 40), partnerPointsSnapshot(73, 2, 95, 35), -5, true},
		{"zero", partnerPointsSnapshot(73, 2, 100, 40), partnerPointsSnapshot(73, 2, 100, 40), 0, true},
		{"new week", partnerPointsSnapshot(73, 2, 100, 40), partnerPointsSnapshot(73, 3, 109, 5), 9, true},
		{"new period", partnerPointsSnapshot(73, 2, 100, 40), partnerPointsSnapshot(74, 1, 9, 9), 0, false},
		{"missing period", &partnerpoints.Snapshot{Integral: partnerPoint(100)}, partnerPointsSnapshot(73, 2, 109, 49), 0, false},
		{"missing after", partnerPointsSnapshot(73, 2, 100, 40), nil, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			command, _ := partnerTestCommand(t)
			var output bytes.Buffer
			command.Command().SetOut(&output)
			reads := 0
			err := command.runWithPoints(t.Context(), "sub/fan.json", func(context.Context) (*partnerpoints.Snapshot, error) {
				reads++
				if reads == 1 {
					return tt.before, nil
				}
				return tt.after, nil
			}, func(context.Context) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, line := range strings.Split(output.String(), "\n") {
				event, matched, err := rewardevent.ParseLine(line)
				if !matched {
					continue
				}
				count++
				if err != nil || event.Account != "sub/fan.json" || event.MusicPartner == nil || event.MusicPartner.Delta != tt.delta || event.MusicPartner.Known != tt.known {
					t.Fatalf("wrong reward event: %+v %v", event, err)
				}
			}
			if count != 1 {
				t.Fatalf("structured event count = %d", count)
			}
		})
	}
}

func TestMusicPartnerPointsBeforeAndAfter(t *testing.T) {
	for _, tt := range []struct {
		name   string
		before *partnerpoints.Snapshot
		after  *partnerpoints.Snapshot
		want   []string
		absent []string
	}{
		{name: "increase", before: partnerPointsSnapshot(73, 2, 100, 40), after: partnerPointsSnapshot(73, 2, 105, 45), want: []string{"评分前积分：本期=100，本周=40", "本期积分：100 -> 105，变化 +5", "本周积分：40 -> 45，变化 +5"}},
		{name: "unchanged", before: partnerPointsSnapshot(73, 2, 100, 40), after: partnerPointsSnapshot(73, 2, 100, 40), want: []string{"变化 +0", "平台可能延迟入账"}, absent: []string{"确认无奖励"}},
		{name: "decrease", before: partnerPointsSnapshot(73, 2, 100, 40), after: partnerPointsSnapshot(73, 2, 95, 35), want: []string{"本期积分：100 -> 95，变化 -5", "本周积分：40 -> 35，变化 -5"}, absent: []string{"变化 +0"}},
		{name: "period changed", before: partnerPointsSnapshot(73, 2, 100, 40), after: partnerPointsSnapshot(74, 2, 5, 5), want: []string{"本期积分：100 -> 5，差值未计算（期号变化）", "本周积分：40 -> 5，差值未计算（期号变化）"}, absent: []string{"变化 -95", "变化 -35"}},
		{name: "week changed", before: partnerPointsSnapshot(73, 2, 100, 40), after: partnerPointsSnapshot(73, 3, 105, 5), want: []string{"本期积分：100 -> 105，变化 +5", "本周积分：40 -> 5，差值未计算（周次变化）"}, absent: []string{"变化 -35"}},
		{name: "period missing", before: &partnerpoints.Snapshot{Week: partnerPoint(2), Integral: partnerPoint(100), CurrentWeekIntegral: partnerPoint(40)}, after: partnerPointsSnapshot(73, 2, 105, 45), want: []string{"本期积分：100 -> 105，差值未计算（期号缺失）", "本周积分：40 -> 45，差值未计算（期号缺失）"}},
		{name: "week missing", before: &partnerpoints.Snapshot{Period: partnerPoint(73), Integral: partnerPoint(100), CurrentWeekIntegral: partnerPoint(40)}, after: partnerPointsSnapshot(73, 2, 105, 45), want: []string{"本期积分：100 -> 105，变化 +5", "本周积分：40 -> 45，差值未计算（周次缺失）"}},
		{name: "integral missing", before: &partnerpoints.Snapshot{Period: partnerPoint(73), Week: partnerPoint(2), CurrentWeekIntegral: partnerPoint(40)}, after: partnerPointsSnapshot(73, 2, 105, 45), want: []string{"评分前积分：本期=未知，本周=40", "本期积分：未知 -> 105，差值未计算（积分缺失）", "本周积分：40 -> 45，变化 +5"}, absent: []string{"本期积分：0 ->"}},
		{name: "weekly missing", before: partnerPointsSnapshot(73, 2, 100, 40), after: &partnerpoints.Snapshot{Period: partnerPoint(73), Week: partnerPoint(2), Integral: partnerPoint(105)}, want: []string{"本期积分：100 -> 105，变化 +5", "本周积分：40 -> 未知，差值未计算（积分缺失）"}, absent: []string{"本周积分：40 -> 0"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			command, _ := partnerTestCommand(t)
			var output bytes.Buffer
			command.Command().SetOut(&output)
			running, ran := false, false
			reads := 0
			read := func(ctx context.Context) (*partnerpoints.Snapshot, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 15*time.Second || time.Until(deadline) <= 0 {
					t.Fatal("points read must have a live deadline of at most 15 seconds")
				}
				if running {
					t.Fatal("points must not be read during scoring")
				}
				reads++
				if reads == 1 {
					return tt.before, nil
				}
				if !ran {
					t.Fatal("second snapshot taken before scoring")
				}
				return tt.after, nil
			}
			err := command.runWithPoints(context.Background(), "cookie.json", read, func(context.Context) error {
				running = true
				if reads != 1 {
					t.Fatal("scoring started without before snapshot")
				}
				ran, running = true, false
				return nil
			})
			if err != nil || reads != 2 || !ran {
				t.Fatalf("err=%v reads=%d ran=%t", err, reads, ran)
			}
			for _, want := range tt.want {
				if !strings.Contains(output.String(), want) {
					t.Errorf("missing %q in %s", want, output.String())
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(output.String(), absent) {
					t.Errorf("unexpected %q in %s", absent, output.String())
				}
			}
		})
	}
}

func TestMusicPartnerPointsReadFailureKeepsScoringResult(t *testing.T) {
	partialFailure := errors.New("部分评分未完成")
	for _, scoreErr := range []error{nil, partialFailure} {
		for _, failedRead := range []int{1, 2} {
			command, _ := partnerTestCommand(t)
			var output bytes.Buffer
			command.Command().SetOut(&output)
			reads := 0
			got := command.runWithPoints(context.Background(), "cookie.json", func(context.Context) (*partnerpoints.Snapshot, error) {
				reads++
				if reads == failedRead {
					return nil, errors.New("MUSIC_U=secret-cookie https://host.test/?csrf_token=secret-token")
				}
				return partnerPointsSnapshot(73, 2, 100, 40), nil
			}, func(context.Context) error { return scoreErr })
			if got != scoreErr || reads != 2 {
				t.Fatalf("original scoring result changed: got=%v want=%v reads=%d", got, scoreErr, reads)
			}
			if !strings.Contains(output.String(), "积分读取失败") || !strings.Contains(output.String(), "未知") {
				t.Fatalf("read failure not represented: %s", output.String())
			}
			for _, secret := range []string{"MUSIC_U", "secret-cookie", "csrf_token", "secret-token", "host.test"} {
				if strings.Contains(output.String(), secret) {
					t.Fatalf("raw points error leaked %q", secret)
				}
			}
		}
	}
}

func TestMusicPartnerPointsCancellationSkipsFinalRead(t *testing.T) {
	for _, duringRead := range []bool{false, true} {
		command, _ := partnerTestCommand(t)
		var output bytes.Buffer
		command.Command().SetOut(&output)
		ctx, cancel := context.WithCancel(context.Background())
		reads := 0
		err := command.runWithPoints(ctx, "cookie.json", func(readCtx context.Context) (*partnerpoints.Snapshot, error) {
			reads++
			if duringRead {
				cancel()
				<-readCtx.Done()
				return nil, readCtx.Err()
			}
			return partnerPointsSnapshot(73, 2, 100, 40), nil
		}, func(context.Context) error {
			cancel()
			return context.Canceled
		})
		cancel()
		if !errors.Is(err, context.Canceled) || reads != 1 || !strings.Contains(output.String(), "评分后积分未查询") {
			t.Fatalf("err=%v reads=%d log=%s", err, reads, output.String())
		}
	}
}

func TestMusicPartnerPointsScoringDeadlineSkipsFinalRead(t *testing.T) {
	command, _ := partnerTestCommand(t)
	var output bytes.Buffer
	command.Command().SetOut(&output)
	reads := 0
	err := command.runWithPoints(context.Background(), "cookie.json", func(context.Context) (*partnerpoints.Snapshot, error) {
		reads++
		return partnerPointsSnapshot(73, 2, 100, 40), nil
	}, func(context.Context) error { return context.DeadlineExceeded })
	if !errors.Is(err, context.DeadlineExceeded) || reads != 1 || !strings.Contains(output.String(), "评分后积分未查询") {
		t.Fatalf("deadline result not preserved: err=%v reads=%d log=%s", err, reads, output.String())
	}
}
