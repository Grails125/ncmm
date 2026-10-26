package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/3899/ncmm/config"
	"gopkg.in/yaml.v3"
)

func TestConfigurationSchemaPathsAreUniqueAndDefaultsMatch(t *testing.T) {
	schema := configurationSchema()
	if schema.SchemaVersion <= 0 {
		t.Fatal("schema version must be positive")
	}
	var defaults any
	if err := yaml.Unmarshal(config.DefaultYAML(), &defaults); err != nil {
		t.Fatal(err)
	}

	for targetName, target := range schema.Targets {
		seen := make(map[string]struct{}, len(target.Fields))
		groups := make(map[string]struct{})
		for _, category := range target.Categories {
			for _, group := range category.Groups {
				groups[group.ID] = struct{}{}
			}
		}
		for _, field := range target.Fields {
			if _, exists := seen[field.Path]; exists {
				t.Fatalf("%s schema path %q is duplicated", targetName, field.Path)
			}
			seen[field.Path] = struct{}{}
			if _, exists := groups[field.Group]; !exists {
				t.Fatalf("%s schema field %q uses unknown group %q", targetName, field.Path, field.Group)
			}
			if targetName != "config" {
				continue
			}
			value, exists := schemaValueAt(defaults, field.Path)
			if !exists {
				continue // Optional fields may be absent from the compact default YAML.
			}
			if !reflect.DeepEqual(value, field.Default) {
				t.Fatalf("default for %q = %#v; want %#v", field.Path, field.Default, value)
			}
		}
	}
}

func TestDeprecatedPlaySessionIsReadOnlyAndWarnsAboutRisk(t *testing.T) {
	schema := configurationSchema().Targets["config"]
	if len(schema.Categories) != 16 {
		t.Fatalf("config category count = %d; deprecated play must reuse playids", len(schema.Categories))
	}
	playSourceGroup := false
	for _, category := range schema.Categories {
		if category.ID == "play" {
			t.Fatal("deprecated play must not create a separate configuration category")
		}
		if category.ID == "playids" {
			for _, group := range category.Groups {
				playSourceGroup = playSourceGroup || group.ID == "play-source"
			}
		}
	}
	var defaults any
	if err := yaml.Unmarshal(config.DefaultYAML(), &defaults); err != nil {
		t.Fatal(err)
	}
	if value, exists := schemaValueAt(defaults, "play.webSession"); !exists || value != false {
		t.Fatalf("deprecated play compatibility default = %#v, exists = %t", value, exists)
	}
	for _, field := range schema.Fields {
		if field.Path == "play.webSession" {
			if !playSourceGroup || field.Group != "play-source" || !field.ReadOnly || !strings.Contains(field.Title, "已废弃") || !strings.Contains(field.Description, "风控") {
				t.Fatalf("deprecated play session must not be enabled by the form: %+v", field)
			}
			return
		}
	}
	t.Fatal("deprecated play session explanation missing")
}

func TestMainConfigurationSchemaCoversDefaultFields(t *testing.T) {
	schema := configurationSchema().Targets["config"]
	if len(schema.Categories) == 0 || schema.Categories[0].ID != "task" {
		t.Fatalf("first config category = %q; want task", schema.Categories[0].ID)
	}
	registered := make(map[string]struct{}, len(schema.Fields))
	for _, field := range schema.Fields {
		registered[field.Path] = struct{}{}
	}
	var defaults any
	if err := yaml.Unmarshal(config.DefaultYAML(), &defaults); err != nil {
		t.Fatal(err)
	}
	var missing []string
	collectUnregisteredSchemaPaths(defaults, nil, registered, &missing)
	if len(missing) > 0 {
		t.Fatalf("default configuration fields missing from schema: %s", strings.Join(missing, ", "))
	}
}

func collectUnregisteredSchemaPaths(value any, path []string, registered map[string]struct{}, missing *[]string) {
	joined := strings.Join(path, ".")
	if _, ok := registered[joined]; ok {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if len(path) == 0 && key == "version" {
				continue
			}
			collectUnregisteredSchemaPaths(child, append(path, key), registered, missing)
		}
	case []any:
		if len(path) > 0 {
			*missing = append(*missing, joined)
		}
	default:
		if len(path) > 0 {
			*missing = append(*missing, joined)
		}
	}
}

func TestConfigSchemaAPI(t *testing.T) {
	server := newAuthTestServer(t, t.TempDir())
	credentials, err := server.authManager.Setup(context.Background(), "Admin#123", requestClientInfo(httptest.NewRequest(http.MethodGet, "/", nil)))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/config/schema", nil)
	request.Host = "localhost:3899"
	request.AddCookie(&http.Cookie{Name: "ncmm_session", Value: credentials.Token})
	response := httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("schema status = %d, body = %s", response.Code, response.Body.String())
	}
	var got configSchemaView
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != configSchemaVersion || len(got.Targets["config"].Fields) == 0 || len(got.Targets["notify"].Fields) == 0 {
		t.Fatalf("unexpected schema response: %+v", got)
	}
}

func TestMusicPartnerSchemaRangesAndCommentPools(t *testing.T) {
	fields := make(map[string]configSchemaField)
	for _, field := range configurationSchema().Targets["config"].Fields {
		fields[field.Path] = field
	}
	for _, legacy := range []string{"musicPartner.score", "musicPartner.extraLimit", "musicPartner.recommendCount"} {
		if _, exists := fields[legacy]; exists {
			t.Fatalf("legacy control %q must remain hidden", legacy)
		}
	}
	for _, want := range []struct {
		path         string
		defaultValue int
		min, max     float64
		unit         string
	}{
		{"musicPartner.scoreMin", 3, 1, 5, "分"},
		{"musicPartner.scoreMax", 4, 1, 5, "分"},
		{"musicPartner.extraLimitMin", 5, 0, 15, "首"},
		{"musicPartner.extraLimitMax", 15, 0, 15, "首"},
		{"musicPartner.waitMin", 15, 1, 300, "秒"},
		{"musicPartner.waitMax", 20, 1, 300, "秒"},
		{"musicPartner.recommendWaitMin", 16, 16, 300, "秒"},
		{"musicPartner.recommendWaitMax", 20, 16, 300, "秒"},
	} {
		t.Run(want.path, func(t *testing.T) {
			field, exists := fields[want.path]
			if !exists || field.Type != "integer" || field.Widget != "number" || field.Unit != want.unit {
				t.Fatalf("unexpected range control: %+v", field)
			}
			if field.Min == nil || *field.Min != want.min || field.Max == nil || *field.Max != want.max || field.Step == nil || *field.Step != 1 {
				t.Fatalf("invalid range constraints: %+v", field)
			}
			if field.Default != want.defaultValue {
				t.Fatalf("default = %#v, want %d", field.Default, want.defaultValue)
			}
		})
	}
	for _, path := range []string{"musicPartner.recommendCountMin", "musicPartner.recommendCountMax"} {
		field := fields[path]
		if field.Default != 0 || field.Min == nil || *field.Min != 0 || field.Max != nil || field.Step == nil || *field.Step != 1 || field.Type != "integer" || field.Widget != "number" || field.Unit != "首" {
			t.Fatalf("推荐目标应默认为0且不限制上限: %+v", field)
		}
	}
	order := fields["musicPartner.recommendOrder"]
	if order.Default != "sequential" || order.Widget != "segmented" || len(order.Options) != 2 || order.Options[0].Value != "sequential" || order.Options[0].Label != "按列表顺序" || order.Options[1].Value != "random" || order.Options[1].Label != "随机抽取" {
		t.Fatalf("推荐抽取顺序配置缺失: %+v", order)
	}
	for _, score := range []string{"1", "2", "3", "4", "5"} {
		field, exists := fields["musicPartner.comments."+score]
		if !exists || field.Type != "string-list" || field.Widget != "textarea-list" || field.Group != "partner-comments" {
			t.Fatalf("missing editable comment pool for score %s: %+v", score, field)
		}
		encoded, err := json.Marshal(field.Default)
		want, _ := json.Marshal(config.GetDefault().MusicPartner.Comments[score])
		if err != nil || string(encoded) == "[]" || string(encoded) != string(want) {
			t.Fatalf("score %s comment default = %s, want configured templates %s; error = %v", score, encoded, want, err)
		}
	}
	if fields["musicPartner.syncYunCircle"].Default != false {
		t.Fatal("YunCircle publication must remain disabled by default")
	}
	for _, path := range []string{"musicPartner.recommendNotes", "musicPartner.recommendComments"} {
		field, exists := fields[path]
		if !exists || field.Type != "string-list" || field.Widget != "textarea-list" || field.Group != "partner-recommend" {
			t.Fatalf("missing recommendation pool: %+v", field)
		}
		encoded, err := json.Marshal(field.Default)
		var values []string
		if err != nil || json.Unmarshal(encoded, &values) != nil || len(values) == 0 {
			t.Fatalf("recommendation pool must provide editable default templates: %s", encoded)
		}
	}
}
