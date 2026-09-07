// Command serve is the OUT-OF-PROCESS entrypoint for the appium verb plugin: a thin
// shim serving the importable provider over go-plugin gRPC via sdk.Serve. The SAME
// NewProvider()/NewMeta() compile INTO charly in-process when listed in
// compiled_plugins; this binary is host-built + connected only when they are NOT —
// placement is invisible above the registry.
//
// HIDDEN RECORDER MODE (plan Cutover E, E-5): with CHARLY_APPIUM_RECORDER=1 the SAME
// binary skips serving and becomes the DETACHED host-side session recorder — the
// runner's generic background-session service spawns it for an appium: session start.
// It polls the persisted Appium session file for the plan's WebDriver session
// (box+instance from env), holds the device-side recording bracket
// (startRecordingScreen), and on SIGTERM/SIGINT pulls the video
// (stopRecordingScreen → MP4 into the evidence artifact dir) and finalizes (FINAL
// marker + evidence row.json) before exiting 0: the runner's stop is complete only
// when row.json is on disk.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	appium "github.com/opencharly/plugin-appium/candy/plugin-appium"
	"github.com/opencharly/sdk"
)

func main() {
	if os.Getenv(appium.EnvRecorder) == "1" {
		os.Exit(recorderMain())
	}
	sdk.Serve(appium.NewProvider(), appium.NewMeta())
}

// recorderMain is the detached recorder process entrypoint (see the package doc). It
// returns the process exit code.
func recorderMain() int {
	box := os.Getenv(appium.EnvBox)
	stateDir := os.Getenv(appium.EnvStateDir)
	sessionID := os.Getenv(appium.EnvSessionID)
	if box == "" || stateDir == "" || sessionID == "" {
		fmt.Fprintf(os.Stderr, "charly-appium recorder: missing env (box=%q state_dir=%q session_id=%q)\n", box, stateDir, sessionID)
		return 2
	}
	timeLimit, _ := strconv.Atoi(os.Getenv(appium.EnvTimeLimit))
	fps, _ := strconv.Atoi(os.Getenv(appium.EnvFps))
	cfg := appium.RecorderConfig{
		Box:         box,
		Instance:    os.Getenv(appium.EnvInstance),
		StateDir:    stateDir,
		ArtifactDir: os.Getenv(appium.EnvArtifactDir),
		SessionID:   sessionID,
		Venue:       os.Getenv(appium.EnvVenue),
		Phase:       os.Getenv(appium.EnvPhase),
		TimeLimit:   timeLimit,
		Fps:         fps,
		VideoType:   os.Getenv(appium.EnvVideoType),
	}

	done := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		close(done) // deterministic finalize: FINAL marker + row.json
	}()

	if err := appium.RunSessionRecorder(cfg, done); err != nil {
		fmt.Fprintf(os.Stderr, "charly-appium recorder: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "charly-appium recorder: finalized session %s state_dir=%s\n", sessionID, stateDir)
	return 0
}
