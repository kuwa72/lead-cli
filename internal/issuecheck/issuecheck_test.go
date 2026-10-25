package issuecheck

import (
	"reflect"
	"strings"
	"testing"
)

const validBody = `# feat(cli): add lead new

## 背景

受け入れ手順が分散している。

## 受入条件

- [ ] ` + "`./test/run-tests.sh`" + ` が pass する

## スコープ外

- 既存 Issue の自動修正
`

func TestCheck_ValidBodyWithBacktickCommand(t *testing.T) {
	if missing := Check(validBody, []string{"risk:b"}); len(missing) != 0 {
		t.Fatalf("Check(valid) = %v, want no missing items", missing)
	}
}

func TestCheck_ValidBodyWithTestPath(t *testing.T) {
	body := `# title

## 受入条件

- [ ] test/test_new_ready.sh が pass する

## スコープ外

- none
`
	if missing := Check(body, []string{"risk:a"}); len(missing) != 0 {
		t.Fatalf("Check(test path) = %v, want no missing items", missing)
	}
}

func TestCheck_ValidBodyWithGoTestFile(t *testing.T) {
	body := `# title

## 受入条件

- [ ] internal/issuecheck/issuecheck_test.go が通る

## スコープ外

- none
`
	if missing := Check(body, []string{"risk:a"}); len(missing) != 0 {
		t.Fatalf("Check(_test.go) = %v, want no missing items", missing)
	}
}

func TestCheck_RejectsProseOnlyAcceptance(t *testing.T) {
	body := `# title

## 受入条件

- [ ] ユーザーが嬉しい

## スコープ外

- none
`
	missing := Check(body, []string{"risk:b"})
	if len(missing) != 1 || !strings.Contains(missing[0], "実行可能") {
		t.Fatalf("Check(prose) = %v, want one executable-condition finding", missing)
	}
}

func TestCheck_RejectsMissingAcceptanceSection(t *testing.T) {
	body := `# title

## 背景

nothing here.

## スコープ外

- none
`
	missing := Check(body, []string{"risk:b"})
	want := []string{"`## 受入条件` セクションがありません"}
	if !reflect.DeepEqual(missing, want) {
		t.Fatalf("Check(no acceptance) = %v, want %v", missing, want)
	}
}

func TestCheck_RejectsAcceptanceWithoutCheckbox(t *testing.T) {
	body := `# title

## 受入条件

- ` + "`./test/run-tests.sh`" + ` が pass する

## スコープ外

- none
`
	missing := Check(body, []string{"risk:b"})
	if len(missing) != 1 || !strings.Contains(missing[0], "- [ ]") {
		t.Fatalf("Check(no checkbox) = %v, want checkbox finding", missing)
	}
}

func TestCheck_RejectsMissingScopeSection(t *testing.T) {
	body := `# title

## 受入条件

- [ ] ` + "`./test/run-tests.sh`" + ` が pass する
`
	missing := Check(body, []string{"risk:b"})
	want := []string{"`## スコープ外` セクションがありません"}
	if !reflect.DeepEqual(missing, want) {
		t.Fatalf("Check(no scope) = %v, want %v", missing, want)
	}
}

func TestCheck_RejectsMissingRisk(t *testing.T) {
	missing := Check(validBody, []string{"needs-review"})
	if len(missing) != 1 || !strings.Contains(missing[0], "risk:a") || !strings.Contains(missing[0], "risk:b") {
		t.Fatalf("Check(no risk) = %v, want risk finding", missing)
	}
}

func TestCheck_CollectsEveryMissingItem(t *testing.T) {
	missing := Check("# title\n\njust prose\n", nil)
	if len(missing) != 3 {
		t.Fatalf("Check(empty body) = %v, want 3 findings", missing)
	}
}

func TestCheck_SubsectionDoesNotEndTheSection(t *testing.T) {
	body := `# title

## 受入条件

### テスト

- [ ] ` + "`go test ./...`" + ` が pass する

## スコープ外

- none
`
	if missing := Check(body, []string{"risk:b"}); len(missing) != 0 {
		t.Fatalf("Check(subsection) = %v, want no missing items", missing)
	}
}

func TestTitle_FirstHeading(t *testing.T) {
	body := "\n## feat(cli): add lead new\n\n## 受入条件\n"
	if got := Title(body); got != "feat(cli): add lead new" {
		t.Fatalf("Title = %q", got)
	}
}

func TestTitle_None(t *testing.T) {
	if got := Title("no heading here"); got != "" {
		t.Fatalf("Title = %q, want empty", got)
	}
}

func TestMessage_Bullets(t *testing.T) {
	msg := Message([]string{"a", "b"})
	if !strings.Contains(msg, "- a") || !strings.Contains(msg, "- b") {
		t.Fatalf("Message = %q, want bullet lines", msg)
	}
}

func TestTemplate_HasRequiredSections(t *testing.T) {
	for _, want := range []string{"## 受入条件", "## スコープ外", "- [ ]"} {
		if !strings.Contains(Template, want) {
			t.Errorf("Template missing %q:\n%s", want, Template)
		}
	}
}
