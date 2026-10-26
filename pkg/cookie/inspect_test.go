package cookie

import (
	"testing"
	"time"
)

func TestHasNonEmptyCoversScopedCookiesWithoutExposingValues(t *testing.T) {
	jar, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &Cookie{jar: jar}
	jar.entries = map[string]map[string]entry{"music.163.com": {
		"scoped":  {Name: "appver", Value: "9.6.05", Path: "/eapi/private", HostOnly: true, Domain: "interface3.music.163.com"},
		"empty":   {Name: "os", Value: " "},
		"expired": {Name: "buildver", Value: "1234", Persistent: true, Expires: time.Now().Add(-time.Hour)},
	}}
	if !c.HasNonEmpty("appver") {
		t.Fatal("scoped identity was missed")
	}
	if c.HasNonEmpty("os", "buildver", "missing") {
		t.Fatal("empty or expired identity should not count")
	}
	if len(jar.entries["music.163.com"]) != 3 {
		t.Fatal("inspection mutated cookie storage")
	}
}
