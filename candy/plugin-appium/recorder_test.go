package appium

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder_test.go covers the DETACHED session recorder (plan Cutover E, E-5) over a
// FAKE WebDriver session: an httptest server speaking the W3C recording endpoints
// (start_recording_screen / stop_recording_screen) the recorder dials — the same
// stub approach plugin-vnc's session tests use, HTTP-flavored. The
// reverse-leg/venue-driving path belongs to the R10 bed.

// fakeAppiumServer is a minimal Appium WebDriver server stub: it serves the two
// session-scoped recording endpoints and records every start (with its body) + stop.
type fakeAppiumServer struct {
	*httptest.Server
	mu     sync.Mutex
	starts int
	stops  int
	bodies []map[string]any
	video  []byte
}

func newFakeAppiumServer(t *testing.T, video []byte) *fakeAppiumServer {
	f := &fakeAppiumServer{video: video}
	mux := http.NewServeMux()
	mux.HandleFunc("/wd/hub/session/", func(w http.ResponseWriter, r *http.Request) {
		tail := strings.TrimPrefix(r.URL.Path, "/wd/hub/session/")
		if i := strings.IndexByte(tail, '/'); i >= 0 {
			tail = tail[i+1:]
		}
		switch tail {
		case "appium/start_recording_screen":
			f.mu.Lock()
			f.starts++
			var body map[string]any
			if r.Body != nil {
				_ = json.NewDecoder(r.Body).Decode(&body)
			}
			f.bodies = append(f.bodies, body)
			f.mu.Unlock()
			writeW3CValue(w, "")
		case "appium/stop_recording_screen":
			f.mu.Lock()
			f.stops++
			f.mu.Unlock()
			writeW3CValue(w, base64.StdEncoding.EncodeToString(video))
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	f.Server = srv
	t.Cleanup(srv.Close)
	return f
}

func (f *fakeAppiumServer) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops
}

func (f *fakeAppiumServer) startBody(n int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n < 1 || n > len(f.bodies) {
		return nil
	}
	return f.bodies[n-1]
}

func writeW3CValue(w http.ResponseWriter, value string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"value": value})
}

// waitFor polls cond until true or the deadline passes.
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}

func saveTestSession(t *testing.T, xdg, box, sid, baseURL string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", xdg)
	if err := saveAppiumSession(&AppiumSession{
		SessionID: sid,
		BaseURL:   baseURL,
		CreatedAt: time.Now().UTC(),
		Image:     box,
	}); err != nil {
		t.Fatalf("saveAppiumSession: %v", err)
	}
}

// fakeVideo is the opaque MP4 payload the fake server returns base64-encoded (the
// recorder treats the video as bytes — round-trip fidelity is the contract).
var fakeVideo = []byte("fake-mp4-payload-bytes")

// TestRunSessionRecorderBracketsAndFinalizes is the recording-wire contract over the
// fake WebDriver session: the recorder discovers the session file, opens the bracket
// with the timeLimit ALWAYS in the start body, and on done pulls the video, lands the
// MP4 artifact, and finalizes FINAL + row.json.
func TestRunSessionRecorderBracketsAndFinalizes(t *testing.T) {
	stateDir := t.TempDir()
	artDir := t.TempDir()
	fake := newFakeAppiumServer(t, fakeVideo)
	saveTestSession(t, t.TempDir(), "bed", "sid-1", fake.URL+"/wd/hub")

	cfg := RecorderConfig{
		Box: "bed", StateDir: stateDir, ArtifactDir: artDir,
		SessionID: "bed.member.cap", Venue: "check-appium-pod", Phase: "live",
		TimeLimit: 60, PollInterval: 20 * time.Millisecond,
	}
	done := make(chan struct{})
	ch := make(chan error, 1)
	go func() { ch <- RunSessionRecorder(cfg, done) }()
	waitFor(t, 2*time.Second, func() bool { s, _ := fake.counts(); return s >= 1 })
	close(done)
	if err := <-ch; err != nil {
		t.Fatalf("RunSessionRecorder: %v", err)
	}

	s, stop := fake.counts()
	if s != 1 || stop != 1 {
		t.Fatalf("start/stop calls = %d/%d, want 1/1", s, stop)
	}
	body := fake.startBody(1)
	if body == nil {
		t.Fatal("no start body captured")
	}
	if tl, ok := body["timeLimit"].(float64); !ok || int(tl) != 60 {
		t.Errorf("start body timeLimit = %v, want 60 (ALWAYS sent)", body["timeLimit"])
	}

	// the pulled MP4 lands in the artifact dir with the exact fake bytes
	got, err := os.ReadFile(filepath.Join(artDir, "appium-1.mp4"))
	if err != nil {
		t.Fatalf("appium-1.mp4: %v", err)
	}
	if !bytes.Equal(got, fakeVideo) {
		t.Errorf("artifact bytes = %d, want %d (base64 round trip)", len(got), len(fakeVideo))
	}

	// FINAL marker + the evidence row
	marker, err := os.ReadFile(filepath.Join(stateDir, finalMarker))
	if err != nil {
		t.Fatalf("FINAL marker missing after stop: %v", err)
	}
	if want := "final segments=1 bytes=" + strconv.Itoa(len(fakeVideo)) + "\n"; string(marker) != want {
		t.Errorf("FINAL content = %q, want %q", marker, want)
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, evidenceFile))
	if err != nil {
		t.Fatalf("row.json missing after stop: %v", err)
	}
	var row evidenceRow
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("decode row.json: %v", err)
	}
	if row.Instrument != "bed.member.cap" || row.Origin != "session" || row.Verb != "appium" ||
		row.Venue != "check-appium-pod" || row.Phase != "live" {
		t.Errorf("row provenance = %+v", row)
	}
	if len(row.Artifact) != 1 || row.Artifact[0].Kind != "mp4" ||
		row.Artifact[0].Path != filepath.Join(artDir, "appium-1.mp4") {
		t.Errorf("row artifact = %+v", row.Artifact)
	}
	if len(row.Segment) != 1 {
		t.Fatalf("row segments = %d, want 1", len(row.Segment))
	}
	if b, ok := row.Segment[0]["bytes"].(float64); !ok || int(b) != len(fakeVideo) {
		t.Errorf("segment bytes = %v, want %d", row.Segment[0]["bytes"], len(fakeVideo))
	}
}

// TestRecorderIgnoresSessionFileRotation is the E-5 R2 ONE-identity pin contract:
// the recorder pins the FIRST session it brackets (the fixture's session-create) and
// a rotation of the SHARED session file to a DIFFERENT id — the baked suite's
// transient session-create/delete churn — must NOT close the current bracket NOR
// re-bracket onto the new id (run 2026.251.1102: the recorder chased three churned
// identities and its final stop 404'd on the baked suite's dead session). The pinned
// bracket holds to finalize and its video still lands.
func TestRecorderIgnoresSessionFileRotation(t *testing.T) {
	stateDir := t.TempDir()
	artDir := t.TempDir()
	xdg := t.TempDir()
	fake := newFakeAppiumServer(t, fakeVideo)
	saveTestSession(t, xdg, "bed", "sid-1", fake.URL+"/wd/hub")

	cfg := RecorderConfig{
		Box: "bed", StateDir: stateDir, ArtifactDir: artDir,
		SessionID: "bed.member.cap", Venue: "check-appium-pod",
		TimeLimit: 60, PollInterval: 20 * time.Millisecond,
	}
	done := make(chan struct{})
	ch := make(chan error, 1)
	go func() { ch <- RunSessionRecorder(cfg, done) }()
	waitFor(t, 2*time.Second, func() bool { s, _ := fake.counts(); return s >= 1 })
	// the baked suite rotates the SHARED file to ITS transient session
	t.Setenv("XDG_CACHE_HOME", xdg)
	if err := saveAppiumSession(&AppiumSession{SessionID: "baked-sid", BaseURL: fake.URL + "/wd/hub", CreatedAt: time.Now().UTC(), Image: "bed"}); err != nil {
		t.Fatalf("rotate session file: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	s, stop := fake.counts()
	if s != 1 {
		t.Errorf("start calls = %d, want 1 (no re-bracket onto the rotated id)", s)
	}
	if stop != 0 {
		t.Errorf("stop calls = %d, want 0 (the pinned bracket must NOT be dropped by the rotation)", stop)
	}
	// finalize: the held pinned bracket's stop pulls the video and the row lands
	close(done)
	if err := <-ch; err != nil {
		t.Fatalf("RunSessionRecorder: %v", err)
	}
	if s, stop := fake.counts(); s != 1 || stop != 1 {
		t.Fatalf("start/stop = %d/%d, want 1/1 (the ONE pinned bracket finalizes once)", s, stop)
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, evidenceFile))
	if err != nil {
		t.Fatalf("row.json: %v", err)
	}
	var row evidenceRow
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("decode row.json: %v", err)
	}
	if len(row.Segment) != 1 || len(row.Artifact) != 1 {
		t.Errorf("row segments/artifacts = %d/%d, want 1/1 (the pinned bracket's video)", len(row.Segment), len(row.Artifact))
	}
	if len(row.Segment) == 1 && row.Segment[0]["session"] != "sid-1" {
		t.Errorf("segment session = %v, want sid-1 (the Pinned fixture session, not the rotated id)", row.Segment[0]["session"])
	}
}

// TestRecorderRotatesOnTimeBudget covers the recording-cap rotation: with a tiny
// timeLimit the recorder preemptively closes + reopens the bracket on the SAME
// session (the device silently stops at the cap — the recorder never waits for expiry).
func TestRecorderRotatesOnTimeBudget(t *testing.T) {
	stateDir := t.TempDir()
	artDir := t.TempDir()
	fake := newFakeAppiumServer(t, fakeVideo)
	saveTestSession(t, t.TempDir(), "bed", "sid-1", fake.URL+"/wd/hub")

	cfg := RecorderConfig{
		Box: "bed", StateDir: stateDir, ArtifactDir: artDir,
		SessionID: "bed.member.cap",
		TimeLimit: 2, PollInterval: 20 * time.Millisecond, // rotate after 80% of 2s
	}
	done := make(chan struct{})
	ch := make(chan error, 1)
	go func() { ch <- RunSessionRecorder(cfg, done) }()
	waitFor(t, 4*time.Second, func() bool { s, _ := fake.counts(); return s >= 2 })
	close(done)
	if err := <-ch; err != nil {
		t.Fatalf("RunSessionRecorder: %v", err)
	}
	s, stop := fake.counts()
	if s != 2 || stop != 2 {
		t.Fatalf("start/stop calls = %d/%d, want 2/2 (rotation on the same session)", s, stop)
	}
	// both segments carry the SAME session
	raw, err := os.ReadFile(filepath.Join(stateDir, evidenceFile))
	if err != nil {
		t.Fatalf("row.json: %v", err)
	}
	var row evidenceRow
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("decode row.json: %v", err)
	}
	if len(row.Segment) != 2 {
		t.Fatalf("segments = %d, want 2", len(row.Segment))
	}
	for _, seg := range row.Segment {
		if seg["session"] != "sid-1" {
			t.Errorf("segment session = %v, want sid-1", seg["session"])
		}
	}
}

// TestRecorderFinalizesWithoutSession covers the honest no-session run: the plan never
// created a WebDriver session (no session file) — the recorder finalizes with an empty
// segment/artifact list (the stop contract — row.json must still land).
func TestRecorderFinalizesWithoutSession(t *testing.T) {
	stateDir := t.TempDir()
	cfg := RecorderConfig{
		Box: "bed", StateDir: stateDir, SessionID: "bed.member.cap",
		PollInterval: 20 * time.Millisecond,
	}
	done := make(chan struct{})
	ch := make(chan error, 1)
	go func() { ch <- RunSessionRecorder(cfg, done) }()
	time.Sleep(100 * time.Millisecond)
	close(done)
	if err := <-ch; err != nil {
		t.Fatalf("RunSessionRecorder: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, evidenceFile))
	if err != nil {
		t.Fatalf("row.json: %v", err)
	}
	var row evidenceRow
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("decode row.json: %v", err)
	}
	if len(row.Segment) != 0 || len(row.Artifact) != 0 {
		t.Errorf("row segments/artifacts = %d/%d, want 0/0 (honest empty)", len(row.Segment), len(row.Artifact))
	}
	marker, err := os.ReadFile(filepath.Join(stateDir, finalMarker))
	if err != nil {
		t.Fatalf("FINAL marker: %v", err)
	}
	if string(marker) != "final segments=0 bytes=0\n" {
		t.Errorf("FINAL = %q", marker)
	}
}

// TestRecorderHoldsBracketAcrossTransientFileGone covers the E-5 R1 2026-09-08
// (run 2026.251.1016) defect: the session file vanishing TRANSIENTLY — other steps'
// churn (the baked suite's session-delete, the runner's phase sweep) deletes the
// file while the WebDriver session itself stays alive server-side — must NOT close
// the open bracket (the premature salvage stop would 404 against the still-live
// session and drop the recording). The finalize (done) then pulls the video from the
// live session and row.json lands segments=1 — the E-5 evidence contract.
func TestRecorderHoldsBracketAcrossTransientFileGone(t *testing.T) {
	stateDir := t.TempDir()
	artDir := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	fake := newFakeAppiumServer(t, fakeVideo)
	sf := filepath.Join(xdg, "charly", "appium", "sessions", "bed.json")
	if err := os.MkdirAll(filepath.Dir(sf), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	saveRaw := func(sid string) {
		t.Helper()
		b, err := json.Marshal(&AppiumSession{
			SessionID: sid, BaseURL: fake.URL + "/wd/hub",
			CreatedAt: time.Now().UTC(), Image: "bed",
		})
		if err != nil {
			t.Fatalf("marshal session: %v", err)
		}
		if err := os.WriteFile(sf, b, 0o600); err != nil {
			t.Fatalf("write session file: %v", err)
		}
	}
	saveRaw("sid-1")

	cfg := RecorderConfig{
		Box: "bed", StateDir: stateDir, ArtifactDir: artDir,
		SessionID: "bed.member.cap", Venue: "check-appium-pod",
		TimeLimit: 60, PollInterval: 20 * time.Millisecond,
	}
	done := make(chan struct{})
	ch := make(chan error, 1)
	go func() { ch <- RunSessionRecorder(cfg, done) }()
	waitFor(t, 2*time.Second, func() bool { s, _ := fake.counts(); return s >= 1 })
	// the session file vanishes TRANSIENTLY (other steps' churn) — the open bracket
	// must SURVIVE: no salvage stop may hit the server.
	if err := os.Remove(sf); err != nil {
		t.Fatalf("remove session file: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, stop := fake.counts(); stop != 0 {
		t.Fatalf("stop calls = %d after file vanish, want 0 (the bracket must NOT be salvaged)", stop)
	}
	// the file returns (the fixture's session stays alive) — the bracket keeps recording
	if s, _ := fake.counts(); s != 1 {
		t.Errorf("start calls = %d, want 1 (no re-bracket while the file is gone)", s)
	}
	// finalize: the still-open bracket's stop pulls the live video and the row lands
	close(done)
	if err := <-ch; err != nil {
		t.Fatalf("RunSessionRecorder: %v", err)
	}
	if s, stop := fake.counts(); s != 1 || stop != 1 {
		t.Fatalf("start/stop = %d/%d, want 1/1 (the held bracket finalizes once)", s, stop)
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, evidenceFile))
	if err != nil {
		t.Fatalf("row.json: %v", err)
	}
	var row evidenceRow
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("decode row.json: %v", err)
	}
	if len(row.Segment) != 1 || len(row.Artifact) != 1 {
		t.Errorf("row segments/artifacts = %d/%d, want 1/1 (the held bracket's video), got %+v", len(row.Segment), len(row.Artifact), row)
	}
}

// TestRunSessionRecorderEmptyStateDir guards the recorder's honest failure when the
// spawn env left no state dir.
func TestRunSessionRecorderEmptyStateDir(t *testing.T) {
	if err := RunSessionRecorder(RecorderConfig{}, make(chan struct{})); err == nil {
		t.Fatal("RunSessionRecorder with empty state dir: want error")
	}
}
