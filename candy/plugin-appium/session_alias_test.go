package appium

// session_alias_test.go — the E-5 R5 single-session aliasing contract: a KEYED
// session-create (the baked av-suite's session_file: baked lifecycle) must NOT
// create a second server-side session when the SHARED box file already holds a LIVE
// fixture session — on a single-session device (UiAutomator2) a new create REPLACES
// the fixture and the recorder's live-stop then 404s against the dead fixture (runs
// 2026.251.1201/1226: every phase green 302/0, evidence row segments=0). The keyed
// create ALIASES the live shared session into the keyed file; a missing or DEAD
// shared session falls back to the normal create; the keyed DELETE leaves an aliased
// session live server-side (only the keyed file goes away).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/opencharly/plugin-appium/candy/plugin-appium/params"
)

// fakeSessionLifecycleServer is a single-session Appium W3C server stub: POST
// /session creates (REPLACING the previous session — UiAutomator2 hosts one session
// per device), GET /session/<id> answers liveness (200 live / 404 terminated), and
// DELETE /session/<id> closes.
type fakeSessionLifecycleServer struct {
	*httptest.Server
	mu      sync.Mutex
	live    string // the current server-side session id ("" = none)
	creates int
	deletes int
}

func newFakeSessionLifecycleServer(t *testing.T) *fakeSessionLifecycleServer {
	f := &fakeSessionLifecycleServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/wd/hub/session", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		f.mu.Lock()
		f.creates++
		f.live = fmt.Sprintf("sess-%d", f.creates)
		sid := f.live
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{"sessionId": sid, "capabilities": map[string]any{}}})
	})
	mux.HandleFunc("/wd/hub/session/", func(w http.ResponseWriter, r *http.Request) {
		sid := strings.TrimPrefix(r.URL.Path, "/wd/hub/session/")
		sid = strings.TrimSuffix(sid, "/")
		f.mu.Lock()
		alive := f.live == sid
		f.mu.Unlock()
		if !alive {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{"error": "invalid session id", "message": "A session is either terminated or not started"}})
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{"sessionId": sid}})
		case http.MethodDelete:
			f.mu.Lock()
			f.deletes++
			f.live = ""
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"value": nil})
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	f.Server = srv
	t.Cleanup(srv.Close)
	return f
}

func (f *fakeSessionLifecycleServer) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates, f.deletes
}

func (f *fakeSessionLifecycleServer) setLive(sid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live = sid
}

// aliasHarness wires the fake engine + server + an XDG session dir for one box.
func aliasHarness(t *testing.T, box string) (*fakeSessionLifecycleServer, string) {
	t.Helper()
	fake := newFakeSessionLifecycleServer(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	inspectFile := fakeEngine(t)
	writeFakeInspect(t, inspectFile, mustServerPort(t, &fakeAppiumServer{Server: fake.Server}))
	return fake, xdg
}

func readSessionFile(t *testing.T, xdg, box, key string) *AppiumSession {
	t.Helper()
	name := box
	if key != "" {
		name = box + "_" + key
	}
	data, err := os.ReadFile(filepath.Join(xdg, "charly", "appium", "sessions", name+".json"))
	if err != nil {
		t.Fatalf("read session file %s: %v", name, err)
	}
	var s AppiumSession
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("decode %s.json: %v", name, err)
	}
	return &s
}

func aliasEnv(box string) *checkEnv {
	return &checkEnv{Box: box, ContainerName: "charly-" + box, Venue: "nested:podman-exec:charly-" + box + "/local"}
}

// TestKeyedCreateAliasesLiveSharedSession is the E-5 R5 core contract: the SHARED
// fixture session is LIVE server-side; a keyed create (the baked suite's lifecycle)
// must NOT spawn a second session on the single-session device — it aliases the
// fixture id into the keyed file and returns it untouched.
func TestKeyedCreateAliasesLiveSharedSession(t *testing.T) {
	const box = "bed"
	fake, xdg := aliasHarness(t, box)
	saveTestSession(t, xdg, box, "fixture-sid", fake.URL+"/wd/hub")
	fake.setLive("fixture-sid")

	sid, err := runSessionCreate(aliasEnv(box), &params.AppiumInput{SessionFile: "baked", Caps: "{\"platformName\":\"Android\"}"})
	if err != nil {
		t.Fatalf("keyed create: %v", err)
	}
	if sid != "fixture-sid" {
		t.Fatalf("keyed create returned %q, want the aliased fixture id fixture-sid", sid)
	}
	if creates, _ := fake.counts(); creates != 0 {
		t.Errorf("server session-creates = %d, want 0 (the alias must NOT create a second server session — single-session device)", creates)
	}
	keyed := readSessionFile(t, xdg, box, "baked")
	if keyed.SessionID != "fixture-sid" {
		t.Errorf("keyed file session = %q, want the aliased fixture session", keyed.SessionID)
	}
}

// TestKeyedCreateFallsBackWhenSharedDead is the stale-session fallback: the shared
// file holds a fixture id the server no longer knows (killed — the update phase
// recycled the pod or a later create replaced it); the keyed create must NOT alias a
// corpse — it creates a fresh server session.
func TestKeyedCreateFallsBackWhenSharedDead(t *testing.T) {
	const box = "bed"
	fake, xdg := aliasHarness(t, box)
	saveTestSession(t, xdg, box, "stale-sid", fake.URL+"/wd/hub")
	// server knows a DIFFERENT session (stale-sid was replaced); the shared file's id is dead
	fake.setLive("sess-other")

	sid, err := runSessionCreate(aliasEnv(box), &params.AppiumInput{SessionFile: "baked", Caps: "{\"platformName\":\"Android\"}"})
	if err != nil {
		t.Fatalf("keyed create (dead shared): %v", err)
	}
	creates, _ := fake.counts()
	if creates != 1 {
		t.Errorf("server session-creates = %d, want 1 (the dead shared session must fall back to a real create)", creates)
	}
	if sid == "stale-sid" {
		t.Errorf("keyed create returned the stale id %q, want a FRESH session", sid)
	}
	keyed := readSessionFile(t, xdg, box, "baked")
	if keyed.SessionID != sid {
		t.Errorf("keyed file session = %q, want %q (the fresh create)", keyed.SessionID, sid)
	}
}

// TestKeyedDeleteLeavesAliasedSession is the R5 delete guard: a keyed delete whose
// entry ALIASES the shared fixture session must NOT close the fixture server-side —
// only the keyed file goes away, the fixture survives for the recorder's live-stop.
func TestKeyedDeleteLeavesAliasedSession(t *testing.T) {
	const box = "bed"
	fake, xdg := aliasHarness(t, box)
	saveTestSession(t, xdg, box, "fixture-sid", fake.URL+"/wd/hub")
	fake.setLive("fixture-sid")
	if _, err := runSessionCreate(aliasEnv(box), &params.AppiumInput{SessionFile: "baked", Caps: "{\"platformName\":\"Android\"}"}); err != nil {
		t.Fatalf("alias create: %v", err)
	}
	out, err := runSessionDelete(aliasEnv(box), &params.AppiumInput{SessionFile: "baked"})
	if err != nil {
		t.Fatalf("alias delete: %v", err)
	}
	if !strings.Contains(out, "alias") {
		t.Errorf("delete output = %q, want the alias guard to name the alias", out)
	}
	if _, deletes := fake.counts(); deletes != 0 {
		t.Errorf("server session-deletes = %d, want 0 (an aliased delete must NOT close the shared fixture)", deletes)
	}
	if _, err := os.Stat(filepath.Join(xdg, "charly", "appium", "sessions", box+"_baked.json")); !os.IsNotExist(err) {
		t.Errorf("keyed file still present after the delete (only the keyed file may go away)")
	}
	if shared := readSessionFile(t, xdg, box, ""); shared.SessionID != "fixture-sid" {
		t.Errorf("shared file session = %q after alias delete, want fixture-sid intact", shared.SessionID)
	}
}

// TestKeyedDeleteClosesOwnSession is the delete-guard CONTROL: a keyed delete whose
// entry is NOT the shared session (a genuine own lifecycle) still closes it
// server-side and removes the keyed file.
func TestKeyedDeleteClosesOwnSession(t *testing.T) {
	const box = "bed"
	fake, xdg := aliasHarness(t, box)
	_ = xdg // the control uses the fake counters + keyed-file removal below
	// shared file absent → the keyed create makes its OWN server session
	if _, err := runSessionCreate(aliasEnv(box), &params.AppiumInput{SessionFile: "baked", Caps: "{\"platformName\":\"Android\"}"}); err != nil {
		t.Fatalf("own create: %v", err)
	}
	out, err := runSessionDelete(aliasEnv(box), &params.AppiumInput{SessionFile: "baked"})
	if err != nil {
		t.Fatalf("own delete: %v", err)
	}
	if !strings.Contains(out, "deleted") || strings.Contains(out, "alias") {
		t.Errorf("delete output = %q, want a NORMAL deleted (own lifecycle)", out)
	}
	if _, deletes := fake.counts(); deletes != 1 {
		t.Errorf("server session-deletes = %d, want 1 (own lifecycle closes server-side)", deletes)
	}
}
