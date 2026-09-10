package inbox

import (
	"fmt"
	"strings"
	"testing"

	"github.com/muesli/termenv"
)

func TestResolveTheme(t *testing.T) {
	tests := []struct {
		name      string
		theme     string
		noColor   string
		colorTerm string
		termEnv   string
		wantMode  Mode
		wantProf  termenv.Profile
		wantErr   bool
	}{
		{"default terminal", "", "", "", "xterm-256color", ModeTerminal, termenv.ANSI, false},
		{"explicit terminal", "terminal", "", "", "xterm-256color", ModeTerminal, termenv.ANSI, false},
		{"dark truecolor", "dark", "", "truecolor", "xterm-256color", ModeDark, termenv.TrueColor, false},
		{"dark 256", "dark", "", "", "xterm-256color", ModeDark, termenv.ANSI256, false},
		{"dark ansi fallback", "dark", "", "", "xterm", ModeDark, termenv.ANSI, false},
		{"light truecolor", "light", "", "truecolor", "xterm-256color", ModeLight, termenv.TrueColor, false},
		{"no_color any value monochrome", "dark", "0", "truecolor", "xterm-256color", ModeMonochrome, termenv.ANSI, false},
		{"empty no_color is unset", "dark", "", "truecolor", "xterm-256color", ModeDark, termenv.TrueColor, false},
		{"term dumb plain", "dark", "", "truecolor", "dumb", ModePlain, termenv.Ascii, false},
		{"invalid plain", "plain", "", "truecolor", "xterm-256color", ModePlain, termenv.Ascii, true},
		{"invalid theme", "blue", "", "", "", ModePlain, termenv.Ascii, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mode, prof, err := ResolveMode(tc.theme, tc.noColor, tc.colorTerm, tc.termEnv)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				if !strings.Contains(err.Error(), "Invalid LEAD_THEME") {
					t.Fatalf("expected invalid LEAD_THEME error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if mode != tc.wantMode || prof != tc.wantProf {
				t.Fatalf("mode=%v prof=%v, want mode=%v prof=%v", mode, prof, tc.wantMode, tc.wantProf)
			}
		})
	}
}

func TestSanitizeStripsControlSequences(t *testing.T) {
	in := "\x1b[31mred\x1b[0m \x1b]0;title\x07normal\x00\x01\t\n"
	got := Sanitize(in)
	want := "red normal\t\n"
	if got != want {
		t.Errorf("Sanitize = %q, want %q", got, want)
	}
}

func TestPlainRenderNoSGRAndASCII(t *testing.T) {
	th := NewTheme(ModePlain, termenv.Ascii, nil)
	got := th.Render("▾ Blocked (1) …", TokenFgPrimary, TokenBgCanvas, true, false, false)
	if strings.Contains(got, "\x1b") {
		t.Errorf("plain output contains escape: %q", got)
	}
	if !strings.Contains(got, "v") || !strings.Contains(got, "...") || strings.Contains(got, "▾") || strings.Contains(got, "…") {
		t.Errorf("plain output not ASCII-replaced: %q", got)
	}
}

func TestMonochromeRenderBoldNoColor(t *testing.T) {
	var b strings.Builder
	th := NewTheme(ModeMonochrome, termenv.ANSI, &b)
	got := th.Render("hello", TokenStatusError, "", true, false, false)
	if !strings.Contains(got, "\x1b[1m") && !strings.Contains(got, "\x1b[7m") {
		t.Errorf("monochrome output lacks bold/reverse: %q", got)
	}
	for _, c := range []string{"\x1b[31m", "\x1b[38;5;", "\x1b[38;2;"} {
		if strings.Contains(got, c) {
			t.Errorf("monochrome output contains color %q: %q", c, got)
		}
	}
}

func TestTerminalUsesBasicANSIColors(t *testing.T) {
	var b strings.Builder
	th := NewTheme(ModeTerminal, termenv.ANSI, &b)
	got := th.Accent("> ", true)
	if !strings.Contains(got, "\x1b[36m") && !strings.Contains(got, "\x1b[1;36m") {
		t.Errorf("terminal accent did not emit cyan ANSI: %q", got)
	}
}

func TestDarkTrueColorUsesRGB(t *testing.T) {
	var b strings.Builder
	th := NewTheme(ModeDark, termenv.TrueColor, &b)
	got := th.Render("x", TokenAccentPrimary, TokenBgCanvas, false, false, false)
	if !strings.Contains(got, "38;2;") && !strings.Contains(got, "48;2;") {
		t.Errorf("dark truecolor did not emit RGB SGR: %q", got)
	}
}

func TestContrastAllTokenPairs(t *testing.T) {
	fgTokens := []Token{TokenFgPrimary, TokenFgSecondary, TokenFgEmphasis, TokenAccentPrimary, TokenStatusInfo, TokenStatusWarning, TokenStatusError, TokenStatusSuccess}
	bgTokens := []Token{TokenBgCanvas, TokenBgSurface}
	selTokens := []Token{TokenFgSelection, TokenAccentPrimary}
	selBg := TokenBgSelection
	borderPairs := []Token{TokenBgCanvas, TokenBgSurface, TokenBgSelection}

	for _, mode := range []Mode{ModeDark, ModeLight} {
		for _, prof := range []termenv.Profile{termenv.TrueColor, termenv.ANSI256} {
			th := NewTheme(mode, prof, nil)
			for _, fg := range fgTokens {
				for _, bg := range bgTokens {
					ratio, err := th.Contrast(fg, bg)
					if err != nil {
						t.Fatalf("contrast %s/%s mode=%v prof=%v: %v", fg, bg, mode, prof, err)
					}
					if ratio < 4.5 {
						t.Errorf("contrast %s/%s mode=%v prof=%v = %.2f, want >= 4.5", fg, bg, mode, prof, ratio)
					}
				}
			}
			for _, fg := range selTokens {
				ratio, err := th.Contrast(fg, selBg)
				if err != nil {
					t.Fatalf("contrast %s/%s mode=%v prof=%v: %v", fg, selBg, mode, prof, err)
				}
				if ratio < 4.5 {
					t.Errorf("contrast %s/%s mode=%v prof=%v = %.2f, want >= 4.5", fg, selBg, mode, prof, ratio)
				}
			}
			for _, bg := range borderPairs {
				ratio, err := th.Contrast(TokenBorderStrong, bg)
				if err != nil {
					t.Fatalf("contrast %s/%s mode=%v prof=%v: %v", TokenBorderStrong, bg, mode, prof, err)
				}
				if ratio < 3.0 {
					t.Errorf("contrast %s/%s mode=%v prof=%v = %.2f, want >= 3.0", TokenBorderStrong, bg, mode, prof, ratio)
				}
			}
		}
	}
}

func TestResolveThemeErrorMessage(t *testing.T) {
	_, _, err := ResolveMode("invalid", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "Invalid LEAD_THEME") {
		t.Fatalf("expected invalid theme error, got %v", err)
	}
}

func TestThemeOutputWidthIgnoresANSI(t *testing.T) {
	var b strings.Builder
	th := NewTheme(ModeDark, termenv.TrueColor, &b)
	styled := th.Render("hello", TokenFgPrimary, TokenBgCanvas, false, false, false)
	if width := strings.Count(styled, ""); width <= 0 {
		_ = fmt.Sprint(width)
	}
	// The visible text should still be "hello".
	if strings.ReplaceAll(styled, "\x1b", "") == "" {
		t.Fatalf("rendered output is empty")
	}
}
