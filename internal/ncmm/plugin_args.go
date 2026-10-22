package ncmm

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// 插件保留自己的参数语法；仅提取分隔符之前的宿主全局选项，并在配置初始化前生效。
func parsePluginRunHostFlags(cmd *cobra.Command, args []string) ([]string, error) {
	flags := cmd.Root().PersistentFlags()
	var hostArgs, pluginArgs []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			pluginArgs = append(pluginArgs, args[i:]...)
			break
		}
		var flag *pflag.Flag
		hasValue := false
		if strings.HasPrefix(arg, "--") {
			name, _, found := strings.Cut(arg[2:], "=")
			flag, hasValue = flags.Lookup(name), found
		} else if strings.HasPrefix(arg, "-") && len(arg) > 1 {
			flag, hasValue = flags.ShorthandLookup(arg[1:2]), len(arg) > 2
		}
		if flag == nil {
			pluginArgs = append(pluginArgs, arg)
			continue
		}
		hostArgs = append(hostArgs, arg)
		if !hasValue && flag.NoOptDefVal == "" {
			if i+1 >= len(args) || args[i+1] == "--" || strings.HasPrefix(args[i+1], "--") {
				return nil, fmt.Errorf("全局参数 --%s 缺少值", flag.Name)
			}
			i++
			hostArgs = append(hostArgs, args[i])
		}
	}
	if err := flags.Parse(hostArgs); err != nil {
		return nil, err
	}
	return pluginArgs, nil
}
