package appium

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// session_file_key_test.go — the E-5 R3 per-candy session-file key contract: an
// authored `session_file:` keys a step's WebDriver session in an ISOLATED file
// (<box>[_<instance>]_<key>.json) instead of the SHARED box file, so independent
// session lifecycles (the baked av-suite vs the fixture+recorder) never churn each
// other's file (runs 2026.251.1102/1122: the shared-file rotations deleted the
// recorder's pinned fixture session mid-run). The DEFAULT (empty key) is the shared
// box file — every existing consumer unchanged.

// TestSessionFileKeyDefaultShared proves the default: an empty key resolves EXACTLY
// the shared box file (backward compat — no existing consumer changes behaviour).
func TestSessionFileKeyDefaultShared(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	shared, err := appiumSessionPath("bed", "i")
	if err != nil {
		t.Fatalf("appiumSessionPath: %v", err)
	}
	keyed, err := appiumSessionPathKeyed("bed", "i", "")
	if err != nil {
		t.Fatalf("appiumSessionPathKeyed(empty): %v", err)
	}
	if keyed != shared {
		t.Errorf("empty key path = %q, want the shared file %q", keyed, shared)
	}
}

// TestSessionFileKeyIsolatesTheBakedLifecycle proves the isolation round-trip: a
// session saved under the "baked" key lands in its OWN file and is invisible to the
// shared-file readers (and vice versa) — the two lifecycles cannot churn each other.
func TestSessionFileKeyIsolatesTheBakedLifecycle(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	baked := &AppiumSession{
		SessionID: "baked-1111", BaseURL: "http://127.0.0.1:9/wd/hub",
		CreatedAt: time.Now().UTC(), Image: "bed",
	}
	if err := saveAppiumSessionKeyed(baked, "baked"); err != nil {
		t.Fatalf("saveAppiumSessionKeyed(baked): %v", err)
	}
	// round-trip: the keyed reader sees it
	got, err := loadAppiumSessionKeyed("bed", "", "baked")
	if err != nil {
		t.Fatalf("loadAppiumSessionKeyed: %v", err)
	}
	if got == nil || got.SessionID != "baked-1111" {
		t.Fatalf("keyed round-trip = %+v, want session baked-1111", got)
	}
	// isolation: the SHARED file reader must NOT see the baked session
	if shared, err := loadAppiumSession("bed", ""); err != nil || shared != nil {
		t.Fatalf("shared reader sees the baked session (leak): %+v err=%v", shared, err)
	}
	// isolation: the keyed file must be its own path (not the shared one)
	path, err := appiumSessionPathKeyed("bed", "", "baked")
	if err != nil {
		t.Fatalf("appiumSessionPathKeyed: %v", err)
	}
	sharedPath, _ := appiumSessionPath("bed", "")
	if path == sharedPath {
		t.Fatalf("keyed path == shared path (%q)", path)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0600 {
		t.Errorf("keyed file perms = %v, want 600 (the session id is a bearer)", fi)
	}
	// the shared lifecycle still works untouched (writes + reads the shared file)
	shared := &AppiumSession{SessionID: "shared-2222", BaseURL: "http://127.0.0.1:9/wd/hub", CreatedAt: time.Now().UTC(), Image: "bed"}
	if err := saveAppiumSession(shared); err != nil {
		t.Fatalf("saveAppiumSession: %v", err)
	}
	if s, err := loadAppiumSession("bed", ""); err != nil || s == nil || s.SessionID != "shared-2222" {
		t.Fatalf("shared round-trip = %+v err=%v", s, err)
	}
	// delete the keyed file; the shared file survives
	if err := deleteAppiumSessionKeyed("bed", "", "baked"); err != nil {
		t.Fatalf("deleteAppiumSessionKeyed: %v", err)
	}
	if s, _ := loadAppiumSession("bed", ""); s == nil || s.SessionID != "shared-2222" {
		t.Errorf("shared session lost after keyed delete: %+v", s)
	}
}

// TestSessionFileKeyPathShape guards the exact keyed filename: the suffix appends
// AFTER the instance suffix — <box>_<instance>_<key>.json.
func TestSessionFileKeyPathShape(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	p, err := appiumSessionPathKeyed("check-android-emulator-pod", "i2", "baked")
	if err != nil {
		t.Fatalf("appiumSessionPathKeyed: %v", err)
	}
	want := filepath.Join(xdg, "charly", "appium", "sessions", "check-android-emulator-pod_i2_baked.json")
	if p != want {
		t.Errorf("keyed path = %q, want %q", p, want)
	}
}
