package appium

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencharly/plugin-appium/candy/plugin-appium/params"
)

// endpoint_reresolve_test.go — the E-5 endpoint re-resolution contract (2026-09-08,
// run 2026.251.1016): the recorder must NOT dial the persisted session-file BaseURL
// (it goes stale the moment the pod recycles and the host-side 4723 forward moves) —
// at bracket-open time it RE-RESOLVES the live endpoint from the container's current
// forward (<engine> inspect, the same resolution the plan's session-create used) and
// dials THAT, keeping the file's SessionID as the bearer. A script-based fake engine
// (CHARLY_PLUGIN_ENGINE → a helper script printing a test-controlled inspect doc)
// models the container; rewriting the inspect file mid-run models the pod recycle.

// fakeEngine installs a script-based fake container-engine CLI: "inspect" prints the
// JSON read from $CHARLY_FAKE_INSPECT_FILE — a file the test writes and REWRITES at
// will (a port change mid-run models the pod recycle). Returns the inspect-file path.
func fakeEngine(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	inspectFile := filepath.Join(dir, "inspect.json")
	script := filepath.Join(dir, "fake-engine")
	body := "#!/bin/sh\nif [ \"$1\" = \"inspect\" ]; then cat \"$CHARLY_FAKE_INSPECT_FILE\"; fi\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake engine: %v", err)
	}
	t.Setenv("CHARLY_PLUGIN_ENGINE", script)
	t.Setenv("CHARLY_FAKE_INSPECT_FILE", inspectFile)
	return inspectFile
}

// writeFakeInspect writes an inspect document publishing the Appium container port
// (4723) on the given HOST port — the live forward the recorder must re-resolve.
func writeFakeInspect(t *testing.T, inspectFile string, hostPort int) {
	t.Helper()
	doc := fmt.Sprintf("[{\"HostConfig\":{\"NetworkMode\":\"bridge\"},\"NetworkSettings\":{\"Ports\":{\"4723/tcp\":[{\"HostIp\":\"127.0.0.1\",\"HostPort\":\"%d\"}]}}}]", hostPort)
	if err := os.WriteFile(inspectFile, []byte(doc), 0o600); err != nil {
		t.Fatalf("write fake inspect: %v", err)
	}
}

// deadPort returns a host port with nothing listening on it — the session file's
// STALE base-URL target (a closed listener's port is the guaranteed-refused one).
func deadPort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 1
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return p
}

// TestRecorderReresolvesEndpointAtBracketOpen is the core E-5 contract: the session
// file's base_url points at a DEAD port (a stale forward from a recycled pod) while
// the container's CURRENT forward is the live fake server — the recorder must dial
// the LIVE forward, not the file. Proof: the fake server's start counter increments
// despite the stale file (the stale dial could never reach it), and the stop pulls a
// real MP4 into the artifact row.
func TestRecorderReresolvesEndpointAtBracketOpen(t *testing.T) {
	stateDir := t.TempDir()
	artDir := t.TempDir()
	fake := newFakeAppiumServer(t, fakeVideo)
	inspectFile := fakeEngine(t)
	writeFakeInspect(t, inspectFile, mustServerPort(t, fake))
	// The persisted session file carries a STALE base URL (dead port — the E-5
	// 1016 defect's exact shape: the file kept the pre-recycle forward).
	saveTestSession(t, t.TempDir(), "check-android-emulator-pod", "efea9754", fmt.Sprintf("http://127.0.0.1:%d/wd/hub", deadPort()))

	cfg := RecorderConfig{
		Box: "check-android-emulator-pod", StateDir: stateDir, ArtifactDir: artDir,
		SessionID: "bed.member.cap", Venue: "nested:podman-exec:charly-check-android-emulator-pod/local",
		ContainerName: "charly-check-android-emulator-pod",
		TimeLimit:     60, PollInterval: 20 * time.Millisecond,
	}
	done := make(chan struct{})
	ch := make(chan error, 1)
	go func() { ch <- RunSessionRecorder(cfg, done) }()
	waitFor(t, 3*time.Second, func() bool { s, _ := fake.counts(); return s >= 1 })
	close(done)
	if err := <-ch; err != nil {
		t.Fatalf("RunSessionRecorder: %v", err)
	}

	s, stop := fake.counts()
	if s != 1 || stop != 1 {
		t.Errorf("fake-server start/stop = %d/%d, want 1/1 — the recorder must dial the LIVE forward (the stale file port refused would leave 0/0)", s, stop)
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, evidenceFile))
	if err != nil {
		t.Fatalf("row.json: %v", err)
	}
	var row evidenceRow
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("decode row.json: %v", err)
	}
	if len(row.Artifact) != 1 || row.Artifact[0].Kind != "mp4" {
		t.Errorf("row artifacts = %+v, want one mp4 (the pulled live video)", row.Artifact)
	}
	if len(row.Segment) != 1 {
		t.Errorf("row segments = %d, want 1", len(row.Segment))
	}
}

// mustServerPort returns the fake Appium server's listening port.
func mustServerPort(t *testing.T, fake *fakeAppiumServer) int {
	t.Helper()
	_, port, err := net.SplitHostPort(fake.Listener.Addr().String())
	if err != nil {
		t.Fatalf("server addr: %v", err)
	}
	var n int
	if _, err := fmt.Sscanf(port, "%d", &n); err != nil {
		t.Fatalf("server port %q: %v", port, err)
	}
	return n
}

// TestRecorderReresolvesEndpointAfterRecycle models the update phase exactly: the
// bracket opens against forward A, the pod recycles (the container keeps its name,
// the host forward MOVES to B), and the recorder's NEXT bracket open must dial B —
// every open re-inspects, nothing is cached from the first resolution. The rotation
// path (tiny timeLimit) forces the second open on the SAME session without touching
// the session file — the stale base_url in the file never wins either bracket.
func TestRecorderReresolvesEndpointAfterRecycle(t *testing.T) {
	stateDir := t.TempDir()
	artDir := t.TempDir()
	fakeA := newFakeAppiumServer(t, fakeVideo)
	fakeB := newFakeAppiumServer(t, fakeVideo)
	inspectFile := fakeEngine(t)
	writeFakeInspect(t, inspectFile, mustServerPort(t, fakeA))
	stale := fmt.Sprintf("http://127.0.0.1:%d/wd/hub", deadPort())
	saveTestSession(t, t.TempDir(), "check-android-emulator-pod", "sid-recycle", stale)

	cfg := RecorderConfig{
		Box: "check-android-emulator-pod", StateDir: stateDir, ArtifactDir: artDir,
		SessionID: "bed.member.cap", ContainerName: "charly-check-android-emulator-pod",
		TimeLimit: 2, PollInterval: 20 * time.Millisecond, // rotate after 80% of 2s
	}
	done := make(chan struct{})
	ch := make(chan error, 1)
	go func() { ch <- RunSessionRecorder(cfg, done) }()
	waitFor(t, 3*time.Second, func() bool { a, _ := fakeA.counts(); return a >= 1 })
	// The pod recycles: same container name, the forward moves to fakeB.
	writeFakeInspect(t, inspectFile, mustServerPort(t, fakeB))
	waitFor(t, 5*time.Second, func() bool { b, _ := fakeB.counts(); return b >= 1 })
	close(done)
	if err := <-ch; err != nil {
		t.Fatalf("RunSessionRecorder: %v", err)
	}

	aStarts, aStops := fakeA.counts()
	bStarts, bStops := fakeB.counts()
	if aStarts != 1 || aStops < 1 {
		t.Errorf("forward A start/stop = %d/%d, want 1/>=1 (first bracket + rotation close)", aStarts, aStops)
	}
	if bStarts != 1 || bStops != 1 {
		t.Errorf("forward B start/stop = %d/%d, want 1/1 (re-resolved live forward + final pull)", bStarts, bStops)
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, evidenceFile))
	if err != nil {
		t.Fatalf("row.json: %v", err)
	}
	var row evidenceRow
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("decode row.json: %v", err)
	}
	if len(row.Segment) != 2 || len(row.Artifact) != 2 {
		t.Errorf("row segments/artifacts = %d/%d, want 2/2 (a bracket on each live forward)", len(row.Segment), len(row.Artifact))
	}
}

// TestRecorderVenueFallbackContainer covers the recorder's SECONDARY container source:
// when the spawn stamp is absent, the podman-exec venue segment names the container to
// inspect ("nested:podman-exec:charly-check-android-emulator-pod/local" → the live
// forward), so a session spawned with a venue but no container stamp still dials the
// LIVE endpoint instead of the stale file.
func TestRecorderVenueFallbackContainer(t *testing.T) {
	stateDir := t.TempDir()
	artDir := t.TempDir()
	fake := newFakeAppiumServer(t, fakeVideo)
	inspectFile := fakeEngine(t)
	writeFakeInspect(t, inspectFile, mustServerPort(t, fake))
	saveTestSession(t, t.TempDir(), "check-android-emulator-pod", "sid-venue", fmt.Sprintf("http://127.0.0.1:%d/wd/hub", deadPort()))

	cfg := RecorderConfig{
		Box: "check-android-emulator-pod", StateDir: stateDir, ArtifactDir: artDir,
		SessionID: "bed.member.cap", Venue: "nested:podman-exec:charly-check-android-emulator-pod/local",
		// ContainerName deliberately NOT stamped — the venue segment must resolve it.
		TimeLimit: 60, PollInterval: 20 * time.Millisecond,
	}
	done := make(chan struct{})
	ch := make(chan error, 1)
	go func() { ch <- RunSessionRecorder(cfg, done) }()
	waitFor(t, 3*time.Second, func() bool { s, _ := fake.counts(); return s >= 1 })
	close(done)
	if err := <-ch; err != nil {
		t.Fatalf("RunSessionRecorder: %v", err)
	}
	if s, _ := fake.counts(); s != 1 {
		t.Errorf("fake-server starts = %d, want 1 (venue-derived container's live forward)", s)
	}
}

// TestContainerNameFromVenue guards the venue parse itself: podman-exec venues name
// the container; host/local or garbage venues carry none.
func TestContainerNameFromVenue(t *testing.T) {
	cases := []struct {
		venue string
		want  string
	}{
		{"nested:podman-exec:charly-check-android-emulator-pod/local", "charly-check-android-emulator-pod"},
		{"podman-exec:charly-bed", "charly-bed"},
		{"charly-check-android-emulator-pod", ""},
		{"local", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := containerNameFromVenue(c.venue); got != c.want {
			t.Errorf("containerNameFromVenue(%q) = %q, want %q", c.venue, got, c.want)
		}
	}
}

// TestBuildSessionSpawnStampsContainerName is the spawn-side half of the contract: the
// session-start provider stamps CheckEnv.ContainerName into the recorder env — the
// SAME container the plan's session-create steps resolve — and omits it when the check
// env carries none (no container context: the recorder falls back to the file/venue).
func TestBuildSessionSpawnStampsContainerName(t *testing.T) {
	env := &checkEnv{Box: "check-android-emulator-pod", ContainerName: "charly-check-android-emulator-pod"}
	e := buildSessionSpawn(&params.AppiumInput{}, env, "/x", "nested:podman-exec:charly-check-android-emulator-pod/local", "logdir")
	if got := e.Env[EnvContainerName]; got != "charly-check-android-emulator-pod" {
		t.Errorf("stamped container = %q, want %q (the container the plan's session-create resolved)", got, "charly-check-android-emulator-pod")
	}
	e2 := buildSessionSpawn(&params.AppiumInput{}, &checkEnv{Box: "b"}, "/x", "", "")
	if _, has := e2.Env[EnvContainerName]; has {
		t.Error("empty CheckEnv.ContainerName must omit CHARLY_APPIUM_CONTAINER")
	}
}
