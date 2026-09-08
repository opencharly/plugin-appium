package appium

import (
	"os"
	"testing"
	"time"
)

// TestPersistedSessionHandleSharedAcrossVerbs is the E-5 handle-sharing proof: the
// plan's appium session-create persists ONE on-disk handle (image-keyed at
// ~/.cache/charly/appium/sessions/<image>.json), and every later consumer resolves
// the SAME session id + base URL from that file — the find/click/app-* verb path via
// resolveW3CSession, and the detached recorder via loadAppiumSession. One identity
// end-to-end is the E-4 one-session fixture rule the E-5 bed mirrors (the check
// fixture's session-create/launch/find/stop/delete all share this handle).
func TestPersistedSessionHandleSharedAcrossVerbs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	want := &AppiumSession{
		SessionID: "3355699a-218d-482d-ae2a-87037b4b8e44",
		BaseURL:   "http://127.0.0.1:33557/wd/hub",
		CreatedAt: time.Now().UTC(),
		Image:     "check-android-emulator-pod",
	}
	if err := saveAppiumSession(want); err != nil {
		t.Fatalf("saveAppiumSession: %v", err)
	}

	// The in-plan verb path (find/click/app-start-activity): the persisted handle.
	got, err := resolveW3CSession(want.Image, "", "", "")
	if err != nil {
		t.Fatalf("resolveW3CSession: %v", err)
	}
	if got.SessionID != want.SessionID {
		t.Errorf("verb handle = %q, want %q", got.SessionID, want.SessionID)
	}
	if got.BaseURL != want.BaseURL {
		t.Errorf("verb base = %q, want %q", got.BaseURL, want.BaseURL)
	}

	// The detached-recorder path (recorder.go polls loadAppiumSession(box, instance)
	// with the same image key): the same handle again.
	loaded, err := loadAppiumSession(want.Image, "")
	if err != nil {
		t.Fatalf("loadAppiumSession: %v", err)
	}
	if loaded == nil || loaded.SessionID != want.SessionID || loaded.BaseURL != want.BaseURL {
		t.Fatalf("recorder handle = %+v, want session %q at %q", loaded, want.SessionID, want.BaseURL)
	}

	// Image-key isolation: another bed's key must not leak this handle.
	other, err := loadAppiumSession("some-other-pod", "")
	if err != nil {
		t.Fatalf("cross-image load: %v", err)
	}
	if other != nil {
		t.Fatalf("cross-image handle leak: %+v", other)
	}

	// The session id is a bearer token for the live server: the file is 0600.
	path, err := appiumSessionPath(want.Image, "")
	if err != nil {
		t.Fatalf("appiumSessionPath: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat session file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("session file perms = %o, want 600", perm)
	}
}
