// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package ncmm

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/config"
	"github.com/3899/ncmm/pkg/log"

	qrcode2 "github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"
)

type loginQrcodeCmd struct {
	root *Login
	cmd  *cobra.Command
	l    *log.Logger

	timeout   time.Duration // 登录超时时间
	dir       string        // 二维码文件路径
	level     int           // 二维码恢复能力等级
	output    string        // Cookie 输出文件路径
	newClient func(*api.Config, *log.Logger) (*api.Client, error)
	waitPoll  func(context.Context, time.Duration) error
}

func qrcode(root *Login, l *log.Logger) *cobra.Command {
	c := &loginQrcodeCmd{
		root: root,
		l:    l,
	}
	c.cmd = &cobra.Command{
		Use:     "qrcode",
		Short:   "use qrcode login",
		Example: "  ncmm login qrcode",
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.execute(cmd.Context(), args)
		},
	}
	c.addFlags()
	return c.cmd
}

func (c *loginQrcodeCmd) addFlags() {
	c.cmd.Flags().DurationVarP(&c.timeout, "timeout", "t", time.Minute*5, "login timeout, eg: 1s、1m")
	c.cmd.Flags().StringVarP(&c.dir, "dir", "d", "", "qrcode file output path. default ./")
	c.cmd.Flags().IntVarP(&c.level, "level", "l", 1, "qrcode recovery capacity,0->7% 1->15%(default) 2->25% 3->30%")
	c.cmd.Flags().StringVarP(&c.output, "output", "o", "", "output cookie file path")
}

func (c *loginQrcodeCmd) execute(ctx context.Context, _ []string) error {
	if c.level < 0 || c.level > 3 {
		return fmt.Errorf("qrcode level must be 0-3")
	}

	// 每次扫码从匿名会话开始，避免旧账号 Cookie 影响授权；只有验证成功才替换目标账号。
	networkCfg, tempCookieFile, err := c.newSessionConfig()
	if err != nil {
		return err
	}
	defer func() {
		_ = os.Chmod(tempCookieFile, 0600)
		_ = os.Remove(tempCookieFile)
	}()

	newClient := c.newClient
	if newClient == nil {
		newClient = api.NewClient
	}
	cli, err := newClient(networkCfg, c.l)
	if err != nil {
		return fmt.Errorf("NewClient: %w", err)
	}
	defer cli.Close(context.Background())
	if transport, ok := cli.GetClient().Transport.(*http.Transport); ok {
		transport = transport.Clone()
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		}
		transport.TLSClientConfig.InsecureSkipVerify = false
		cli.GetClient().Transport = transport
	}
	request := eapi.New(cli)

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// 1. 生成key
	key, err := request.QrcodeCreateKey(ctx, &eapi.QrcodeCreateKeyReq{Type: 3})
	if err != nil {
		return fmt.Errorf("QrcodeCreateKey: %w", err)
	}
	if key == nil {
		return fmt.Errorf("二维码登录未返回结果，请稍后重试")
	}
	if key.Code != 200 || key.UniKey == "" {
		return fmt.Errorf("获取登录二维码失败（响应码：%d），请稍后重试", key.Code)
	}

	// 2. 生成二维码
	qr, err := weapi.New(cli).QrcodeGenerate(ctx, &weapi.QrcodeGenerateReq{
		CodeKey:  key.UniKey,
		Level:    qrcode2.RecoveryLevel(c.level),
		Platform: "pc",
		DeviceId: ""})
	if err != nil {
		return fmt.Errorf("QrcodeGenerate: %s", err)
	}

	// 3. 手机扫码
	if c.dir == "" {
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		c.dir = dir
	}
	if err := os.MkdirAll(c.dir, os.ModePerm); err != nil {
		return fmt.Errorf("MkdirAll: %w", err)
	}
	var file = filepath.Join(c.dir, "qrcode.png")
	if err := os.WriteFile(file, qr.Qrcode, 0600); err != nil {
		return err
	}
	defer os.Remove(file)
	c.cmd.Println(">>>>> please scan qrcode in your phone <<<<<")
	c.cmd.Printf("qrcode content: https://music.163.com/login?codekey=%s\n", key.UniKey)
	c.cmd.Printf("qrcode file: %s\n", file)
	c.cmd.Printf("qrcode: \n%s\n", qr.QrcodePrint)

	// 4. 轮训获取扫码状态
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if c.waitPoll != nil {
			if err := c.waitPoll(ctx, 3*time.Second); err != nil {
				return err
			}
		} else {
			timer := time.NewTimer(3 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		resp, err := request.QrcodeCheck(ctx, &eapi.QrcodeCheckReq{Type: 3, Key: key.UniKey})
		if err != nil {
			return fmt.Errorf("QrcodeCheck: %w", err)
		}
		if resp == nil {
			return fmt.Errorf("二维码状态查询未返回结果，请重新扫码")
		}
		log.Debug("二维码扫码状态：code=%d", resp.Code)
		switch resp.Code {
		case 800: // 二维码不存在、已过期、用户取消授权
			return fmt.Errorf("二维码已过期或已取消（响应码：800），请重新获取后扫码")
		case 801: // 等待扫码
			continue
		case 802: // 正在扫码授权中
			continue
		case 803: // 授权登录成功
			goto ok
		case 8821:
			return fmt.Errorf("网易云拒绝二维码登录（响应码：8821），请升级手机端官方网易云音乐后重新扫码，或使用 Cookie 登录")
		default:
			return fmt.Errorf("二维码登录失败（响应码：%d），请重新扫码或使用 Cookie 登录", resp.Code)
		}
	}
ok:

	if err := os.Remove(file); err != nil {
		if !os.IsNotExist(err) {
			log.Info("remove qrcode file: %s", err)
		}
	}

	// 5. 查询登录信息是否成功
	if cli.MusicU() == "" {
		return fmt.Errorf("扫码授权未返回有效登录 Cookie，请重新扫码或使用 Cookie 登录")
	}
	weapi.ClearUserInfoCache(cli.MusicU())
	user, err := weapi.New(cli).GetUserInfo(ctx, &weapi.GetUserInfoReq{})
	if err != nil {
		return fmt.Errorf("GetUserInfo: %s", err)
	}
	if user == nil || user.Code != 200 || user.Account == nil || user.Account.Id <= 0 || user.Profile == nil || user.Profile.UserId != user.Account.Id {
		return fmt.Errorf("扫码后账号校验失败，未保存 Cookie，请重新扫码或使用 Cookie 登录")
	}
	// 先完成 Cookie 导出并关闭会话，再移动文件，避免延迟导出重建临时文件。
	if err := cli.Close(ctx); err != nil {
		return fmt.Errorf("保存扫码登录会话失败: %w", err)
	}
	if err := verifyQRCodeSavedSession(networkCfg, cli.MusicU()); err != nil {
		log.Warn("扫码登录会话落盘校验失败（错误类型 %T），未替换原账号", err)
		return fmt.Errorf("扫码登录会话未正确保存，原账号未替换，请检查目录写入权限后重试")
	}

	err = c.root.saveLoginResult(ctx, user.Profile.Nickname, user.Profile.AvatarUrl, user.Account.Id, tempCookieFile, c.output, "")
	if err != nil {
		return err
	}
	c.cmd.Printf("login success: uid=%d nickname=%s\n", user.Account.Id, user.Profile.Nickname)
	return nil
}

// Cookie.Close 不保证导出错误向上传递，移动文件前须核对磁盘上的实际会话。
func verifyQRCodeSavedSession(network *api.Config, expectedMusicU string) error {
	if network == nil || expectedMusicU == "" {
		return fmt.Errorf("缺少待校验的扫码登录会话")
	}
	readonly := *network
	readonly.Cookie.ReadOnly = true
	readonly.Cookie.Interval = 0
	saved, err := api.NewClient(&readonly, nil)
	if err != nil {
		return err
	}
	defer saved.Close(context.Background())
	if saved.MusicU() != expectedMusicU {
		return fmt.Errorf("扫码登录会话与磁盘记录不一致")
	}
	return nil
}

func (c *loginQrcodeCmd) newSessionConfig() (*api.Config, string, error) {
	original := c.root.root.Cfg.Network
	if original == nil {
		return nil, "", fmt.Errorf("未配置网络连接")
	}
	targetDir := filepath.Dir(original.Cookie.Filepath)
	if original.Cookie.Filepath == "" {
		targetDir = c.root.root.Opts.Home
		if targetDir == "" {
			targetDir = config.HomeDir
		}
	}
	if c.output != "" && filepath.IsAbs(c.output) {
		targetDir = filepath.Dir(c.output)
	}
	if err := os.MkdirAll(targetDir, 0700); err != nil {
		return nil, "", fmt.Errorf("创建扫码登录目录失败: %w", err)
	}
	file, err := os.CreateTemp(targetDir, ".qrcode-cookie-*.json")
	if err != nil {
		return nil, "", fmt.Errorf("创建扫码登录会话失败: %w", err)
	}
	path := file.Name()
	if _, err := file.WriteString("{}"); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, "", fmt.Errorf("初始化扫码登录会话失败: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, "", fmt.Errorf("初始化扫码登录会话失败: %w", err)
	}
	network := *original
	network.Cookie.Filepath = path
	network.Cookie.Interval = 0
	network.Cookie.ReadOnly = false
	network.Debug = false
	network.Retry = 0
	return &network, path, nil
}
