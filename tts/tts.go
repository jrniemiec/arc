// Package tts provides macOS say(1)-based text-to-speech with hang protection.
//
// Ported from github.com/jrniemiec/c2/core/tts.go and c2/tui/update.go.
// Uses only stdlib — no CGo, no PortAudio, no sherpa-onnx.
package tts

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

// ── Text preprocessing (from c2/core/tts.go) ────────────────────────────────

var (
	ttsCodeBlock  = regexp.MustCompile("(?s)```.*?```")
	ttsInlineCode = regexp.MustCompile("`[^`]+`")
	ttsURL        = regexp.MustCompile(`https?://\S+`)
	ttsBullets    = regexp.MustCompile(`(?m)^[\s]*[•●○◦▪▫–—\-\*]+\s*`)
	ttsMDSymbols  = regexp.MustCompile(`[*_#~|>\[\]{}\\]+`)
	ttsBoxDrawing = regexp.MustCompile(`[\x{2500}-\x{257F}\x{2580}-\x{259F}]+`)
	ttsArrows     = regexp.MustCompile(`[\x{2190}-\x{21FF}\x{27F0}-\x{27FF}\x{2900}-\x{297F}\x{1F800}-\x{1F8FF}]+`)
	ttsMathOps    = regexp.MustCompile(`[\x{2200}-\x{22FF}\x{2A00}-\x{2AFF}]+`)
	ttsDingbats   = regexp.MustCompile(`[\x{2700}-\x{27BF}]+`)
	ttsMiscTech   = regexp.MustCompile(`[\x{2300}-\x{23FF}]+`)
	ttsEnclosed   = regexp.MustCompile(`[\x{2460}-\x{24FF}]+`)
	ttsLetterlike = regexp.MustCompile(`[\x{2100}-\x{214F}]+`)
	ttsCombining  = regexp.MustCompile(`[\x{0300}-\x{036F}]+`)
	ttsEmoji      = regexp.MustCompile(`[\x{1F000}-\x{1FFFF}]+`)
	ttsDashRun    = regexp.MustCompile(`[-=~_+]{3,}`)
	ttsMultiSpace = regexp.MustCompile(`[ \t]{2,}`)
	ttsMultiNL    = regexp.MustCompile(`\n{3,}`)
)

// Strip removes markdown and characters that cause say(1) to mispronounce
// or produce unwanted noise.
func Strip(s string) string {
	s = ttsCodeBlock.ReplaceAllString(s, ". ")
	s = ttsInlineCode.ReplaceAllString(s, "")
	s = ttsURL.ReplaceAllString(s, "")
	s = ttsBullets.ReplaceAllString(s, "")
	s = ttsMDSymbols.ReplaceAllString(s, "")
	s = ttsBoxDrawing.ReplaceAllString(s, "")
	s = ttsArrows.ReplaceAllString(s, "")
	s = ttsMathOps.ReplaceAllString(s, "")
	s = ttsDingbats.ReplaceAllString(s, "")
	s = ttsMiscTech.ReplaceAllString(s, "")
	s = ttsEnclosed.ReplaceAllString(s, "")
	s = ttsLetterlike.ReplaceAllString(s, "")
	s = ttsCombining.ReplaceAllString(s, "")
	s = ttsEmoji.ReplaceAllString(s, "")
	s = ttsDashRun.ReplaceAllString(s, "")
	s = ttsMultiSpace.ReplaceAllString(s, " ")
	s = ttsMultiNL.ReplaceAllString(s, "\n\n")

	// Filter lines that are mostly non-alphanumeric (diagrams, table separators).
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			out = append(out, "")
			continue
		}
		total := 0
		alnum := 0
		for _, r := range trimmed {
			total++
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				alnum++
			}
		}
		if alnum*100/total >= 30 {
			out = append(out, line)
		}
	}

	return strings.TrimSpace(strings.Join(out, "\n"))
}

// JoinSoftWraps joins soft-wrapped lines into paragraph blocks suitable for TTS.
// A new block starts on blank lines, lines ending with ?/!, headings, list items,
// and code fences.
func JoinSoftWraps(text string) []string {
	lines := strings.Split(text, "\n")
	var blocks []string
	var current []string

	flush := func() {
		joined := strings.TrimSpace(strings.Join(current, " "))
		if joined != "" {
			blocks = append(blocks, joined)
		}
		current = current[:0]
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
			flush()
			continue
		}

		isHeading := strings.HasPrefix(trimmed, "#")
		isList := len(trimmed) > 0 && (trimmed[0] == '-' || trimmed[0] == '*' ||
			(trimmed[0] >= '0' && trimmed[0] <= '9'))
		isCodeFence := strings.HasPrefix(trimmed, "```")

		if isHeading || isCodeFence || isList {
			flush()
			current = append(current, trimmed)
			flush()
			continue
		}

		current = append(current, trimmed)

		last := trimmed[len(trimmed)-1]
		if last == '?' || last == '!' {
			flush()
		}
	}
	flush()
	return blocks
}

// ── Player (from c2/tui/update.go — say(1) subprocess management) ───────────

// DoneMsg is sent when TTS playback completes or is interrupted.
type DoneMsg struct {
	Err error
	Gen int // generation counter; ignore if != Player.Gen()
}

// Player manages a macOS say(1) subprocess with hang protection.
type Player struct {
	mu    sync.Mutex
	cmd   *exec.Cmd
	gen   int
	voice string
	rate  int
}

// NewPlayer creates a Player with the given voice and rate (words per minute).
// Empty voice uses system default. Rate <= 0 defaults to 200.
func NewPlayer(voice string, rate int) *Player {
	if rate <= 0 {
		rate = 200
	}
	return &Player{voice: voice, rate: rate}
}

// Gen returns the current generation counter.
func (p *Player) Gen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gen
}

// Playing reports whether TTS is currently active.
func (p *Player) Playing() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd != nil
}

// Play starts say(1) with the given text and returns a function that blocks
// until playback completes, returning a DoneMsg. The caller is responsible
// for running the returned function in a goroutine (or as a tea.Cmd).
//
// A timeout is computed from the text length to guard against say(1) hangs.
// The process is placed in its own process group so Stop() can kill both
// say and its child audio synthesis process.
func (p *Player) Play(text string) func() DoneMsg {
	p.mu.Lock()
	p.gen++
	gen := p.gen

	args := []string{"-r", fmt.Sprintf("%d", p.rate)}
	if p.voice != "" {
		args = append(args, "-v", p.voice)
	}

	// Estimate timeout: expected speech duration + 3s grace, minimum 8s.
	words := len([]rune(text))/5 + 1
	secs := words*60/p.rate + 3
	if secs < 8 {
		secs = 8
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs)*time.Second)
	cmd := exec.CommandContext(ctx, "say", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = strings.NewReader(text)
	p.cmd = cmd
	p.mu.Unlock()

	return func() DoneMsg {
		defer cancel()
		err := cmd.Run()
		p.mu.Lock()
		if p.cmd == cmd {
			p.cmd = nil
		}
		p.mu.Unlock()
		return DoneMsg{Err: err, Gen: gen}
	}
}

// Stop kills any in-flight say(1) process and its entire process group.
func (p *Player) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil {
		return
	}
	cmd := p.cmd
	p.cmd = nil
	p.gen++ // invalidate any in-flight DoneMsg
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// SetVoice changes the voice for future Play calls.
func (p *Player) SetVoice(voice string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.voice = voice
}

// SetRate changes the rate (wpm) for future Play calls.
func (p *Player) SetRate(rate int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if rate <= 0 {
		rate = 200
	}
	p.rate = rate
}
