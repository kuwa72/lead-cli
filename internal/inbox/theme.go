package inbox

import (
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Mode selects how the inbox renders colors and decorations.
type Mode int

const (
	ModePlain Mode = iota
	ModeMonochrome
	ModeTerminal
	ModeDark
	ModeLight
)

// Token names the semantic color slots from docs/rfc-inbox-ui-density.md §7.2.
type Token string

const (
	TokenBgCanvas      Token = "bg.canvas"
	TokenBgSurface     Token = "bg.surface"
	TokenBgSelection   Token = "bg.selection"
	TokenFgPrimary     Token = "fg.primary"
	TokenFgSecondary   Token = "fg.secondary"
	TokenFgEmphasis    Token = "fg.emphasis"
	TokenFgSelection   Token = "fg.selection"
	TokenBorderMuted   Token = "border.muted"
	TokenBorderStrong  Token = "border.strong"
	TokenAccentPrimary Token = "accent.primary"
	TokenStatusInfo    Token = "status.info"
	TokenStatusWarning Token = "status.warning"
	TokenStatusError   Token = "status.error"
	TokenStatusSuccess Token = "status.success"
)

// colorDef holds the palette values for one token.
type colorDef struct {
	darkRGB, lightRGB string
	dark256, light256 uint8
	ansi              uint8
	terminalDefault   bool // true for tokens that use the terminal's default fg/bg
}

var tokenColors = map[Token]colorDef{
	TokenBgCanvas:      {darkRGB: "#161B22", lightRGB: "#F6F8FA", dark256: 234, light256: 255, ansi: 0, terminalDefault: true},
	TokenBgSurface:     {darkRGB: "#21262D", lightRGB: "#FFFFFF", dark256: 235, light256: 231, ansi: 0, terminalDefault: true},
	TokenBgSelection:   {darkRGB: "#263C46", lightRGB: "#D8EBF0", dark256: 237, light256: 254, ansi: 8, terminalDefault: true},
	TokenFgPrimary:     {darkRGB: "#E6EDF3", lightRGB: "#1F2328", dark256: 255, light256: 235, ansi: 15, terminalDefault: true},
	TokenFgSecondary:   {darkRGB: "#A8B3BF", lightRGB: "#59636E", dark256: 249, light256: 241, ansi: 7, terminalDefault: true},
	TokenFgEmphasis:    {darkRGB: "#FFFFFF", lightRGB: "#0D1117", dark256: 231, light256: 233, ansi: 15, terminalDefault: true},
	TokenFgSelection:   {darkRGB: "#F0F6FC", lightRGB: "#142D36", dark256: 255, light256: 235, ansi: 15, terminalDefault: true},
	TokenBorderMuted:   {darkRGB: "#444C56", lightRGB: "#BBC3CD", dark256: 239, light256: 251, ansi: 8, terminalDefault: true},
	TokenBorderStrong:  {darkRGB: "#7D8995", lightRGB: "#66717E", dark256: 245, light256: 243, ansi: 7, terminalDefault: true},
	TokenAccentPrimary: {darkRGB: "#76CEDB", lightRGB: "#096978", dark256: 116, light256: 23, ansi: 6},
	TokenStatusInfo:    {darkRGB: "#A5BFFF", lightRGB: "#315A96", dark256: 147, light256: 60, ansi: 4},
	TokenStatusWarning: {darkRGB: "#E6B566", lightRGB: "#805800", dark256: 179, light256: 94, ansi: 3},
	TokenStatusError:   {darkRGB: "#F28B82", lightRGB: "#B42332", dark256: 210, light256: 124, ansi: 1},
	TokenStatusSuccess: {darkRGB: "#88C99A", lightRGB: "#216E39", dark256: 115, light256: 22, ansi: 2},
}

// Theme maps semantic tokens to SGR sequences for a resolved Mode and profile.
type Theme struct {
	mode     Mode
	profile  termenv.Profile
	renderer *lipgloss.Renderer
	out      io.Writer
}

// ValidateTheme checks that a LEAD_THEME value is one of the accepted names.
func ValidateTheme(theme string) error {
	if theme == "" {
		return nil
	}
	if theme != "terminal" && theme != "dark" && theme != "light" {
		return fmt.Errorf("Invalid LEAD_THEME: expected terminal, dark, or light.")
	}
	return nil
}

// ResolveMode picks the render mode and color profile from environment values.
// It validates LEAD_THEME and applies the NO_COLOR / TERM=dumb precedence in
// docs/rfc-inbox-ui-density.md §7.4.
func ResolveMode(themeEnv, noColor, colorTerm, termEnv string) (Mode, termenv.Profile, error) {
	if err := ValidateTheme(themeEnv); err != nil {
		return ModePlain, termenv.Ascii, err
	}

	if termEnv == "dumb" {
		return ModePlain, termenv.Ascii, nil
	}
	if noColor != "" {
		return ModeMonochrome, termenv.ANSI, nil
	}

	switch themeEnv {
	case "dark":
		return ModeDark, profileForColorTerm(colorTerm, termEnv), nil
	case "light":
		return ModeLight, profileForColorTerm(colorTerm, termEnv), nil
	default:
		return ModeTerminal, termenv.ANSI, nil
	}
}

func profileForColorTerm(colorTerm, termEnv string) termenv.Profile {
	ct := strings.ToLower(colorTerm)
	if strings.Contains(ct, "truecolor") || strings.Contains(ct, "24bit") {
		return termenv.TrueColor
	}
	if strings.Contains(termEnv, "256color") {
		return termenv.ANSI256
	}
	return termenv.ANSI
}

// NewTheme creates a Theme for the given output. The profile is forced so
// headless tests can request ANSI output even when out is not a TTY.
func NewTheme(mode Mode, profile termenv.Profile, out io.Writer) *Theme {
	if out == nil {
		out = os.Stdout
	}
	renderer := lipgloss.NewRenderer(out, termenv.WithProfile(profile))
	return &Theme{mode: mode, profile: profile, renderer: renderer, out: out}
}

// Mode returns the resolved rendering mode.
func (t *Theme) Mode() Mode { return t.mode }

// Profile returns the color profile.
func (t *Theme) Profile() termenv.Profile { return t.profile }

// Render returns text styled with the given token colors and attributes. In
// plain mode it returns the sanitized, ASCII-replaced text with no SGR codes.
func (t *Theme) Render(text string, fg, bg Token, bold, reverse, underline bool) string {
	text = Sanitize(text)
	if t.mode == ModePlain {
		return asciiReplace(text)
	}
	style := t.renderer.NewStyle()
	if c, ok := tokenColors[fg]; ok {
		if col := t.colorFor(c); col != nil {
			style = style.Foreground(col)
		}
	}
	if c, ok := tokenColors[bg]; ok {
		if col := t.colorFor(c); col != nil {
			style = style.Background(col)
		}
	}
	if bold {
		style = style.Bold(true)
	}
	if reverse {
		style = style.Reverse(true)
	}
	if underline {
		style = style.Underline(true)
	}
	return style.Render(text)
}

func (t *Theme) colorFor(c colorDef) lipgloss.TerminalColor {
	switch t.mode {
	case ModePlain, ModeMonochrome:
		return nil
	case ModeTerminal:
		if c.terminalDefault {
			return nil
		}
		return lipgloss.Color(strconv.Itoa(int(c.ansi)))
	case ModeDark:
		switch t.profile {
		case termenv.TrueColor:
			return lipgloss.Color(c.darkRGB)
		case termenv.ANSI256:
			return lipgloss.Color(strconv.Itoa(int(c.dark256)))
		default:
			return lipgloss.Color(strconv.Itoa(int(c.ansi)))
		}
	case ModeLight:
		switch t.profile {
		case termenv.TrueColor:
			return lipgloss.Color(c.lightRGB)
		case termenv.ANSI256:
			return lipgloss.Color(strconv.Itoa(int(c.light256)))
		default:
			return lipgloss.Color(strconv.Itoa(int(c.ansi)))
		}
	}
	return nil
}

// Accent renders the primary accent color (selected row marker, preview number).
func (t *Theme) Accent(text string, bold bool) string {
	if t.mode == ModeTerminal || t.mode == ModeMonochrome {
		return t.Render(text, TokenAccentPrimary, "", bold, false, false)
	}
	return t.Render(text, TokenAccentPrimary, "", bold, false, false)
}

// Selected renders text for the selected row. In terminal/monochrome it uses
// reverse video; in dark/light it uses the selection background.
func (t *Theme) Selected(text string) string {
	if t.mode == ModeTerminal || t.mode == ModeMonochrome {
		return t.Render(text, TokenFgSelection, TokenBgSelection, false, true, false)
	}
	return t.Render(text, TokenFgSelection, TokenBgSelection, true, false, false)
}

// ListRow renders a non-selected list row.
func (t *Theme) ListRow(text string, header bool) string {
	if header {
		return t.Render(text, TokenFgPrimary, TokenBgCanvas, true, false, false)
	}
	return t.Render(text, TokenFgPrimary, TokenBgCanvas, false, false, false)
}

// Header renders the dashboard header line.
func (t *Theme) Header(repo string, meta string) string {
	return t.Render(repo, TokenFgEmphasis, TokenBgCanvas, true, false, false) +
		t.Render(meta, TokenFgSecondary, TokenBgCanvas, false, false, false)
}

// Status renders the feedback/status line using semantic colors when the
// status text matches a known prefix.
func (t *Theme) Status(text string) string {
	fg := TokenFgSecondary
	switch {
	case strings.HasPrefix(text, "Loading") || strings.HasPrefix(text, "Approving") || strings.HasPrefix(text, "Commenting"):
		fg = TokenStatusInfo
	case strings.HasPrefix(text, "Approved") || strings.HasPrefix(text, "Commented") || strings.HasPrefix(text, "Rejected") || strings.HasPrefix(text, "Marked"):
		fg = TokenStatusSuccess
	case strings.HasPrefix(text, "Error") || strings.HasPrefix(text, "Unable") || strings.HasPrefix(text, "Load error"):
		fg = TokenStatusError
	case strings.HasPrefix(text, "Issue changed"):
		fg = TokenStatusWarning
	}
	return t.Render(text, fg, TokenBgCanvas, false, false, false)
}

// Cursor renders the text/character under the input cursor. In terminal/monochrome/dark/light
// modes it uses reverse video. In plain mode or ASCII profile it returns a visible representation.
func (t *Theme) Cursor(text string) string {
	if t.mode == ModePlain || t.profile == termenv.Ascii {
		if text == "" || text == " " {
			return "_"
		}
		return text
	}
	if text == "" {
		text = " "
	}
	return t.Render(text, TokenFgPrimary, TokenBgCanvas, false, true, false)
}

// Border returns a single border character with the strong/muted token.
func (t *Theme) Border(strong bool) string {
	if strong {
		return t.Render("│", TokenBorderStrong, "", false, false, false)
	}
	return t.Render("─", TokenBorderMuted, "", false, false, false)
}

// Preview renders a line of preview text with diff syntax highlighting.
func (t *Theme) Preview(text string, title bool) string {
	if title {
		return t.Render(text, TokenFgEmphasis, TokenBgSurface, true, false, false)
	}
	trimmed := strings.TrimLeft(text, " ")
	if strings.HasPrefix(trimmed, "+") && !strings.HasPrefix(trimmed, "+++") {
		return t.Render(text, TokenStatusSuccess, TokenBgSurface, false, false, false)
	}
	if strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "---") {
		return t.Render(text, TokenStatusError, TokenBgSurface, false, false, false)
	}
	if strings.HasPrefix(trimmed, "@@") {
		return t.Render(text, TokenAccentPrimary, TokenBgSurface, true, false, false)
	}
	if strings.HasPrefix(trimmed, "diff --git") || strings.HasPrefix(trimmed, "+++") || strings.HasPrefix(trimmed, "---") {
		return t.Render(text, TokenFgEmphasis, TokenBgSurface, true, false, false)
	}
	if strings.HasPrefix(trimmed, "Changed files") || strings.HasPrefix(trimmed, "PR Diff") {
		return t.Render(text, TokenFgEmphasis, TokenBgSurface, true, false, false)
	}
	return t.Render(text, TokenFgPrimary, TokenBgSurface, false, false, false)
}

// ErrorDetail renders error/loading text in the preview pane.
func (t *Theme) ErrorDetail(text string) string {
	return t.Render(text, TokenStatusError, TokenBgSurface, false, false, false)
}

// InfoDetail renders informational preview text.
func (t *Theme) InfoDetail(text string) string {
	return t.Render(text, TokenFgSecondary, TokenBgSurface, false, false, false)
}

var (
	csiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	oscRe = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?`)
	ctlRe = regexp.MustCompile(`[\x00-\x08\x0b-\x0c\x0e-\x1f\x7f]`)
)

// Sanitize removes ANSI/OSC/cursor control sequences and other C0 control
// characters from external strings before they are measured or displayed.
func Sanitize(s string) string {
	s = oscRe.ReplaceAllString(s, "")
	s = csiRe.ReplaceAllString(s, "")
	s = ctlRe.ReplaceAllString(s, "")
	return s
}

func asciiReplace(s string) string {
	r := strings.NewReplacer(
		"▾", "v",
		"▸", ">",
		"─", "-",
		"│", "|",
		"…", "...",
	)
	return r.Replace(s)
}

// Contrast returns the WCAG contrast ratio between two tokens for the current
// mode and profile. It is used by theme tests to validate §9.
func (t *Theme) Contrast(a, b Token) (float64, error) {
	c1, ok := tokenColors[a]
	if !ok {
		return 0, fmt.Errorf("unknown token %q", a)
	}
	c2, ok := tokenColors[b]
	if !ok {
		return 0, fmt.Errorf("unknown token %q", b)
	}
	rgbA, err := t.rgb(c1)
	if err != nil {
		return 0, err
	}
	rgbB, err := t.rgb(c2)
	if err != nil {
		return 0, err
	}
	return contrastRatio(rgbA, rgbB), nil
}

func (t *Theme) rgb(c colorDef) ([3]float64, error) {
	switch t.mode {
	case ModeDark:
		if t.profile == termenv.ANSI256 {
			return ansi256ToRGB(c.dark256)
		}
		return hexToRGB(c.darkRGB)
	case ModeLight:
		if t.profile == termenv.ANSI256 {
			return ansi256ToRGB(c.light256)
		}
		return hexToRGB(c.lightRGB)
	default:
		return hexToRGB(c.darkRGB)
	}
}

func hexToRGB(hex string) ([3]float64, error) {
	if len(hex) != 7 || hex[0] != '#' {
		return [3]float64{}, fmt.Errorf("invalid hex %q", hex)
	}
	var r, g, b int
	if _, err := fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b); err != nil {
		return [3]float64{}, err
	}
	return [3]float64{float64(r) / 255, float64(g) / 255, float64(b) / 255}, nil
}

func ansi256ToRGB(c uint8) ([3]float64, error) {
	n := int(c)
	switch {
	case n < len(ansi16):
		v := ansi16[n]
		return [3]float64{float64(v[0]) / 255, float64(v[1]) / 255, float64(v[2]) / 255}, nil
	case n < 232:
		m := n - 16
		r := m / 36
		g := (m % 36) / 6
		b := m % 6
		return [3]float64{cubeVal(r), cubeVal(g), cubeVal(b)}, nil
	default:
		v := 8 + (n-232)*10
		f := float64(v) / 255
		return [3]float64{f, f, f}, nil
	}
}

func cubeVal(i int) float64 {
	vals := []int{0, 95, 135, 175, 215, 255}
	return float64(vals[i]) / 255
}

// contrastRatio uses the sRGB relative luminance formula from RFC §9.
func contrastRatio(a, b [3]float64) float64 {
	la := relativeLuminance(a)
	lb := relativeLuminance(b)
	if la > lb {
		return (la + 0.05) / (lb + 0.05)
	}
	return (lb + 0.05) / (la + 0.05)
}

func relativeLuminance(c [3]float64) float64 {
	var l [3]float64
	for i, v := range c {
		if v <= 0.04045 {
			l[i] = v / 12.92
		} else {
			l[i] = math.Pow((v+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*l[0] + 0.7152*l[1] + 0.0722*l[2]
}

// ansi16 is the xterm 256-color standard 16-color palette (R, G, B).
var ansi16 = [16][3]int{
	{0, 0, 0}, {205, 0, 0}, {0, 205, 0}, {205, 205, 0},
	{0, 0, 238}, {205, 0, 205}, {0, 205, 205}, {229, 229, 229},
	{127, 127, 127}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0},
	{0, 0, 255}, {255, 0, 255}, {0, 255, 255}, {255, 255, 255},
}
