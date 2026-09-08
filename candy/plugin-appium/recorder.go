package appium

// recorder.go — the DETACHED host-side session recorder (plan Cutover E, E-5).
// appium-session start hands THIS binary (in recorder mode, env
// CHARLY_APPIUM_RECORDER=1) to the runner's generic background-session service
// (plugin-check's compiled-in verb:session seam). The recorder owns the DEVICE-side
// screen-recording bracket for the whole session: it polls the persisted Appium
// session file (the plan's session-create step writes it — the SAME host-side
// ~/.cache/charly/appium/sessions/<box>[_<instance>].json the other verbs read) for
// the WebDriver session id + base URL, opens the bracket (POST
// /session/<id>/appium/start_recording_screen), re-brackets when the session is
// recreated mid-run (session-delete + session-create) or when the device-side
// recording cap (timeLimit) approaches, and on SIGTERM/SIGINT finalizes: pulls the
// video (stop_recording_screen → base64 MP4) into the evidence artifact dir, then
// writes the deterministic FINAL marker + the evidence row.json. While it runs, the
// PROVIDER spawns no process, knows no transport, and owns no pidfile.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Recorder-mode env contract between provider.go's spawn env (buildSessionSpawn) and
// cmd/serve's recorder mode (the reader).
const (
	EnvRecorder      = "CHARLY_APPIUM_RECORDER"
	EnvBox           = "CHARLY_APPIUM_BOX"
	EnvInstance      = "CHARLY_APPIUM_INSTANCE"
	EnvStateDir      = "CHARLY_APPIUM_STATE_DIR"
	EnvArtifactDir   = "CHARLY_APPIUM_ARTIFACT_DIR"
	EnvSessionID     = "CHARLY_APPIUM_SESSION_ID"
	EnvVenue         = "CHARLY_APPIUM_VENUE"
	EnvContainerName = "CHARLY_APPIUM_CONTAINER"
	EnvPhase         = "CHARLY_APPIUM_PHASE"
	EnvTimeLimit     = "CHARLY_APPIUM_TIME_LIMIT"
	EnvFps           = "CHARLY_APPIUM_FPS"
	EnvVideoType     = "CHARLY_APPIUM_VIDEO_TYPE"
)

// finalMarker is the deterministic end-of-stream marker the stop path greps for;
// evidenceFile is the recorder's evidence row (the shared #EvidenceRow shape the
// instrument lifecycle reads back + copies blind into evidence.yml).
const (
	finalMarker  = "FINAL"
	evidenceFile = "row.json"
)

// evidenceRow mirrors the shared #EvidenceRow shape (plan §4 A-task-1) — the minimal
// session subset the recorder writes and sessionStop reads back. The runner's
// evidence phase copies the segment's frames/bytes scalars + the artifact list blind
// (no plugin-specific manifest code here).
type evidenceRow struct {
	Instrument string             `json:"instrument"`
	Origin     string             `json:"origin"`
	Verb       string             `json:"verb"`
	Venue      string             `json:"venue,omitempty"`
	Phase      string             `json:"phase,omitempty"`
	Segment    []map[string]any   `json:"segment,omitempty"`
	Artifact   []evidenceArtifact `json:"artifact,omitempty"`
}

// evidenceArtifact is one artifact entry of the evidence row (path + the open kind
// word — "mp4" here).
type evidenceArtifact struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// RecorderConfig is the detached-session recorder's full runtime contract.
type RecorderConfig struct {
	Box         string // session-file key (the plan's session-create writes it)
	Instance    string // session-file key suffix (optional)
	StateDir    string // the run's state dir: row.json + FINAL land here
	ArtifactDir string // the run's evidence-artifact dir; pulled MP4s land here (fallback: state dir)
	SessionID   string // the venue-scoped session id — stamped into the evidence row
	Venue       string // evidence-row provenance
	Phase       string // evidence-row provenance (build|live|update|teardown)
	TimeLimit   int    // device-side recording cap in seconds (default 1800)
	Fps         int    // device-side encoding rate (optional)
	VideoType   string // h264|mpeg4 (optional, server default)

	// SessionFile is the SPAWN-STAMPED session-file path (EnvSessionFile); when set it
	// wins over the re-derived appiumSessionPath — the cross-process env agreement fix
	// (E-5 R1, 2026-09-08).
	SessionFile string

	// ContainerName is the SPAWN-STAMPED container name (CheckEnv.ContainerName — the
	// SAME container the plan's session-create step resolved). At bracket-open time the
	// recorder re-inspects THIS container for the LIVE host forward of the Appium port
	// instead of trusting the persisted session-file BaseURL, which goes stale the
	// moment the pod recycles (E-5 2026-09-08, run 2026.251.1016: the recorder dialed
	// the dead pre-recycle forward for the whole run — zero brackets).
	ContainerName string

	// PollInterval is the session-file poll cadence (tests shrink it; default 500ms).
	PollInterval time.Duration
}

// rotationFraction is the fraction of the recording cap at which the recorder
// preemptively rotates the bracket (stop + start) so a phase longer than the cap
// keeps continuous coverage — the device records at most timeLimit seconds per
// startRecordingScreen call and never signals the expiry.
const rotationFraction = 0.8

// recordingBracket is one open startRecordingScreen bracket: the WebDriver session
// it belongs to and when it opened.
type recordingBracket struct {
	SessionID string
	StartedAt time.Time
	s         *w3cSession // raw W3C HTTP client bound to the session
}

// closedBracket is the outcome of one closed bracket: the pulled MP4's host path +
// the span + byte size, used to assemble the evidence row.
type closedBracket struct {
	SessionID string
	Start     time.Time
	Stop      time.Time
	Bytes     int
	Artifact  string
}

// RunSessionRecorder is the detached-mode engine (cmd/serve, recorder mode): waits
// for the plan's WebDriver session (session file), holds the recording bracket until
// done closes, then finalizes the FINAL marker + row.json. Returns nil when the
// finalize landed (the runner's stop is complete only when row.json is on disk).
func RunSessionRecorder(cfg RecorderConfig, done <-chan struct{}) error {
	if cfg.StateDir == "" {
		return fmt.Errorf("recorder: empty state dir")
	}
	if cfg.TimeLimit <= 0 {
		cfg.TimeLimit = defaultTimeLimit
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return fmt.Errorf("recorder: create state dir: %w", err)
	}
	closed := runBracketLoop(cfg, done)
	return finalizeRecorder(cfg, closed)
}

// runBracketLoop is the poll loop: discover the session file → open/rotate/close
// brackets; returns the closed brackets (the done signal's final close included).
// Failures are logged to stderr and retried on the next tick — a transient server or
// file error must never kill the session (the runner's stop ladder owns the
// process lifetime).
func runBracketLoop(cfg RecorderConfig, done <-chan struct{}) []closedBracket {
	tick := time.NewTicker(cfg.PollInterval)
	defer tick.Stop()
	var current *recordingBracket
	closed := []closedBracket{}
	seq := 0
	for {
		select {
		case <-done:
			if current != nil {
				if b, err := closeBracket(cfg, &seq, current); err == nil {
					closed = append(closed, b)
				} else {
					fmt.Fprintf(os.Stderr, "charly-appium recorder: final bracket: %v\n", err)
				}
			}
			return closed
		case <-tick.C:
			var (
				sess *AppiumSession
				err  error
			)
			if cfg.SessionFile != "" {
				sess, err = loadAppiumSessionPath(cfg.SessionFile)
			} else {
				sess, err = loadAppiumSession(cfg.Box, cfg.Instance)
			}
			if err != nil {
				// transient read (e.g. the file mid-write): retry next tick
				continue
			}
			if sess == nil {
				// The session file vanished. The salvage-close-on-missing behavior
				// (stop_recording_screen immediately) dropped the LAST bracket when the
				// file disappeared TRANSIENTLY — other steps churn the file (the baked
				// suite's session-delete, the runner's phase sweep) while the WebDriver
				// session itself stays alive server-side (the E-5 fixture holds ONE
				// identity at a 3600s idle cap). E-5 R1 2026-09-08, run 2026.251.1016:
				// the fixture session's bracket was salvaged (404) the instant the file
				// vanished, and row.json finalized with segments=0 despite the live
				// session. HOLD the open bracket — the finalize (done) or the next
				// session change closes it against the still-live session.
				continue
			}
			if current == nil || current.SessionID != sess.SessionID {
				// new session (or first): close any stale bracket, open the new one
				if current != nil {
					if b, err := closeBracket(cfg, &seq, current); err == nil {
						closed = append(closed, b)
					} else {
						fmt.Fprintf(os.Stderr, "charly-appium recorder: close bracket: %v\n", err)
					}
					current = nil
				}
				b, err := openBracket(cfg, sess)
				if err != nil {
					fmt.Fprintf(os.Stderr, "charly-appium recorder: start bracket: %v\n", err)
					continue // retry next tick
				}
				current = b
				continue
			}
			// time-budget rotation: the device auto-stops at timeLimit; preempt at
			// 80% so long phases keep continuous coverage.
			if time.Since(current.StartedAt) >= rotationAfter(cfg.TimeLimit) {
				if b, err := closeBracket(cfg, &seq, current); err == nil {
					closed = append(closed, b)
				} else {
					fmt.Fprintf(os.Stderr, "charly-appium recorder: rotate bracket: %v\n", err)
				}
				current = nil
				nb, err := openBracket(cfg, sess)
				if err != nil {
					fmt.Fprintf(os.Stderr, "charly-appium recorder: rotate restart: %v\n", err)
					continue
				}
				current = nb
			}
		}
	}
}

// rotationAfter returns the elapsed time at which the recorder rotates the bracket.
func rotationAfter(timeLimit int) time.Duration {
	if timeLimit <= 0 {
		timeLimit = defaultTimeLimit
	}
	return time.Duration(float64(timeLimit) * rotationFraction * float64(time.Second))
}

// resolveRecorderEndpoint returns the LIVE Appium base URL to dial at bracket-open
// time. The persisted session-file BaseURL goes stale the moment the pod recycles (the
// update phase recreates the container and the host-side 4723 forward moves), so the
// recorder re-resolves the CURRENT endpoint from the container's live forward instead
// of trusting the file — keeping the file's SessionID (the bearer) while refreshing the
// endpoint (E-5 2026-09-08, run 2026.251.1016: the recorder dialed the dead pre-recycle
// forward for the whole run, zero brackets). Mirror of container.go's appiumBaseURL —
// the SAME resolution the plan's session-create used — so each bracket open dials the
// LIVE forward. The container name is the spawn-stamped CheckEnv.ContainerName
// (ENV_STAMP); the venue's podman-exec segment is the fallback when the stamp is
// absent. Without ANY container context (unit harness, container-less plan step) the
// persisted URL is the best available endpoint.
func resolveRecorderEndpoint(cfg RecorderConfig, sess *AppiumSession) (string, error) {
	name := cfg.ContainerName
	if name == "" {
		name = containerNameFromVenue(cfg.Venue)
	}
	if name != "" {
		base, err := appiumBaseURL(&checkEnv{Box: cfg.Box, Instance: cfg.Instance, ContainerName: name}, appiumBasePath)
		if err != nil {
			return "", fmt.Errorf("re-resolve appium endpoint (container %s): %w", name, err)
		}
		return base, nil
	}
	return sess.BaseURL, nil
}

// containerNameFromVenue extracts the live container name from a podman-exec venue
// string ("nested:podman-exec:charly-<box>/local" → "charly-<box>") — the recorder's
// secondary container source when the spawn stamp is absent. Returns "" for a
// non-podman-exec venue or an empty segment (no container context; the persisted
// session-file URL is then the best available endpoint).
func containerNameFromVenue(venue string) string {
	i := strings.LastIndex(venue, "podman-exec:")
	if i < 0 {
		return ""
	}
	rest := venue[i+len("podman-exec:"):]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// openBracket opens the device-side recording bracket on the given WebDriver
// session. timeLimit is ALWAYS sent (Appium's own default is only 180s — a phase
// longer than that silently ends the bracket at the device). The dial target is the
// RE-RESOLVED live endpoint (resolveRecorderEndpoint), never the persisted
// possibly-stale session-file BaseURL.
func openBracket(cfg RecorderConfig, sess *AppiumSession) (*recordingBracket, error) {
	opts := map[string]any{"timeLimit": cfg.TimeLimit}
	if cfg.Fps > 0 {
		opts["fps"] = cfg.Fps
	}
	if cfg.VideoType != "" {
		opts["videoType"] = cfg.VideoType
	}
	base, err := resolveRecorderEndpoint(cfg, sess)
	if err != nil {
		return nil, fmt.Errorf("startRecordingScreen on session %s: %w", sess.SessionID, err)
	}
	s := newW3CSession(base, sess.SessionID)
	if _, err := s.call(http.MethodPost, "/appium/start_recording_screen", opts); err != nil {
		return nil, fmt.Errorf("startRecordingScreen on session %s: %w", sess.SessionID, err)
	}
	return &recordingBracket{SessionID: sess.SessionID, StartedAt: time.Now().UTC(), s: s}, nil
}

// closeBracket pulls the recording (stop_recording_screen → base64 MP4), decodes it,
// and lands it as appium-<n>.mp4 in the artifact dir (fallback: the state dir).
func closeBracket(cfg RecorderConfig, seq *int, b *recordingBracket) (closedBracket, error) {
	resp, err := b.s.call(http.MethodPost, "/appium/stop_recording_screen", nil)
	if err != nil {
		return closedBracket{}, fmt.Errorf("stopRecordingScreen on session %s: %w", b.SessionID, err)
	}
	var b64 string
	if err := json.Unmarshal(resp, &b64); err != nil {
		return closedBracket{}, fmt.Errorf("stopRecordingScreen on session %s: decode value: %w", b.SessionID, err)
	}
	video, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return closedBracket{}, fmt.Errorf("stopRecordingScreen on session %s: decode base64: %w", b.SessionID, err)
	}
	*seq++
	dir := cfg.StateDir
	if cfg.ArtifactDir != "" {
		dir = cfg.ArtifactDir
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return closedBracket{}, fmt.Errorf("recorder: create artifact dir: %w", err)
		}
	}
	name := fmt.Sprintf("appium-%d.mp4", *seq)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, video, 0o644); err != nil {
		return closedBracket{}, fmt.Errorf("recorder: write %s: %w", path, err)
	}
	return closedBracket{SessionID: b.SessionID, Start: b.StartedAt, Stop: time.Now().UTC(), Bytes: len(video), Artifact: path}, nil
}

// finalizeRecorder writes the deterministic end-of-stream marker + the evidence row
// into the state dir. Called once, on the SIGTERM path — the runner's stop is
// complete only when row.json is on disk. A run where the plan never created a
// WebDriver session finalizes honestly: empty segment + artifact lists.
func finalizeRecorder(cfg RecorderConfig, closed []closedBracket) error {
	total := 0
	segments := make([]map[string]any, 0, len(closed))
	artifacts := make([]evidenceArtifact, 0, len(closed))
	for _, b := range closed {
		total += b.Bytes
		segments = append(segments, map[string]any{
			"session": b.SessionID,
			"start":   b.Start.Format(time.RFC3339),
			"stop":    b.Stop.Format(time.RFC3339),
			"bytes":   b.Bytes,
		})
		artifacts = append(artifacts, evidenceArtifact{Path: b.Artifact, Kind: "mp4"})
	}
	marker := fmt.Sprintf("final segments=%d bytes=%d\n", len(closed), total)
	if err := os.WriteFile(filepath.Join(cfg.StateDir, finalMarker), []byte(marker), 0o644); err != nil {
		return fmt.Errorf("recorder: write %s: %w", finalMarker, err)
	}
	row := evidenceRow{
		Instrument: cfg.SessionID,
		Origin:     "session",
		Verb:       "appium",
		Venue:      cfg.Venue,
		Phase:      cfg.Phase,
		Segment:    segments,
		Artifact:   artifacts,
	}
	b, err := json.MarshalIndent(row, "", "  ")
	if err != nil {
		return fmt.Errorf("recorder: marshal evidence row: %w", err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(filepath.Join(cfg.StateDir, evidenceFile), b, 0o644); err != nil {
		return fmt.Errorf("recorder: write %s: %w", evidenceFile, err)
	}
	return nil
}
