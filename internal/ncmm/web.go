package ncmm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/internal/atomicfile"
	"github.com/3899/ncmm/internal/filelock"
	"github.com/3899/ncmm/internal/managedruntime"
	"github.com/3899/ncmm/internal/selfupdate"
	"github.com/3899/ncmm/internal/webui"
	"github.com/3899/ncmm/pkg/utils"
	"github.com/spf13/cobra"
)

type webOptions struct {
	listen             string
	webConfig          string
	legacyScheduler    bool
	schedulerMigration *webui.SchedulerMigration
	background         bool
	secureCookie       bool
}

func newWebCommand(root *Root) *cobra.Command {
	secureCookie, _ := strconv.ParseBool(os.Getenv("NCMM_WEB_SECURE_COOKIE"))
	opts := webOptions{secureCookie: secureCookie}
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Start the ncmm management service",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := resolveSchedulerMigration(cmd, &opts); err != nil {
				return err
			}
			if opts.background {
				return startWebBackground(opts.listen, webAuthHome(root))
			}
			return runWeb(cmd.Context(), root, opts)
		},
	}
	cmd.Flags().StringVar(&opts.listen, "listen", "127.0.0.1:3899", "WebUI listen address")
	cmd.Flags().StringVar(&opts.webConfig, "web-config", "", "WebUI settings file path")
	cmd.Flags().BoolVar(&opts.legacyScheduler, "scheduler", false, "preserve enabled schedules while migrating a v1 WebUI configuration")
	_ = cmd.Flags().MarkHidden("scheduler")
	cmd.Flags().BoolVar(&opts.background, "background", false, "start the WebUI without a console window (Windows only)")
	cmd.Flags().BoolVar(&opts.secureCookie, "secure-cookie", secureCookie, "mark the WebUI session cookie Secure (or NCMM_WEB_SECURE_COOKIE)")
	cmd.AddCommand(newWebStatusCommand(root), newWebStopCommand(root))
	return cmd
}

func resolveSchedulerMigration(cmd *cobra.Command, opts *webOptions) error {
	opts.schedulerMigration = nil
	if cmd.Flags().Changed("scheduler") {
		opts.schedulerMigration = &webui.SchedulerMigration{
			PreserveEnabled: opts.legacyScheduler,
		}
		return nil
	}
	if value := os.Getenv("NCMM_WEB_PRESERVE_LEGACY_SCHEDULES"); value != "" {
		preserve, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid NCMM_WEB_PRESERVE_LEGACY_SCHEDULES value %q: %w", value, err)
		}
		opts.schedulerMigration = &webui.SchedulerMigration{
			PreserveEnabled: preserve,
		}
	}
	return nil
}

func runWeb(parent context.Context, root *Root, opts webOptions) error {
	home := filepath.Clean(utils.Ternary(root.Opts.Home != "", root.Opts.Home, config.HomeDir))
	if err := os.MkdirAll(home, 0755); err != nil {
		return err
	}
	configPath, err := prepareWebConfig(parent, home, root.CfgPath)
	if err != nil {
		return err
	}
	root.CfgPath = configPath
	if opts.webConfig == "" {
		opts.webConfig = filepath.Join(home, "webui.yaml")
	}
	opts.webConfig, err = filepath.Abs(opts.webConfig)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	server, err := webui.New(ctx, webui.Options{
		Listen: opts.listen, Home: home, ConfigPath: configPath,
		WebConfig: opts.webConfig, Executable: executable, Version: root.AppVersion,
		Commit: root.AppCommit, Branch: root.AppBranch, BuildTime: root.AppBuildTime,
		SecureCookie: opts.secureCookie, Output: root.cmd.Printf,
		SchedulerMigration: opts.schedulerMigration,
		PluginManager:      NewPlugin(root, root.l),
	})
	if err != nil {
		return err
	}
	err = server.Run(ctx)
	if !errors.Is(err, webui.ErrRestartRequested) {
		return err
	}
	if selfupdate.RuntimeManaged(executable) {
		return &managedRestartError{}
	}
	restarted := exec.Command(executable, webRestartArgs(os.Args[1:], home, configPath, opts.webConfig)...)
	restarted.Dir = home
	restarted.Env = append(os.Environ(), "NCMM_WEB_RESTARTED=1")
	restarted.Stdout = os.Stdout
	restarted.Stderr = os.Stderr
	return restarted.Start()
}

type managedRestartError struct{}

func (*managedRestartError) Error() string { return "请求启动器重启程序" }
func (*managedRestartError) ExitCode() int { return managedruntime.RestartExitCode }

func prepareWebConfig(parent context.Context, home, configPath string) (string, error) {
	if configPath != "" && configPath != "default" {
		return filepath.Abs(configPath)
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return "", err
	}
	// 与默认启动共用候选规则；已存在 config.yml 时不另建 config.yaml。
	location, err := findConfigLocation("", home, home, false)
	if err != nil || location.Path != "" {
		return location.Path, err
	}
	configPath = filepath.Join(location.Home, "config.yaml")
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	lock, err := filelock.Acquire(ctx, configPath+".lock")
	if err != nil {
		return "", fmt.Errorf("initialize config lock: %w", err)
	}
	defer lock.Close()
	location, err = findConfigLocation("", location.Home, location.Home, false)
	if err != nil || location.Path != "" {
		return location.Path, err
	}
	if err := atomicfile.Write(configPath, config.DefaultYAML(), 0600); err != nil {
		return "", fmt.Errorf("initialize config: %w", err)
	}
	return configPath, nil
}

func webRestartArgs(args []string, home, configPath, webConfigPath string) []string {
	// 重启工作目录会切到 home，固定已解析路径，避免重新发现其他实例。
	result := []string{"--home", home, "--config", configPath}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			result = append(result, "--web-config", webConfigPath)
			return append(result, args[index:]...)
		}
		switch {
		case arg == "--home" || arg == "--config" || arg == "-c" || arg == "--web-config":
			index++
		case strings.HasPrefix(arg, "--home=") || strings.HasPrefix(arg, "--config=") || strings.HasPrefix(arg, "--web-config=") || strings.HasPrefix(arg, "-c"):
		default:
			result = append(result, arg)
		}
	}
	return append(result, "--web-config", webConfigPath)
}
