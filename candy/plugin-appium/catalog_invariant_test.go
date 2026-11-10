package appium

// catalog_invariant_test.go pins the invariant the jetkvm, cua, punktfunk, gh, omarchy,
// herdr, spice and desktop-kind siblings keep and this plugin needs for the same reason:
// every word the SCHEMA advertises must be reachable, and every word the plugin routes must
// be in the schema. Without it a word can sit in the catalog as a phantom (advertised but
// unimplemented): the schema validates the authored `appium: …` step, and the step then
// fails only at dispatch with an unknown-method error, so the catalog and the skill that
// documents it mislead the agent that dispatches on it.
//
// THIS repo routes `method` in FOUR places, and the invariant has to read all of them or it
// is wrong in the most dangerous direction:
//
//   - provider.go special-cases `session` BEFORE dispatch (the detached recorder owns the
//     device-side bracket, so dispatch is never reached);
//   - dispatch.go carries a first switch (lifecycle + locators + the `source`/`back` pair),
//     then a MAP-driven gesture arm (`gesturePastTense[method]`), then a second switch
//     (app / key / device / execute / raw).
//
// A test that read only the switches would report `session` as a phantom and every gesture
// word as one; a test that read only the special case and the maps would report the rest.
//
// It also pins three Go-side word sets the census cannot see:
//
//   - `requiredModifiers` is keyed by method AND valued by input field name; a typo in
//     either half does not fail to COMPILE, it silently disables (or permanently fails) the
//     required-input check for the method it was meant to guard;
//   - `gestureMobileName` and `gesturePastTense` are two maps over the SAME gesture words,
//     and the dispatch arm is gated on the second while the mobile: command name is read
//     from the first — a word present in one and not the other dispatches `mobile: ` with
//     an empty command name;
//   - session_method.go's `switch in.Action` IS `#AppiumInput.action`'s router, so the
//     `_action` table and that switch must agree in both directions.

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"cuelang.org/go/cue"
)

var (
	dispatchSwitch = regexp.MustCompile(`(?s)func dispatch\(.*?\n\}`)
	runSessionFunc = regexp.MustCompile(`(?s)func runSession\(.*?\n\}`)
	caseArm        = regexp.MustCompile(`(?m)^[ \t]*case[ \t]+((?:"(?:[^"\\]|\\.)*"[ \t]*,?[ \t]*)+):`)
	quotedWord     = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	// provider.go routes one method before dispatch, as a plain comparison.
	preDispatch = regexp.MustCompile(`method == "([a-z][a-z0-9-]*)"`)
	// the two gesture maps and the required-modifier table are Go map literals.
	gestureNameMap   = regexp.MustCompile(`(?s)var gestureMobileName = map\[string\]string\{(.*?)\n\}`)
	gesturePastMap   = regexp.MustCompile(`(?s)var gesturePastTense = map\[string\]string\{(.*?)\n\}`)
	requiredModsMap  = regexp.MustCompile(`(?s)var requiredModifiers = map\[string\]\[\]string\{(.*?)\n\}`)
	mapWordKey       = regexp.MustCompile(`"([a-z][a-z0-9-]*)":`)
	requiredModValue = regexp.MustCompile(`"(?:[a-z][a-z0-9-]*)"`)
)

func readSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// schemaMethods returns the sorted method names the SERVED #AppiumInput.method enum accepts.
// It reads the schema through the CUE API rather than its text: the enum is now §10.1 S3,
// and a text parse of the union would find ZERO methods and leave every invariant here
// passing vacuously.
func schemaMethods(t *testing.T) []string {
	t.Helper()
	return fieldWordList(t, servedSchema(t), "AppiumInput", "method")
}

// schemaActions returns the sorted words the SERVED #AppiumInput.action enum accepts.
func schemaActions(t *testing.T) []string {
	t.Helper()
	return fieldWordList(t, servedSchema(t), "AppiumInput", "action")
}

// mapBlock returns the body of a Go map literal, failing when the anchor moved instead of
// reading an empty block and reporting a phantom for every word.
func mapBlock(t *testing.T, src, name string, re *regexp.Regexp) string {
	t.Helper()
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("could not locate the %s map literal — if it moved, this test must move with it, not silently pass", name)
	}
	return m[1]
}

// mapKeys returns the sorted string keys of a Go map literal body.
func mapKeys(body string) []string {
	seen := map[string]bool{}
	for _, m := range mapWordKey.FindAllStringSubmatch(body, -1) {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for w := range seen {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

// sortedKeys returns a map's keys sorted, for a failure message that names what the reader
// actually saw instead of only its size.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// routedMethods returns every method word the plugin routes: the two dispatch switches'
// arms, the map-driven gesture words, and provider.go's pre-dispatch comparison.
func routedMethods(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	disp := readSource(t, "dispatch.go")
	fn := dispatchSwitch.FindString(disp)
	if fn == "" {
		t.Fatal("could not locate dispatch in dispatch.go")
	}
	for _, arm := range caseArm.FindAllStringSubmatch(fn, -1) {
		for _, q := range quotedWord.FindAllStringSubmatch(arm[1], -1) {
			seen[q[1]] = true
		}
	}
	gestures := mapKeys(mapBlock(t, disp, "gesturePastTense", gesturePastMap))
	if len(gestures) == 0 {
		t.Fatal("the gesturePastTense map has no keys — the gesture routing would be invisible here")
	}
	for _, g := range gestures {
		seen[g] = true
	}
	prov := readSource(t, "provider.go")
	pre := preDispatch.FindAllStringSubmatch(prov, -1)
	if len(pre) == 0 {
		t.Fatal(`could not locate the pre-dispatch ` + "`method == \"…\"`" + ` comparison in provider.go — if it moved, this test must move with it, not silently pass`)
	}
	for _, m := range pre {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for w := range seen {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

// TestCatalogHasNoPhantomMethods asserts every schema method is routed — no word is
// advertised that the plugin cannot act on.
func TestCatalogHasNoPhantomMethods(t *testing.T) {
	reachable := map[string]bool{}
	for _, m := range routedMethods(t) {
		reachable[m] = true
	}
	if len(reachable) == 0 {
		t.Fatal("the dispatch surface is empty — the invariant would pass over nothing")
	}
	for _, m := range schemaMethods(t) {
		if !reachable[m] {
			t.Errorf("schema method %q is a PHANTOM: neither provider.go's pre-dispatch comparison, dispatch's switches nor the gesture map routes it — add an implementation or remove it from the table", m)
		}
	}
}

// TestDispatchHasNoPhantomArms is the other direction: an arm for a word the schema no
// longer accepts is dead code, and it hides a rename that only half landed.
func TestDispatchHasNoPhantomArms(t *testing.T) {
	advertised := map[string]bool{}
	for _, m := range schemaMethods(t) {
		advertised[m] = true
	}
	for _, m := range routedMethods(t) {
		if !advertised[m] {
			t.Errorf("the plugin routes %q, which the served #AppiumInput.method enum does NOT accept — the arm is unreachable", m)
		}
	}
}

// TestEverySchemaMethodIsRouted is the two-way set equality in one assertion.
func TestEverySchemaMethodIsRouted(t *testing.T) {
	schema := schemaMethods(t)
	routed := routedMethods(t)
	if len(schema) != len(routed) {
		t.Fatalf("served enum has %d methods but the plugin routes %d — the catalog and the dispatch have drifted\n schema=%v\n routed=%v",
			len(schema), len(routed), schema, routed)
	}
	for i := range schema {
		if schema[i] != routed[i] {
			t.Errorf("served method %q has no matching routed arm (routed=%v)", schema[i], routed)
			break
		}
	}
}

// TestRequiredModifierKeysAreSchemaMethods pins the Go-side map whose keys ARE method
// words. A key that names no method is not a harmless typo: the check it carries never runs
// and the method it was meant to guard dispatches without its required input.
func TestRequiredModifierKeysAreSchemaMethods(t *testing.T) {
	advertised := map[string]bool{}
	for _, m := range schemaMethods(t) {
		advertised[m] = true
	}
	keys := mapKeys(mapBlock(t, readSource(t, "dispatch.go"), "requiredModifiers", requiredModsMap))
	if len(keys) == 0 {
		t.Fatal("requiredModifiers has no keys — the check would pass over nothing")
	}
	for _, k := range keys {
		if !advertised[k] {
			t.Errorf("requiredModifiers is keyed by %q, which the served #AppiumInput.method enum does NOT accept — the required-input check for that method never runs", k)
		}
	}
}

// TestRequiredModifierValuesAreInputFields pins the map's VALUE half: each required key
// names an input field the provider reads, and a name no field carries can never be
// satisfied — the method would be permanently rejected at dispatch, with the missing-field
// message naming a key the author cannot write.
func TestRequiredModifierValuesAreInputFields(t *testing.T) {
	input := servedSchema(t).LookupPath(cue.ParsePath("#AppiumInput"))
	if err := input.Err(); err != nil {
		t.Fatalf("#AppiumInput does not resolve in the served schema: %v", err)
	}
	fields := map[string]bool{}
	it, err := input.Fields(cue.Optional(true))
	if err != nil {
		t.Fatalf("#AppiumInput has no fields: %v", err)
	}
	for it.Next() {
		// An OPTIONAL field's selector string carries the constraint marker
		// (`caps?`) — `cue.Optional(true)` is what makes those fields visible at
		// all, and the author writes the bare name, so the marker is stripped.
		fields[strings.TrimSuffix(it.Selector().String(), "?")] = true
	}
	// CALIBRATION, and the reason this check is not an uncalibrated absence
	// claim: it first asserts the reader sees fields the def MUST carry, so a
	// formatting regression in the selector string fails HERE instead of
	// reporting every required modifier as an unknown field (measured: without
	// the trim, all 21 values were reported missing).
	if !fields["method"] || !fields["caps"] {
		t.Fatalf("#AppiumInput's field reader does not see its own `method`/`caps` fields (read %d: %v) — the reader is broken, not the map", len(fields), sortedKeys(fields))
	}
	if len(fields) == 0 {
		t.Fatal("#AppiumInput resolves to no fields — this check would pass over nothing")
	}
	body := mapBlock(t, readSource(t, "dispatch.go"), "requiredModifiers", requiredModsMap)
	checked := 0
	for _, line := range regexp.MustCompile(`(?m)^\t"[a-z][a-z0-9-]*":\s*\{(.*?)\},`).FindAllStringSubmatch(body, -1) {
		for _, q := range requiredModValue.FindAllString(line[1], -1) {
			checked++
			name := q[1 : len(q)-1]
			if !fields[name] {
				t.Errorf("requiredModifiers demands the input field %q, which #AppiumInput does not carry — the method can never satisfy the check", name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("requiredModifiers carries no values — this check would pass over nothing")
	}
}

// TestGestureMapsAgree pins the pair of maps the gesture arm reads: dispatch gates on
// `gesturePastTense[method]` and then passes `gestureMobileName[method]` as the mobile:
// command. A word in the gate map but not the name map dispatches `mobile: ` with an EMPTY
// command name — a failure that names nothing and looks like a server fault; a word in the
// name map only is dead code that hides a half-landed rename.
func TestGestureMapsAgree(t *testing.T) {
	disp := readSource(t, "dispatch.go")
	names := mapKeys(mapBlock(t, disp, "gestureMobileName", gestureNameMap))
	pasts := mapKeys(mapBlock(t, disp, "gesturePastTense", gesturePastMap))
	if len(names) == 0 || len(pasts) == 0 {
		t.Fatal("a gesture map has no keys — this check would pass over nothing")
	}
	nameSet := map[string]bool{}
	for _, n := range names {
		nameSet[n] = true
	}
	pastSet := map[string]bool{}
	for _, p := range pasts {
		pastSet[p] = true
	}
	for _, n := range names {
		if !pastSet[n] {
			t.Errorf("gestureMobileName carries %q but gesturePastTense does not — the dispatch gate never lets it through, so the mobile: command name is dead code", n)
		}
	}
	for _, p := range pasts {
		if !nameSet[p] {
			t.Errorf("gesturePastTense carries %q but gestureMobileName does not — a `appium: {%q}` step would dispatch `mobile: ` with an EMPTY command name", p, "method: "+p)
		}
	}
}

// TestSessionActionsAreSchemaActions pins the `_action` table against its ROUTER:
// session_method.go's `switch in.Action`. This is the pair the sweep could most easily
// merge with the method words — both live on #AppiumInput — and a merged or mistyped set
// falls through to "session requires action" at dispatch instead of failing the schema.
func TestSessionActionsAreSchemaActions(t *testing.T) {
	src := readSource(t, "session_method.go")
	fn := runSessionFunc.FindString(src)
	if fn == "" {
		t.Fatal("could not locate runSession in session_method.go")
	}
	routed := map[string]bool{}
	for _, arm := range caseArm.FindAllStringSubmatch(fn, -1) {
		for _, q := range quotedWord.FindAllStringSubmatch(arm[1], -1) {
			routed[q[1]] = true
		}
	}
	if len(routed) == 0 {
		t.Fatal("runSession routes no action — this check would pass over nothing")
	}
	advertised := map[string]bool{}
	for _, a := range schemaActions(t) {
		advertised[a] = true
	}
	for a := range routed {
		if !advertised[a] {
			t.Errorf("runSession routes the action %q, which the served #AppiumInput.action enum does NOT accept — the arm is unreachable", a)
		}
	}
	for a := range advertised {
		if !routed[a] {
			t.Errorf("#AppiumInput.action accepts %q but runSession has no arm for it — the step validates and then fails with \"session requires action\"", a)
		}
	}
}
