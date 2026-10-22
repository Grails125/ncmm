package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFansGroupDefaultsAndIndependentCommentCredentials(t *testing.T) {
	if defaultConfig.FansGroup.WebSession {
		t.Fatal("web session must default off")
	}
	if len(defaultConfig.FansGroup.CommentCheckTokens) != 0 {
		t.Fatal("comment credential must default empty")
	}
	f := &FansGroupConf{CommentCheckTokens: map[string]string{"account-a.json": "web-a"}}
	if f.CommentCheckTokenFor("account-a.json") != "web-a" || f.CommentCheckTokenFor("account-b.json") != "" {
		t.Fatal("account credentials crossed")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("version: 1.2.2-grails.1\nfansgroup:\n  webSession: true\n  commentCheckTokens:\n    account-a.json: web-a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := AutoUpgradeConfig(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.FansGroup.WebSession || cfg.FansGroup.CommentCheckTokenFor("account-a.json") != "web-a" {
		t.Fatal("explicit choices lost during upgrade")
	}
}
