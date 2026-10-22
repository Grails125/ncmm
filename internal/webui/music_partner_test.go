package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/robfig/cron/v3"
)

func TestMusicPartnerScheduleArguments(t *testing.T) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	for _, test := range []struct {
		name  string
		args  []string
		valid bool
	}{
		{name: "configured accounts", valid: true},
		{name: "explicit account", args: []string{"--cookie-file", "sub/partner.json"}, valid: true},
		{name: "equals syntax", args: []string{"--cookie-file=sub/partner.json"}, valid: true},
		{name: "config override", args: []string{"--config", "other.yaml"}},
		{name: "home override", args: []string{"--home=other"}},
		{name: "unsupported scoring override", args: []string{"--score", "4"}},
		{name: "missing cookie path", args: []string{"--cookie-file"}},
		{name: "empty cookie path", args: []string{"--cookie-file= "}},
		{name: "flag as cookie path", args: []string{"--cookie-file", "--home"}},
		{name: "duplicate cookie", args: []string{"--cookie-file=a.json", "--cookie-file=b.json"}},
		{name: "positional argument", args: []string{"a.json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := Schedule{Name: "合伙人评分", Command: "music-partner", Cron: "0 9 * * *", OverlapPolicy: "skip", Args: test.args}
			if err := validateSchedule(job, parser); (err == nil) != test.valid {
				t.Fatalf("validateSchedule(%v) = %v; valid = %v", test.args, err, test.valid)
			}
		})
	}
}

func TestMusicPartnerResourceKeysFollowSelectedAccounts(t *testing.T) {
	manager := newControlledRunManager(t, 2)
	for _, test := range []struct {
		name      string
		selection string
		command   string
		args      []string
		want      []string
	}{
		{name: "main by default", command: "music-partner", want: []string{"account:cookie.json"}},
		{name: "secondary only", command: "music-partner", selection: "musicPartner:\n  enableMain: false\n  enableSecondaries: true\n", want: []string{"account:secondary.json"}},
		{name: "both enabled", command: "music-partner", selection: "musicPartner:\n  enableMain: true\n  enableSecondaries: true\n", want: []string{"account:cookie.json", "account:secondary.json"}},
		{name: "explicit account overrides selection", command: "music-partner", args: []string{"--cookie-file=chosen.json"}, want: []string{"account:chosen.json"}},
		{name: "batch remains conservative", command: "task", want: []string{"account:cookie.json", "account:secondary.json", "database:database"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := strings.Replace(string(configForTest()), "secondary: []", "secondary: [\"./secondary.json\"]", 1) + test.selection
			if err := os.WriteFile(manager.configPath, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, key := range test.want {
				kind, path, _ := strings.Cut(key, ":")
				want = append(want, kind+":"+manager.canonicalResourcePath(path))
			}
			slices.Sort(want)
			if got := manager.concurrencyKeys(context.Background(), Schedule{Command: test.command, Args: test.args}); !reflect.DeepEqual(got, want) {
				t.Fatalf("resource keys = %v; want %v", got, want)
			}
		})
	}
}

func TestMusicPartnerSerializesSharedAccounts(t *testing.T) {
	for _, command := range []string{"sign", "task"} {
		t.Run(command, func(t *testing.T) {
			manager := newControlledRunManager(t, 2)
			gateA, gateB := filepath.Join(manager.home, "partner.done"), filepath.Join(manager.home, "other.done")
			partner := controlledJob("partner", "skip", "cookie.json", gateA)
			partner.Command = "music-partner"
			other := Schedule{ID: "other", Name: "other", Command: command, OverlapPolicy: "skip", Args: []string{"--test-gate", gateB}}
			first, err := manager.start(context.Background(), partner)
			if err != nil || first.Status != "running" {
				t.Fatalf("partner start = %+v, %v", first, err)
			}
			t.Cleanup(func() { _ = manager.stop(first.ID) })
			second, err := manager.start(context.Background(), other)
			if err != nil || second.Status != "queued" {
				t.Fatalf("shared account start = %+v, %v; want queued", second, err)
			}
			t.Cleanup(func() { _ = manager.stop(second.ID) })
			releaseRun(t, gateA)
			waitForRunStatus(t, manager, first.ID, "success")
			waitForRunStatus(t, manager, second.ID, "running")
			releaseRun(t, gateB)
			waitForRunStatus(t, manager, second.ID, "success")
		})
	}
}

func TestMusicPartnerDifferentAccountsRunTogether(t *testing.T) {
	manager := newControlledRunManager(t, 2)
	gateA, gateB := filepath.Join(manager.home, "a.done"), filepath.Join(manager.home, "b.done")
	partner := controlledJob("partner", "skip", "a.json", gateA)
	partner.Command = "music-partner"
	other := controlledJob("other", "skip", "b.json", gateB)
	other.Command = "task"
	first, err := manager.start(context.Background(), partner)
	if err != nil || first.Status != "running" {
		t.Fatalf("partner start = %+v, %v", first, err)
	}
	t.Cleanup(func() { _ = manager.stop(first.ID) })
	second, err := manager.start(context.Background(), other)
	if err != nil || second.Status != "running" || manager.runningCount() != 2 {
		t.Fatalf("independent account start = %+v, %v; want running", second, err)
	}
	t.Cleanup(func() { _ = manager.stop(second.ID) })
	releaseRun(t, gateA)
	releaseRun(t, gateB)
	waitForRunStatus(t, manager, first.ID, "success")
	waitForRunStatus(t, manager, second.ID, "success")
}

func TestMusicPartnerFailedCommandHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] == "--music-partner-failure-helper" {
		os.Exit(7)
	}
}

func TestMusicPartnerCommandFailureReachesRunsAPI(t *testing.T) {
	manager := newControlledRunManager(t, 1)
	manager.command = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMusicPartnerFailedCommandHelper$", "--", "--music-partner-failure-helper")
	}
	run, err := manager.start(context.Background(), Schedule{ID: "partner", Name: "合伙人评分", Command: "music-partner", OverlapPolicy: "skip"})
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, manager, run.ID, "failed")
	server := &Server{runner: manager}
	response := httptest.NewRecorder()
	server.handleRunsGet(response, httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil))
	var runs []RunRecord
	if err := json.Unmarshal(response.Body.Bytes(), &runs); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(runs) != 1 || runs[0].Status != "failed" || runs[0].ExitCode == nil || *runs[0].ExitCode != 7 || runs[0].Error == "" {
		t.Fatalf("runs API did not expose failure: status=%d runs=%+v", response.Code, runs)
	}
}
