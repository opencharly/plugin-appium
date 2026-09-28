# plugin-appium

Android UI automation for OpenCharly — the `appium:` W3C-WebDriver check verb.

The plugin is an out-of-tree Go module: charly fetches this repo at the pinned
tag, go-builds the provider on the host, and serves it **out-of-process** over
go-plugin gRPC via the plugin SDK. That keeps the heavy
`github.com/tebeka/selenium` dependency and its transitive packages out of
charly's core `go.mod`, while `appium:` authoring stays unchanged — the verb
dispatches through the provider registry exactly like a built-in.

## What it provides

| Capability | Surface |
|---|---|
| `verb:appium` | the `appium:` check verb — the full 48-method W3C surface: `status`, `session`, `find`, `click`, `send-keys`, `screenshot`, `gesture-*`, `app-*`, `key-*`, `device-*`, `execute`, `raw` |

The `session` method is a detached device-side screen-recording bracket
(`startRecordingScreen` / `stopRecordingScreen` → MP4) over the runner's generic
background-session service. The R10 consumer is the
`check-android-emulator-pod` bed in `opencharly/charly` (via
`android-emulator-layer`).

## How to use it

Compose the plugin candy in a box or check bed's `candy:` list:

```yaml
- '@github.com/opencharly/plugin-appium/candy/plugin-appium:<tag>'
```

Then author the verb in a plan:

```yaml
- check: the Appium server is ready
  id: appium-status-ready
  appium: status
  stdout:
    - contains: '"ready":true'
  context: [runtime]
```

## Layout

- `candy/plugin-appium/` — the plugin module: `plugin.go`/`provider.go`,
  `session.go`/`session_method.go`, `w3c.go`, `dispatch.go`, `recorder.go`,
  `schema/appium.cue` (the self-contained `#AppiumInput`),
  `params/cue_types_gen.go`, `cmd/serve/main.go`.
- `candy/plugin-appium/charly.yml` — the `plugin-appium:` candy entity.
- `charly.yml` — the root project manifest (`discover: candy`).
- `.github/workflows/tag-on-merge.yml` — CalVer tag + `CHANGELOG/` on merge.

## Related

- Owning skill: `/charly-check:appium` — the `appium:` check verb reference (the
  candy carries no `skill:` entity of its own; the gap is tracked in
  [opencharly/opencharly#291](https://github.com/opencharly/opencharly/issues/291)).
- `/charly-check:adb` — the sibling Android Debug Bridge verb.
- `/charly-internals:plugin` — the plugin/provider model.
- [`opencharly/charly`](https://github.com/opencharly/charly) — the charly CLI.
