package weapi

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/3899/ncmm/api/types"
)

func assertPartnerJSON(t *testing.T, value any, expected string) {
	t.Helper()
	actual, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]json.RawMessage
	if err := json.Unmarshal(actual, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wire JSON = %s; want %s", actual, expected)
	}
}

func TestPartnerEvaluateJSONMatchesSignerContract(t *testing.T) {
	req := PartnerEvaluateReq{ReqCommon: types.ReqCommon{CSRFToken: "test-csrf"}, TaskId: "9007199254740993", WorkId: "8977849", Score: "3", Tags: ThreeAOnePartnerTags, CustomTags: "%5B%5D"}
	t.Run("daily_without_optional_fields", func(t *testing.T) {
		assertPartnerJSON(t, req, `{"taskId":9007199254740993,"workId":8977849,"score":"3","tags":"3-A-1","customTags":"%5B%5D","comment":"","syncYunCircle":"false","csrf_token":"test-csrf"}`)
	})
	t.Run("extra_with_explicit_circle_sync", func(t *testing.T) {
		copy := req
		copy.ExtraResource, copy.SyncYunCircle = true, true
		assertPartnerJSON(t, &copy, `{"taskId":9007199254740993,"workId":8977849,"score":"3","tags":"3-A-1","customTags":"%5B%5D","comment":"","syncYunCircle":"true","extraResource":"true","csrf_token":"test-csrf"}`)
	})
	t.Run("preserve_explicit_optional_values", func(t *testing.T) {
		copy := req
		copy.CustomTags, copy.Comment, copy.Source = "[]", "测试", "mp-music-partner"
		copy.SyncComment, copy.ExtraScore = true, `{"1":3}`
		assertPartnerJSON(t, &copy, `{"taskId":9007199254740993,"workId":8977849,"score":"3","tags":"3-A-1","customTags":"[]","comment":"测试","syncYunCircle":"false","syncComment":true,"source":"mp-music-partner","extraScore":"{\"1\":3}","csrf_token":"test-csrf"}`)
	})
}

func TestPartnerExtraReportJSONMatchesSignerContract(t *testing.T) {
	req := PartnerExtraReportReq{ReqCommon: types.ReqCommon{CSRFToken: "test-csrf"}, WorkId: "8977849", ResourceId: "9007199254740993", InteractType: "PLAY_END"}
	assertPartnerJSON(t, req, `{"workId":8977849,"resourceId":9007199254740993,"bizResourceId":"","interactType":"PLAY_END","csrf_token":"test-csrf"}`)
}

func TestPartnerJSONRejectsInvalidIDs(t *testing.T) {
	for _, id := range []string{"", "0", "-1", "1.5", "invalid", "9223372036854775808"} {
		requests := []any{
			PartnerEvaluateReq{TaskId: id, WorkId: "1"},
			PartnerEvaluateReq{TaskId: "1", WorkId: id},
			PartnerExtraReportReq{WorkId: id, ResourceId: "1"},
			PartnerExtraReportReq{WorkId: "1", ResourceId: id},
		}
		for _, req := range requests {
			if _, err := json.Marshal(req); err == nil {
				t.Errorf("%T accepted invalid ID %q", req, id)
			}
		}
	}
}
