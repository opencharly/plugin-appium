package appium

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAppiumCreateSessionRemote pins the W3C session-create that replaced github.com/tebeka/selenium
// (a 2019 library used for exactly this POST and five string constants). The library is gone, so the
// WIRE SHAPE is now this plugin's own responsibility and must be asserted rather than assumed:
// POST /session, a {"capabilities":{"alwaysMatch":…}} body, and the id read from value.sessionId.
//
// A stub that returned an id unconditionally would satisfy a naive test; this one asserts the
// request the server actually received, so it fails if the body shape regresses to tebeka's
// {"desiredCapabilities":…} form.
func TestAppiumCreateSessionRemote(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":{"sessionId":"abc-123","capabilities":{}}}`))
	}))
	defer srv.Close()

	sid, err := appiumCreateSessionRemote(srv.URL, map[string]any{"platformName": "Android"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if sid != "abc-123" {
		t.Fatalf("session id = %q, want the value.sessionId the server returned", sid)
	}
	if gotMethod != http.MethodPost || gotPath != "/session" {
		t.Fatalf("request = %s %s, want POST /session", gotMethod, gotPath)
	}
	caps, ok := gotBody["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("body carries no capabilities object: %v", gotBody)
	}
	always, ok := caps["alwaysMatch"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities carries no alwaysMatch (the W3C shape): %v", caps)
	}
	if always["platformName"] != "Android" {
		t.Fatalf("alwaysMatch.platformName = %v, want the caller's caps", always["platformName"])
	}
}

// TestAppiumCreateSessionRemoteSurfacesServerError pins the OTHER half: a refusal must be loud and
// carry the server's own words, never a silent empty session id.
func TestAppiumCreateSessionRemoteSurfacesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"value":{"error":"session not created","message":"no such device"}}`))
	}))
	defer srv.Close()

	sid, err := appiumCreateSessionRemote(srv.URL, map[string]any{})
	if err == nil {
		t.Fatalf("a 400 reply returned no error (got session %q)", sid)
	}
	if !strings.Contains(err.Error(), "no such device") {
		t.Fatalf("error %q does not carry the server's message", err)
	}
}
