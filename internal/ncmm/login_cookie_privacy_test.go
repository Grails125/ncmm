package ncmm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3899/ncmm/pkg/log"
)

func TestCookieLoginDebugLogDoesNotExposeInput(t *testing.T) {
	const secret = "synthetic-cookie-private-marker"
	directory := t.TempDir()
	logPath := filepath.Join(directory, "login.log")
	configuration := &log.Config{Level: "debug"}
	configuration.Rotate.Filename = logPath
	logger := log.New(configuration)
	previous := log.Default
	log.Default = logger
	t.Cleanup(func() {
		log.Default = previous
		_ = logger.Close()
	})
	command := &loginCookieCmd{format: "json"}
	// 无效 JSON 在网络调用前失败，也必须保留输入内容的保密性。
	if err := command.execute(context.Background(), []string{`{"MUSIC_U":"` + secret + `",`}); err == nil {
		t.Fatal("invalid cookie JSON unexpectedly succeeded")
	}
	output, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(output), secret) {
		t.Fatal("debug log exposed the imported cookie")
	}
	if !strings.Contains(string(output), "format=json") {
		t.Fatal("debug log lost safe operation context")
	}
}

func TestCookieLoginMalformedFieldsStayOutOfErrorsAndLogs(t *testing.T) {
	const netscapeSecret = "synthetic-expiration-private-marker"
	const jsonSecret = "1e999999"
	inputs := []struct {
		name   string
		format string
		secret string
		body   string
	}{
		{"netscape_auto", "", netscapeSecret, "# Netscape HTTP Cookie File\n.music.163.com\tTRUE\t/\tFALSE\t" + netscapeSecret + "\tMUSIC_U\tsynthetic-token\n"},
		{"netscape_explicit", "netscape", netscapeSecret, "# Netscape HTTP Cookie File\n.music.163.com\tTRUE\t/\tFALSE\t" + netscapeSecret + "\tMUSIC_U\tsynthetic-token\n"},
		{"netscape_legacy_alias", "netscaple", netscapeSecret, "# Netscape HTTP Cookie File\n.music.163.com\tTRUE\t/\tFALSE\t" + netscapeSecret + "\tMUSIC_U\tsynthetic-token\n"},
		{"json_auto", "", jsonSecret, `[{"name":"MUSIC_U","value":"synthetic-token","expirationDate":` + jsonSecret + `}]`},
		{"json_explicit", "json", jsonSecret, `[{"name":"MUSIC_U","value":"synthetic-token","expirationDate":` + jsonSecret + `}]`},
	}
	for _, input := range inputs {
		for _, fromFile := range []bool{false, true} {
			name := input.name + "/text"
			if fromFile {
				name = input.name + "/file"
			}
			t.Run(name, func(t *testing.T) {
				directory := t.TempDir()
				logPath := filepath.Join(directory, "login.log")
				configuration := &log.Config{Level: "debug"}
				configuration.Rotate.Filename = logPath
				logger := log.New(configuration)
				previous := log.Default
				log.Default = logger
				t.Cleanup(func() {
					log.Default = previous
					_ = logger.Close()
				})
				command := &loginCookieCmd{format: input.format}
				arguments := []string{input.body}
				if fromFile {
					command.File = filepath.Join(directory, "input.txt")
					if err := os.WriteFile(command.File, []byte(input.body), 0600); err != nil {
						t.Fatal(err)
					}
					arguments = nil
				}
				// 所有输入都在格式解析阶段失败，不会启动客户端或请求真实服务。
				err := command.execute(context.Background(), arguments)
				if err == nil {
					t.Fatal("malformed cookie unexpectedly parsed")
				}
				if strings.Contains(err.Error(), input.secret) {
					t.Error("parse error exposed a private input field")
				}
				output, readErr := os.ReadFile(logPath)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if strings.Contains(string(output), input.secret) {
					t.Error("debug log exposed a private input field")
				}
			})
		}
	}
}
