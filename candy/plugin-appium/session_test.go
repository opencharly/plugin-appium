package appium

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencharly/plugin-appium/candy/plugin-appium/params"
	"github.com/opencharly/spec/spec"
)

// session_test.go covers the appium: session method (plan Cutover E, E-5): the spawn
// request the provider hands to the runner's generic background-session service, the
// runSession validation gates, and the bounded stop-row wait. The reverse-leg
// submission itself (InvokeProvider ClassVerb session) is exercised by the R10 bed —
// the venue-driving path.

// TestBuildSessionSpawn asserts the exact spawn request the provider submits to the
// runner's generic session service: this plugin's binary in recorder mode + the
// session-file identity + recording options env, with the venue default from the
// CheckEnv snapshot applied.
func TestBuildSessionSpawn(t *testing.T) {
	in := &params.AppiumInput{
		SessionId:   "bed.member.cap",
		StateDir:    "/var/run/checks/bed/x",
		ArtifactDir: "/var/run/checks/bed/log/media",
		TimeLimit:   120,
		Fps:         15,
		VideoType:   "h264",
		Phase:       "live",
	}
	env := &checkEnv{Box: "b", Instance: "i"}
	req := buildSessionSpawn(in, env, "/usr/lib/charly/plugin-appium", "check-appium-pod", "")
	if req.Op != "spawn" {
		t.Errorf("op = %q, want spawn", req.Op)
	}
	if req.SessionID != "bed.member.cap" {
		t.Errorf("session_id = %q", req.SessionID)
	}
	if len(req.Command) != 2 || req.Command[0] != "/usr/lib/charly/plugin-appium" || req.Command[1] != "__dummy-arg" {
		t.Errorf("command = %v, want [<self> __dummy-arg]", req.Command)
	}
	if req.Env[EnvRecorder] != "1" {
		t.Errorf("CHARLY_APPIUM_RECORDER = %q, want 1", req.Env[EnvRecorder])
	}
	if req.Env[EnvBox] != "b" {
		t.Errorf("CHARLY_APPIUM_BOX = %q, want b", req.Env[EnvBox])
	}
	if req.Env[EnvInstance] != "i" {
		t.Errorf("CHARLY_APPIUM_INSTANCE = %q, want i", req.Env[EnvInstance])
	}
	if req.Env[EnvStateDir] != "/var/run/checks/bed/x" {
		t.Errorf("CHARLY_APPIUM_STATE_DIR = %q", req.Env[EnvStateDir])
	}
	if req.Env[EnvArtifactDir] != "/var/run/checks/bed/log/media" {
		t.Errorf("CHARLY_APPIUM_ARTIFACT_DIR = %q", req.Env[EnvArtifactDir])
	}
	if req.Env[EnvSessionID] != "bed.member.cap" {
		t.Errorf("CHARLY_APPIUM_SESSION_ID = %q", req.Env[EnvSessionID])
	}
	if req.Env[EnvTimeLimit] != "120" {
		t.Errorf("CHARLY_APPIUM_TIME_LIMIT = %q, want 120", req.Env[EnvTimeLimit])
	}
	if req.Env[EnvFps] != "15" {
		t.Errorf("CHARLY_APPIUM_FPS = %q, want 15", req.Env[EnvFps])
	}
	if req.Env[EnvVideoType] != "h264" {
		t.Errorf("CHARLY_APPIUM_VIDEO_TYPE = %q, want h264", req.Env[EnvVideoType])
	}
	if req.Env[EnvVenue] != "check-appium-pod" {
		t.Errorf("CHARLY_APPIUM_VENUE = %q, want check-appium-pod (CheckEnv default)", req.Env[EnvVenue])
	}
	if req.Env[EnvPhase] != "live" {
		t.Errorf("CHARLY_APPIUM_PHASE = %q, want live", req.Env[EnvPhase])
	}

	// defaults: zero time_limit spawns the 1800s cap (ALWAYS sent — Appium's own
	// default is only 180s); an empty instance omits the instance env.
	def := buildSessionSpawn(&params.AppiumInput{SessionId: "s", StateDir: "/x"}, &checkEnv{Box: "b"}, "/e", "", "")
	if def.Env[EnvTimeLimit] != "1800" {
		t.Errorf("default time_limit = %q, want 1800", def.Env[EnvTimeLimit])
	}
	if _, has := def.Env[EnvInstance]; has {
		t.Error("empty instance must omit CHARLY_APPIUM_INSTANCE")
	}
	if _, has := def.Env[EnvFps]; has {
		t.Error("zero fps must omit CHARLY_APPIUM_FPS (server default)")
	}
}

// TestRunSessionValidation guards the required-modifier semantics of the session
// method WITHOUT the reverse leg: each gate fails before any submission.
func TestRunSessionValidation(t *testing.T) {
	ctx := t.Context()
	// session_id/state_dir have provider-side fallbacks (plan-step sessions); the
	// gates that remain are action validation + the submission dispatch under a stub cc.
	if _, err := runSession(ctx, stubCC{}, &checkEnv{Box: "b"}, &params.AppiumInput{Action: "bogus"}, ""); err == nil {
		t.Error("bogus action: want error")
	}
	// a well-formed session reaches the submission (the stub cc answers an error —
	// proving the InvokeProvider dispatch path is exercised, not skipped).
	if _, err := runSession(ctx, stubCC{}, &checkEnv{Box: "b"}, &params.AppiumInput{SessionId: "s", Action: "start"}, ""); err == nil {
		t.Error("start with stub cc: want error (stub answers an error)")
	}
}

type stubCC struct{ spec.CheckContext }

func (stubCC) InvokeProvider(ctx context.Context, class, word, op string, paramsJSON, env []byte) ([]byte, error) {
	return nil, fmt.Errorf("stub: submission reached the reverse-leg dispatch (class %s word %s op %s)", class, word, op)
}

// TestWaitForEvidenceRow covers the stop path's bounded row wait: the row appears
// (the recorder's SIGTERM trap finalizes) → the bytes are returned; the row never
// appears → the deadline error names the path + timeout; a cancelled context aborts
// the wait promptly. The timeout path is the crashed-recorder case (no row, ever) —
// the stop must fail fast, not hang.
func TestWaitForEvidenceRow(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	rowPath := filepath.Join(dir, evidenceFile)
	rowJSON := "{\"instrument\":\"s\"}"

	// Row-appears path: the row lands after a short delay (the recorder's finalize
	// lagging the stop return) — the wait must return the bytes.
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(rowPath, []byte(rowJSON), 0o644)
	}()
	raw, err := waitForEvidenceRow(ctx, rowPath, 2*time.Second)
	if err != nil {
		t.Fatalf("row-appears path: %v", err)
	}
	if string(raw) != rowJSON {
		t.Fatalf("row-appears path: got %q", raw)
	}

	// Timeout path: no row ever lands — the wait must fail with the path + timeout
	// named (the crashed-recorder case).
	missing := filepath.Join(dir, "missing", evidenceFile)
	_, err = waitForEvidenceRow(ctx, missing, 150*time.Millisecond)
	if err == nil {
		t.Fatal("timeout path: want error")
	}
	if !strings.Contains(err.Error(), "evidence row missing after") {
		t.Fatalf("timeout path: error %q does not name the deadline", err)
	}

	// Cancelled-ctx path: a cancelled context aborts the wait promptly.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = waitForEvidenceRow(cctx, missing, 5*time.Second)
	if err == nil {
		t.Fatal("cancelled-ctx path: want error")
	}
}

// TestRotationAfter guards the time-budget rotation math: the bracket preemptively
// rotates at 80% of the recording cap (the device auto-stops at the cap and never
// signals the expiry).
func TestRotationAfter(t *testing.T) {
	if got := rotationAfter(0); got != 1440*time.Second {
		t.Errorf("rotationAfter(0) = %v, want 24m (default 1800s cap at 80%%)", got)
	}
	if got := rotationAfter(1800); got != 1440*time.Second {
		t.Errorf("rotationAfter(1800) = %v, want 24m", got)
	}
	if got := rotationAfter(60); got != 48*time.Second {
		t.Errorf("rotationAfter(60) = %v, want 48s", got)
	}
}

// TestEvidenceRowWire asserts the recorder's evidence row decodes as JSON with the
// shared-envelope keys (instrument/origin/verb/venue/phase/segment/artifact) — the
// shape the runner's evidence phase copies blind.
func TestEvidenceRowWire(t *testing.T) {
	row := evidenceRow{
		Instrument: "bed.member.cap",
		Origin:     "session",
		Verb:       "appium",
		Venue:      "check-appium-pod",
		Phase:      "live",
		Segment:    []map[string]any{{"session": "s1", "start": "2026-09-07T00:00:00Z", "stop": "2026-09-07T00:01:00Z", "bytes": 1234}},
		Artifact:   []evidenceArtifact{{Path: "/run/media/appium-1.mp4", Kind: "mp4"}},
	}
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("row is not JSON: %v", err)
	}
	for _, k := range []string{"instrument", "origin", "verb", "venue", "phase", "segment", "artifact"} {
		if _, ok := m[k]; !ok {
			t.Errorf("row missing key %q: %s", k, b)
		}
	}
}

// TestSessionStartClearsStaleSessionFile covers the E-5 R1 2026-09-08 (run
// 2026.251.1016) cross-run contamination: the ONE-identity fixture never deletes
// the session file, so the NEXT run's recorder would open its first bracket
// against the PRIOR run's dead session/endpoint. sessionStart deletes the stale
// file before spawning the new recorder.
func TestSessionStartClearsStaleSessionFile(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	sf, err := appiumSessionPath("b", "")
	if err != nil {
		t.Fatalf("appiumSessionPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sf), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(sf, []byte("{\"session_id\":\"stale\",\"base_url\":\"http://127.0.0.1:1\"}"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	// the start dispatch reaches the stub submission AFTER clearing the stale file —
	// the stub errors, proving the dispatch path ran and the delete preceded it.
	if _, err := sessionStart(t.Context(), stubCC{}, &checkEnv{Box: "b"}, &params.AppiumInput{SessionId: "s", Action: "start", StateDir: "/x"}, ""); err == nil {
		t.Fatal("sessionStart with stub cc: want error")
	}
	if _, statErr := os.Stat(sf); !os.IsNotExist(statErr) {
		t.Fatalf("stale session file still present after session start: %v", statErr)
	}
}
