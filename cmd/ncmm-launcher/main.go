package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/3899/ncmm/internal/managedruntime"
)

var Version = "0.0.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "[launcher]", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("需要 serve 或 exec 子命令")
	}
	mode := args[0]
	args = args[1:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	image := os.Getenv("NCMM_IMAGE_EXECUTABLE")
	if image == "" {
		image = "/opt/ncmm/ncmm"
	}
	root := managedruntime.Directory()
	if mode == "exec" {
		if err := managedruntime.Initialize(root, image, Version); err != nil {
			return err
		}
		path, err := managedruntime.CurrentExecutable(root)
		if err != nil {
			return err
		}
		return managedruntime.Exec(path, args, environment(root))
	}
	if mode != "serve" {
		return fmt.Errorf("未知启动器命令")
	}
	home := os.Getenv("NCMM_HOME")
	if home == "" {
		home = "/data"
	}
	for i, arg := range args {
		if arg == "--home" && i+1 < len(args) {
			home = args[i+1]
		}
		if strings.HasPrefix(arg, "--home=") {
			home = strings.TrimPrefix(arg, "--home=")
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return managedruntime.Serve(ctx, managedruntime.SupervisorOptions{Root: root, Image: image, ImageVersion: Version, Home: home, Args: args, Output: os.Stdout})
}

func environment(root string) []string {
	result := make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "NCMM_RUNTIME_MANAGED=") || strings.HasPrefix(item, "NCMM_RUNTIME_DIR=") {
			continue
		}
		result = append(result, item)
	}
	return append(result, "NCMM_RUNTIME_MANAGED=1", "NCMM_RUNTIME_DIR="+root)
}
