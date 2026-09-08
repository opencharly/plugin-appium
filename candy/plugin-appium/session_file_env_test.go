package appium

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencharly/plugin-appium/candy/plugin-appium/params"
)


// TestBuildSessionSpawnStampsSessionFile is the E-5 cross-process env-agreement guard:
// the session-start provider stamps EnvSessionFile with the exact path the plan's
// session-create writes (appiumSessionPath in the SAME serve env), so the detached
// recorder polls the file the plan actually updates — never a HOME/XDG_CACHE_HOME-
// re-derived path that diverges from the serve env (E-5 R1: the zero-bracket recorder).
func TestBuildSessionSpawnStampsSessionFile(t *testing.T) {
	const box, inst = "check-android-emulator-pod", ""
	env := &checkEnv{Box: box, Instance: inst}
	e := buildSessionSpawn(&params.AppiumInput{}, env, "/x/plugin-appium", "nested:podman-exec:pod/local", "logdir")
	got := e.Env[EnvSessionFile]
	if got == "" {
		t.Fatalf("buildSessionSpawn env missing %s", EnvSessionFile)
	}
	want, err := appiumSessionPath(box, inst)
	if err != nil {
		t.Fatalf("appiumSessionPath: %v", err)
	}
	if got != want {
		t.Errorf("stamped session file = %q, want %q (the path session-create saveAppiumSession writes)", got, want)
	}
}

// TestRecorderPrefersStampedSessionFile proves the recorder reads the STAMPED path even
// when its own env would re-derive a different one (XDG_CACHE_HOME divergence) — the
// exact E-5 failure mode.
func TestRecorderPrefersStampedSessionFile(t *testing.T) {
	tmp := t.TempDir()
	stamped := filepath.Join(tmp, "sessions", "check-android-emulator-pod.json")
	if err := os.MkdirAll(filepath.Dir(stamped), 0o700); err != nil {
		t.Fatal(err)
	}
	want := &AppiumSession{SessionID: "stamped-1111-2222", BaseURL: "http://127.0.0.1:9/wd/hub", Image: "check-android-emulator-pod"}
	raw, _ := json.Marshal(want)
	if err := os.WriteFile(stamped, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// Different XDG_CACHE_HOME so a re-derived path would NOT be the stamped one.
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "other-cache"))
	got, err := loadAppiumSessionPath(stamped)
	if err != nil {
		t.Fatalf("loadAppiumSessionPath: %v", err)
	}
	if got.SessionID != want.SessionID {
		t.Errorf("recorder session = %q, want %q (must read the stamped file)", got.SessionID, want.SessionID)
	}
}
