package proxy

import (
	"net/http"
	"testing"

	"emby-media-portal/internal/auth"
)

func TestClassifyTrafficTreatsEmbyPublicPathsAsPublic(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://example.com/emby/system/info/public", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	if got := classifyTraffic(req, ""); got != "public" {
		t.Fatalf("classifyTraffic() = %q, want public", got)
	}
}

func TestLookupRecentPlaybackUserFallsBackToRecentAuthenticatedClient(t *testing.T) {
	p := &Proxy{
		playbackUsers: make(map[string]recentPlaybackUser),
		recentUsers:   make(map[string]recentPlaybackUser),
	}

	progressReq, err := http.NewRequest(http.MethodPost, "http://example.com/emby/Sessions/Playing/Progress", nil)
	if err != nil {
		t.Fatalf("new progress request: %v", err)
	}
	progressReq.RemoteAddr = "203.0.113.10:12345"
	progressReq.Header.Set("User-Agent", "Mozilla/5.0")

	client := &auth.ClientInfo{
		ClientName: "Mozilla",
		UserAgent:  "Mozilla/5.0",
	}

	p.rememberRecentUser(progressReq, client, "user-123", "Rudy")

	segmentReq, err := http.NewRequest(http.MethodGet, "http://example.com/emby/videos/88876/hls1/main/1.ts", nil)
	if err != nil {
		t.Fatalf("new segment request: %v", err)
	}
	segmentReq.RemoteAddr = "203.0.113.10:54321"
	segmentReq.Header.Set("User-Agent", "Mozilla/5.0")

	recent, ok := p.lookupRecentPlaybackUser(segmentReq, client)
	if !ok {
		t.Fatal("lookupRecentPlaybackUser() did not find recent user")
	}
	if recent.UserID != "user-123" {
		t.Fatalf("lookupRecentPlaybackUser() user id = %q, want user-123", recent.UserID)
	}
	if recent.UserName != "Rudy" {
		t.Fatalf("lookupRecentPlaybackUser() user name = %q, want Rudy", recent.UserName)
	}
}
