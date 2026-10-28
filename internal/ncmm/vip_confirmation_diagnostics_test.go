package ncmm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
	cookiepkg "github.com/3899/ncmm/pkg/cookie"
	"github.com/3899/ncmm/pkg/notify"
)

func newVipConfirmationClient(t *testing.T, respond func(string) string) *api.Client {
	t.Helper()
	cookiePath := filepath.Join(t.TempDir(), "cookie.json")
	if err := os.WriteFile(cookiePath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := api.NewClient(&api.Config{Cookie: cookiepkg.Config{Filepath: cookiePath, ReadOnly: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	client.GetClient().Transport = musicianVipClaimTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(respond(r.URL.Path))), Request: r}, nil
	})
	return client
}

func TestVipSignRequiresReadbackConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, receipt, list, records string
		confirmed                    bool
	}{
		{"rejected", `{"code":200,"data":false}`, `{"code":200,"data":[]}`, `{"code":200,"data":[]}`, false},
		{"accepted-without-record", `{"code":200,"data":true}`, `{"code":200,"data":[{"missionId":-500,"mainTitle":"黑胶乐签","status":10}]}`, `{"code":200,"data":[{"today":true,"recordId":0,"time":0,"score":3}]}`, false},
		{"confirmed-record", `{"code":200,"data":true}`, `{"code":200,"data":[]}`, `{"code":200,"data":[{"today":true,"recordId":123,"score":3}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newVipConfirmationClient(t, func(path string) string {
				switch path {
				case "/eapi/vip-center-bff/task/sign":
					return tc.receipt
				case "/eapi/vip-center-bff/task/list":
					return tc.list
				case "/eapi/vipnewcenter/app/user/sign/info":
					return tc.records
				default:
					t.Fatalf("unexpected endpoint: %s", path)
					return ""
				}
			})
			command := NewSign(&Root{Cfg: &config.Config{}}, nil)
			var output bytes.Buffer
			command.cmd.SetOut(&output)
			if got := command.executeVipSign(t.Context(), eapi.New(client), ""); got != tc.confirmed {
				t.Fatalf("confirmation=%v want %v; %s", got, tc.confirmed, output.String())
			}
			if !tc.confirmed && strings.Contains(output.String(), "乐签成功") {
				t.Fatal(output.String())
			}
		})
	}
}

func TestVipGrowthRejectsStaleOrUndatedTodayScore(t *testing.T) {
	now := time.Now().In(neteaseLocation)
	for _, tc := range []struct {
		name, day string
		more      bool
		want      int64
		known     bool
		queries   int
	}{
		{"current", now.Format("20060102"), false, 9, true, 0},
		{"stale", now.AddDate(0, 0, -1).Format("200612"), false, 6, true, 1},
		{"missing-date", "", false, 6, true, 1},
		{"incomplete-details", "", true, 0, false, 5},
		{"crossed-day", "", true, 6, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queries := 0
			ext, _ := json.Marshal(map[string]any{"todayScore": 9, "currentDay": tc.day})
			client := newVipConfirmationClient(t, func(path string) string {
				switch path {
				case "/eapi/vipnewcenter/app/level/growhpoint/basic":
					return fmt.Sprintf(`{"code":200,"data":{"userLevel":{"growthPoint":8224,"extJson":%q}}}`, string(ext))
				case "/eapi/vipnewcenter/app/level/growth/details":
					queries++
					if tc.name == "incomplete-details" {
						return fmt.Sprintf(`{"code":200,"data":{"hasMore":true,"details":[{"time":%d,"growthPoint":6}]}}`, now.UnixMilli()-int64(queries))
					}
					return fmt.Sprintf(`{"code":200,"data":{"hasMore":%t,"details":[{"time":%d,"growthPoint":6},{"time":%d,"growthPoint":9}]}}`, tc.more, now.UnixMilli(), now.AddDate(0, 0, -1).UnixMilli())
				default:
					t.Fatalf("unexpected endpoint: %s", path)
					return ""
				}
			})
			got, err := getVipGrowthState(t.Context(), eapi.New(client), "")
			if err != nil || got.GrowthPoint != 8224 || got.TodayScoreKnown != tc.known || (tc.known && got.TodayScore != tc.want) || queries != tc.queries {
				t.Fatalf("state=%+v err=%v detailQueries=%d", got, err, queries)
			}
		})
	}
}

func TestVipGrowthDateDoesNotAcceptAmbiguousCompactDate(t *testing.T) {
	for _, date := range []time.Time{time.Date(2026, 1, 11, 0, 0, 0, 0, neteaseLocation), time.Date(2026, 11, 1, 0, 0, 0, 0, neteaseLocation)} {
		if vipGrowthDayMatches("2026111", date) {
			t.Fatalf("ambiguous date must fall back to ledger: %s", date)
		}
		if !vipGrowthDayMatches(date.Format("20060102"), date) {
			t.Fatal("padded date should remain supported")
		}
	}
}

func TestVipUnsupportedTasksDoNotRepeatOrClaimCompletion(t *testing.T) {
	listCalls, rewards := 0, 0
	client := newVipConfirmationClient(t, func(path string) string {
		switch path {
		case "/eapi/vip-center-bff/task/list":
			listCalls++
			return `{"code":200,"data":[{"mainTitle":"关注网易云会员小红书账号","status":10},{"mainTitle":"关注装扮bot小红书账号","status":10}]}`
		case "/eapi/vipnewcenter/app/user/sign/info":
			return `{"code":200,"data":[{"today":true,"recordId":123}]}`
		case "/eapi/vipnewcenter/app/level/growhpoint/basic":
			return `{"code":200,"data":{"userLevel":{"growthPoint":8224}}}`
		case "/eapi/vipnewcenter/app/level/growth/details":
			return `{"code":200,"data":{"details":[]}}`
		case "/weapi/middle/vip/mission/user/progress/list":
			return `{"code":200,"data":[]}`
		case "/eapi/vipnewcenter/app/level/task/reward/getall":
			rewards++
			return `{"code":200,"data":{"result":true}}`
		default:
			t.Fatalf("unexpected endpoint: %s", path)
			return ""
		}
	})
	command := NewSign(&Root{Cfg: &config.Config{Sign: &config.SignConf{Automatic: true}}}, nil)
	var output bytes.Buffer
	command.cmd.SetOut(&output)
	command.handleVipTasks(t.Context(), client, weapi.New(client), 0)
	if listCalls != 2 || rewards != 1 {
		t.Fatalf("empty task rounds repeated: list=%d rewards=%d", listCalls, rewards)
	}
	if strings.Contains(output.String(), "子任务 [关注") || strings.Contains(output.String(), "成功领取所有") || !strings.Contains(output.String(), "未支持自动执行") {
		t.Fatal(output.String())
	}
}

func TestVipPendingWelfareDoesNotRepeatClaims(t *testing.T) {
	claims, rewards := 0, 0
	client := newVipConfirmationClient(t, func(path string) string {
		switch path {
		case "/eapi/vip-center-bff/task/list":
			return `{"code":200,"data":[{"missionCode":"FLQ","mainTitle":"福利券","status":50}]}`
		case "/eapi/vipnewcenter/app/user/sign/info":
			return `{"code":200,"data":[{"today":true,"recordId":123}]}`
		case "/eapi/vipnewcenter/app/level/growhpoint/basic":
			return `{"code":200,"data":{"userLevel":{"growthPoint":8224}}}`
		case "/eapi/vipnewcenter/app/level/growth/details":
			return `{"code":200,"data":{"details":[]}}`
		case "/weapi/middle/vip/mission/user/progress/list":
			return `{"code":200,"data":[]}`
		case "/eapi/vipnewcenter/app/level/task/reward/getall":
			rewards++
			return `{"code":200,"data":{"result":true}}`
		case "/eapi/vipnewcenter/app/benefitcenter/benefits/category/list":
			return `{"code":200,"data":[{"id":10,"name":"免费券","benefitGet":false}]}`
		case "/eapi/vipcenter/benefits/get":
			claims++
			return `{"code":200,"result":{"benefitGet":true}}`
		case "/eapi/vipnewcenter/app/level/welfare/new/list":
			return `{"code":200,"data":{}}`
		case "/weapi/vipnewcenter/app/level/welfare/new/list":
			return `{"code":200,"data":{}}`
		default:
			t.Fatalf("意外请求：%s", path)
			return ""
		}
	})
	root := &Root{Cfg: &config.Config{Sign: &config.SignConf{Automatic: true}}, Report: notify.NewReport("test")}
	c := NewSign(root, nil)
	var output bytes.Buffer
	c.cmd.SetOut(&output)
	c.handleVipTasks(t.Context(), client, weapi.New(client), 4, "fixture")
	if claims != 1 || rewards != 1 || strings.Contains(output.String(), "成功领券打卡日常福利任务") {
		t.Fatalf("不能反复领奖或将商家券回执等同成长任务完成：claims=%d rewards=%d 日志=%s", claims, rewards, output.String())
	}
	_, _, unknown := root.Report.OutcomeCounts()
	if unknown != 1 {
		t.Fatalf("状态50仍须保留未确认：%+v", root.Report.Items())
	}
}

func TestVipFinalSignUsesOneConfirmedOutcomeWithoutOverwritingList(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(fmt.Sprintf("confirmed-%t", confirmed), func(t *testing.T) {
			signs, records := 0, 0
			client := newVipConfirmationClient(t, func(path string) string {
				switch path {
				case "/eapi/vip-center-bff/task/list":
					return `{"code":200,"data":[{"missionId":-500,"mainTitle":"黑胶乐签","status":10}]}`
				case "/eapi/vip-center-bff/task/sign":
					signs++
					return `{"code":200,"data":true}`
				case "/eapi/vipnewcenter/app/user/sign/info":
					records++
					if confirmed && records >= 3 {
						return `{"code":200,"data":[{"today":true,"recordId":123}]}`
					}
					return `{"code":200,"data":[]}`
				case "/eapi/vipnewcenter/app/level/growhpoint/basic":
					return `{"code":200,"data":{"userLevel":{"growthPoint":8224}}}`
				case "/eapi/vipnewcenter/app/level/growth/details":
					return `{"code":200,"data":{"details":[]}}`
				case "/weapi/middle/vip/mission/user/progress/list":
					return `{"code":200,"data":[]}`
				case "/eapi/vipnewcenter/app/level/task/reward/getall":
					return `{"code":200,"data":{"result":true}}`
				default:
					t.Fatalf("unexpected endpoint: %s", path)
					return ""
				}
			})
			root := &Root{Cfg: &config.Config{Sign: &config.SignConf{Automatic: true}}, Report: notify.NewReport("test")}
			command := NewSign(root, nil)
			var output bytes.Buffer
			command.cmd.SetOut(&output)
			command.handleVipTasks(t.Context(), client, weapi.New(client), 0, "fixture-account")
			unconfirmed := 0
			for _, item := range root.Report.Items() {
				if strings.Contains(item.Task, "乐签") || item.Task == "vip-sign" {
					unconfirmed++
				}
			}
			want := 1
			if confirmed {
				want = 0
			}
			if signs != 1 || unconfirmed != want {
				t.Fatalf("signs=%d report=%+v output=%s", signs, root.Report.Items(), output.String())
			}
			for _, line := range strings.Split(output.String(), "\n") {
				if strings.Contains(line, "任务: 黑胶乐签") && strings.Contains(line, "已完成") {
					t.Fatalf("local result overwrote pending official task: %s", line)
				}
			}
		})
	}
}

func TestVipFinalTasksDistinguishMissingAttemptFromNewTask(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-%t", missing), func(t *testing.T) {
			lists, rewards := 0, 0
			client := newVipConfirmationClient(t, func(path string) string {
				switch path {
				case "/eapi/vip-center-bff/task/list":
					lists++
					if (missing && lists <= 3) || (!missing && lists == 2) {
						return `{"code":200,"data":[{"missionId":7,"mainTitle":"查看AI调音大师","status":10}]}`
					}
					return `{"code":200,"data":[]}`
				case "/eapi/vipnewcenter/app/user/sign/info":
					return `{"code":200,"data":[{"today":true,"recordId":123}]}`
				case "/eapi/vipnewcenter/app/level/growhpoint/basic":
					return `{"code":200,"data":{"userLevel":{"growthPoint":8224}}}`
				case "/eapi/vipnewcenter/app/level/growth/details":
					return `{"code":200,"data":{"details":[]}}`
				case "/weapi/middle/vip/mission/user/progress/list":
					return `{"code":200,"data":[]}`
				case "/eapi/vipnewcenter/app/level/task/reward/getall":
					rewards++
					return fmt.Sprintf(`{"code":200,"data":{"result":%t}}`, !missing)
				default:
					t.Fatalf("unexpected endpoint: %s", path)
					return ""
				}
			})
			root := &Root{Cfg: &config.Config{Sign: &config.SignConf{Automatic: true}}, Report: notify.NewReport("test")}
			command := NewSign(root, nil)
			var output bytes.Buffer
			command.cmd.SetOut(&output)
			command.handleVipTasks(t.Context(), client, weapi.New(client), 0, "fixture-account")
			if rewards != 1 {
				t.Fatalf("uncertain reward was retried: %d", rewards)
			}
			found := false
			for _, item := range root.Report.Items() {
				if item.Task == "vip:查看AI调音大师" {
					found = true
					wantKind := notify.KindSkip
					if missing {
						wantKind = notify.KindUnconfirmed
					}
					if item.Kind != wantKind {
						t.Fatalf("incorrect outcome: %+v", item)
					}
				}
			}
			if !found {
				t.Fatalf("task outcome disappeared: %+v", root.Report.Items())
			}
		})
	}
}
