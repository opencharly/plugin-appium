package appium

// w3c_create_test.go — the coverage for the W3C `POST /session` boundary.
//
// THE BOUNDARY IS A LIVE APPIUM HTTP API, AND R8 GOVERNS IT: a test that crosses a real
// service boundary runs against that service, or skips visibly when its endpoint is absent.
// It is never mocked. So this file has TWO kinds of case, and they do not substitute for
// each other:
//
//   - TestAppiumCreateSessionRequestShape asserts the WIRE SHAPE — URL, method, header and
//     the `capabilities.alwaysMatch` envelope — on a PURE function. No server is involved,
//     so nothing is faked and R8 does not apply: there is no boundary in that test.
//
//   - TestAppiumCreateSessionRemoteLive crosses the REAL boundary and SKIPS VISIBLY when
//     APPIUM_SERVER_URL is unset.
//
// Keeping them apart is what makes each honest: a server in the shape test would be a fake
// OF the boundary (what R8 names), and a shape assertion inside the live test would be
// unreachable whenever no Appium is running.

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// TestAppiumCreateSessionRequestShape pins the W3C request the plugin sends, with no server
// and therefore no boundary: unit coverage of a pure builder.
func TestAppiumCreateSessionRequestShape(t *testing.T) {
	req, err := appiumCreateSessionRequest("http://127.0.0.1:4723", map[string]any{
		"platformName":      "Android",
		"appium:deviceName": "emulator-5554",
	})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	if req.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", req.Method)
	}
	if got, want := req.URL.String(), "http://127.0.0.1:4723/session"; got != want {
		t.Errorf("url = %q, want %q — the path is /session, appended to the caller's base", got, want)
	}
	if ct := req.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	var body struct {
		Capabilities struct {
			AlwaysMatch map[string]any `json:"alwaysMatch"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("the body is not the W3C envelope: %v (%s)", err, raw)
	}
	if body.Capabilities.AlwaysMatch["platformName"] != "Android" {
		t.Errorf("alwaysMatch.platformName = %v, want the caller's cap — caps go under "+
			"capabilities.alwaysMatch, not at the top level",
			body.Capabilities.AlwaysMatch["platformName"])
	}
	if _, ok := body.Capabilities.AlwaysMatch["appium:deviceName"]; !ok {
		t.Error("a namespaced cap was dropped — the envelope must pass the caller's map through")
	}
}

// TestAppiumCreateSessionRequestRejectsABadBase is the error half of the pure builder.
func TestAppiumCreateSessionRequestRejectsABadBase(t *testing.T) {
	if _, err := appiumCreateSessionRequest("://not-a-url", nil); err == nil {
		t.Error("a malformed base was accepted — the request would fail at the boundary instead of here")
	}
}

// TestAppiumCreateSessionRemoteLive crosses the REAL boundary, or skips visibly.
func TestAppiumCreateSessionRemoteLive(t *testing.T) {
	base := os.Getenv("APPIUM_SERVER_URL")
	if base == "" {
		t.Skip("APPIUM_SERVER_URL is unset: no Appium endpoint to cross. R8 requires the live " +
			"boundary or a VISIBLE skip, and this is the skip — the wire shape is covered " +
			"separately by TestAppiumCreateSessionRequestShape, which needs no boundary.")
	}

	sid, err := appiumCreateSessionRemote(base, map[string]any{
		"platformName":          "Android",
		"appium:automationName": "UiAutomator2",
	})
	if err != nil {
		t.Fatalf("create session against the live Appium at %s: %v", base, err)
	}
	if strings.TrimSpace(sid) == "" {
		t.Fatal("the live Appium returned an empty sessionId — a session was claimed but not made")
	}
	t.Logf("live Appium created session %s", sid)

	if err := appiumDeleteSessionRemote(base, sid); err != nil {
		t.Errorf("deleting session %s: %v", sid, err)
	}
}
