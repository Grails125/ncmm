package ncmm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/3899/ncmm/config"
)

type configLocation struct {
	Path       string
	Home       string
	Discovered bool
}

func (c *Root) resolveConfigLocation() (configLocation, error) {
	home := c.Opts.Home
	explicitHome := c.cmd.PersistentFlags().Changed("home")
	if explicitHome && strings.TrimSpace(home) == "" {
		return configLocation{}, fmt.Errorf("--home 不能为空")
	}
	if !explicitHome {
		if envHome := strings.TrimSpace(os.Getenv("NCMM_HOME")); envHome != "" {
			home, explicitHome = envHome, true
		}
	}
	if home == "" {
		home = config.HomeDir
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return configLocation{}, fmt.Errorf("读取工作目录失败: %w", err)
	}
	// 容器的数据目录是实例边界，缺少配置时不能误用镜像中的模板。
	allowDiscovery := !explicitHome && os.Getenv("NCMM_DOCKER_OFFICIAL") != "1"
	return findConfigLocation(c.Opts.Config, home, workingDir, allowDiscovery)
}

func findConfigLocation(configPath, home, workingDir string, allowDiscovery bool) (configLocation, error) {
	abs := func(path string) string {
		if !filepath.IsAbs(path) {
			path = filepath.Join(workingDir, path)
		}
		return filepath.Clean(path)
	}
	location := configLocation{Home: abs(home)}
	if configPath != "" {
		location.Path = abs(configPath)
		if found, err := configFileExists(location.Path); err != nil || !found {
			if err == nil {
				err = fmt.Errorf("指定的配置文件不存在: %s", location.Path)
			}
			return configLocation{}, err
		}
		return location, nil
	}
	directories := []string{location.Home}
	if allowDiscovery {
		// 只检查标准文件名及有限目录，不递归扫描其他实例、备份或临时文件。
		for _, directory := range []string{".", "data", "run", ".ncmm", "config", "configs", "conf"} {
			directories = append(directories, filepath.Join(workingDir, directory))
		}
	}
	for _, directory := range uniqueStrings(directories) {
		for _, name := range []string{"config.yaml", "config.yml"} {
			candidate := filepath.Join(directory, name)
			found, err := configFileExists(candidate)
			if err != nil {
				return configLocation{}, err
			}
			if found {
				location.Path, location.Home = candidate, directory
				location.Discovered = allowDiscovery
				return location, nil
			}
		}
	}
	return location, nil
}

func configFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("读取配置文件信息失败 %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("配置路径不是普通文件: %s", path)
	}
	return true, nil
}
