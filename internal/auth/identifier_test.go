package auth

import "testing"

func TestParseEmbyAuthorizationNormalizesMalformedSuffix(t *testing.T) {
	values := parseEmbyAuthorization(`MediaBrowser Client="VidHub\"", DeviceId="85057A58-09E5-4ED2-A428-7BFDD2873F6E\"", UserId="498f6764eaa94ec5b1688d364e473c88\""`)

	if got := values["Client"]; got != "VidHub" {
		t.Fatalf("Client = %q", got)
	}
	if got := values["DeviceId"]; got != "85057A58-09E5-4ED2-A428-7BFDD2873F6E" {
		t.Fatalf("DeviceId = %q", got)
	}
	if got := values["UserId"]; got != "498f6764eaa94ec5b1688d364e473c88" {
		t.Fatalf("UserId = %q", got)
	}
}
