package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/testutil"
)

var (
	errMissing  = errors.New("missing")
	errAuthBoom = errors.New("auth boom")
)

func baseDeps() Deps {
	return Deps{
		Gh:            &testutil.FakeGhClient{},
		LookPath:      func(string) (string, error) { return "", errMissing },
		Getenv:        func(string) string { return "" },
		Home:          "/nonexistent-home",
		Version:       "v0.0.0-test",
		GenCompletion: func(string) (string, error) { return "gen", nil },
	}
}

func findCheck(r Report, name string) Check {
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	return Check{}
}

func TestRun_AuthFailureFailsRequired(t *testing.T) {
	d := baseDeps()
	d.Gh = &testutil.FakeGhClient{AuthErr: errAuthBoom}

	rep := Run(d)
	if rep.OK() {
		t.Error("doctor with gh auth failure = OK, want required failure")
	}
	auth := findCheck(rep, "gh auth")
	if auth.OK || !auth.Required {
		t.Errorf("gh auth check = %+v, want required failure", auth)
	}
	if !strings.Contains(auth.Detail, "gh auth login") {
		t.Errorf("auth detail = %q, want login guidance", auth.Detail)
	}
}

func TestRun_OptionalShortagesDoNotFail(t *testing.T) {
	// No herdr, no agents, no completion: all optional → still OK
	// as long as gh auth passes.
	rep := Run(baseDeps())
	if !rep.OK() {
		t.Errorf("doctor with only optional gaps = fail; checks: %+v", rep.Checks)
	}
	for _, name := range []string{"herdr", "agents", "completion", "keybinding"} {
		c := findCheck(rep, name)
		if c.Required {
			t.Errorf("%s marked required, want optional", name)
		}
		if c.OK {
			t.Errorf("%s = ok with nothing present", name)
		}
	}
}

func TestRun_HerdrAndAgentsDetected(t *testing.T) {
	d := baseDeps()
	d.LookPath = func(name string) (string, error) {
		if name == "herdr" || name == "agy" {
			return "/bin/" + name, nil
		}
		return "", errMissing
	}
	d.Getenv = func(k string) string {
		if k == "HERDR_ENV" {
			return "1"
		}
		return ""
	}

	rep := Run(d)
	if c := findCheck(rep, "herdr"); !c.OK || !strings.Contains(c.Detail, "multipane") {
		t.Errorf("herdr check = %+v, want multipane available", c)
	}
	if c := findCheck(rep, "agents"); !c.OK || !strings.Contains(c.Detail, "agy") {
		t.Errorf("agents check = %+v, want agy listed", c)
	}
}

func TestRun_OfflineSkipsNetworkProbe(t *testing.T) {
	d := baseDeps()
	fake := &testutil.FakeGhClient{ApiUserLogin: "octocat"}
	d.Gh = fake
	d.Offline = true

	rep := Run(d)
	if c := findCheck(rep, "github api"); c.OK || !strings.Contains(c.Detail, "skipped") {
		t.Errorf("api check offline = %+v, want skipped", c)
	}
}

func TestRun_OnlineProbesAPI(t *testing.T) {
	d := baseDeps()
	d.Gh = &testutil.FakeGhClient{ApiUserLogin: "octocat"}

	rep := Run(d)
	if c := findCheck(rep, "github api"); !c.OK || !strings.Contains(c.Detail, "octocat") {
		t.Errorf("api check = %+v, want login detail", c)
	}
}

func TestReport_JSONShape(t *testing.T) {
	rep := Run(baseDeps())
	raw := rep.JSON()
	for _, want := range []string{`"name"`, `"required"`, `"ok"`, `"gh auth"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("doctor JSON missing %s:\n%s", want, raw)
		}
	}
}
