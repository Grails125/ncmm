package ncmm

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/spf13/cobra"
)

func TestPluginRunParsesHostFlagsBeforeConfigInitialization(t *testing.T) {
	for _, args := range [][]string{
		{"--config", "audit.yaml", "--home", "audit-home", "plugin", "run", "missing-audit-plugin"},
		{"plugin", "run", "--config=audit.yaml", "--home=audit-home", "missing-audit-plugin"},
		{"plugin", "run", "missing-audit-plugin", "-c", "audit.yaml", "--home", "audit-home"},
	} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			root := New()
			stopped := fmt.Errorf("test stops before config or plugin execution")
			root.cmd.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
				if root.Opts.Config != "audit.yaml" || root.Opts.Home != "audit-home" {
					t.Errorf("host options not parsed: %+v", root.Opts)
				}
				if !root.cmd.PersistentFlags().Changed("config") || !root.cmd.PersistentFlags().Changed("home") {
					t.Error("explicit flags must remain visible to configuration discovery")
				}
				return stopped
			}
			root.cmd.SetArgs(args)
			if err := root.cmd.Execute(); err != stopped {
				t.Fatalf("unexpected command error: %v", err)
			}
		})
	}
}

func TestPluginRunHostFlagBoundary(t *testing.T) {
	root := New()
	cmd, _, err := root.cmd.Find([]string{"plugin", "run"})
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--home", "host", "demo", "--days", "7", "--", "--config", "plugin.yaml", "--home=plugin", "--", "literal"}
	got, err := parsePluginRunHostFlags(cmd, args)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"demo", "--days", "7", "--", "--config", "plugin.yaml", "--home=plugin", "--", "literal"}
	if !reflect.DeepEqual(got, want) || root.Opts.Home != "host" || root.Opts.Config != "" {
		t.Fatalf("args=%q opts=%+v", got, root.Opts)
	}
}

func TestPluginRunRejectsMissingHostFlagValue(t *testing.T) {
	root := New()
	cmd, _, _ := root.cmd.Find([]string{"plugin", "run"})
	if _, err := parsePluginRunHostFlags(cmd, []string{"demo", "--home"}); err == nil {
		t.Fatal("missing --home value must fail before reading configuration")
	}
}
