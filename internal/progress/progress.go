package progress

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/langbot-app/langbot-cli/internal/i18n"
	"golang.org/x/term"
)

type contextKey struct{}

type Display struct {
	mu                sync.Mutex
	out               io.Writer
	animated, verbose bool
	machine, carriage bool
	painted           bool
	healthHint        bool
	title, tail       string
	number            int
	total             int
	created           time.Time
	visible           bool
	pending           []string
	seen              map[string]bool
	start             time.Time
	done, exited      chan struct{}
	once              sync.Once
}

func New(out io.Writer, lookup func(string) (string, bool), machine, verbose bool) *Display {
	display := &Display{out: out, verbose: verbose, machine: machine, created: time.Now(), total: 1, seen: map[string]bool{}, done: make(chan struct{}), exited: make(chan struct{})}
	file, ok := out.(*os.File)
	_, noColor := lookup("NO_COLOR")
	terminal, _ := lookup("TERM")
	display.carriage = ok && !machine && !noColor && terminal != "dumb" && term.IsTerminal(int(file.Fd()))
	display.animated = display.carriage && enableVT(file)
	go display.animate()
	return display
}

func WithContext(ctx context.Context, display *Display) context.Context {
	return context.WithValue(ctx, contextKey{}, display)
}

func Step(ctx context.Context, title string) {
	if display, ok := ctx.Value(contextKey{}).(*Display); ok {
		display.Step(title)
	}
}

func Output(ctx context.Context, fallback io.Writer) io.Writer {
	if display, ok := ctx.Value(contextKey{}).(*Display); ok {
		return display
	}
	return fallback
}

func (display *Display) Step(title string) {
	display.mu.Lock()
	defer display.mu.Unlock()
	title = i18n.Human(title)
	if title == display.title || display.seen[title] {
		return
	}
	display.seen[title] = true
	if display.title == i18n.Human("Working") {
		display.title = ""
		display.number = 0
	}
	display.complete(true)
	display.number++
	display.title, display.tail, display.start = title, "", time.Now()
	display.healthHint = false
}

func Plan(ctx context.Context, total int) {
	if display, ok := ctx.Value(contextKey{}).(*Display); ok {
		display.mu.Lock()
		display.total = total
		display.mu.Unlock()
	}
}

func (display *Display) reveal() bool {
	if !display.visible && time.Since(display.created) < 400*time.Millisecond {
		return false
	}
	display.visible = true
	for _, line := range display.pending {
		fmt.Fprintln(display.out, line)
	}
	display.pending = nil
	return true
}

func (display *Display) line(marker string, active bool) string {
	left := marker + " " + display.title
	if active {
		left += fmt.Sprintf("  [%d/%d]", display.number, max(display.number, display.total))
	}
	right := fmt.Sprintf("%.1fs", time.Since(display.start).Seconds())
	width := min(terminalWidth(display.out)-2, 76)
	if width < 58 {
		return fit(left+" "+right, width)
	}
	left = fit(left, width-len(right)-3)
	return "  " + left + strings.Repeat(" ", width-2-Width(left)-len(right)) + right
}

func (display *Display) clear() {
	if !display.painted {
		return
	}
	if display.animated {
		fmt.Fprint(display.out, "\r\x1b[2K\x1b[1A\r\x1b[2K")
	} else {
		fmt.Fprint(display.out, "\r"+strings.Repeat(" ", terminalWidth(display.out)-1)+"\r")
	}
	display.painted = false
}

func (display *Display) complete(success bool) {
	if display.title == "" {
		return
	}
	display.clear()
	marker := "✓"
	if !success {
		marker = "✗"
	}
	line := display.line(marker, false)
	if !display.reveal() {
		display.pending = append(display.pending, line)
		return
	}
	fmt.Fprintln(display.out, line)
}

func (display *Display) Write(data []byte) (int, error) {
	display.mu.Lock()
	defer display.mu.Unlock()
	text := clean(string(data))
	display.tail += text
	if len(display.tail) > 8192 {
		display.tail = display.tail[len(display.tail)-8192:]
	}
	if display.verbose {
		display.clear()
		_, err := io.WriteString(display.out, text)
		return len(data), err
	}
	return len(data), nil
}

func clean(text string) string {
	var result strings.Builder
	escape := false
	for _, char := range text {
		if char == '\x1b' {
			escape = true
			continue
		}
		if escape {
			if char >= '@' && char <= '~' && char != '[' {
				escape = false
			}
			continue
		}
		if char == '\r' {
			char = '\n'
		}
		if char == '\n' || char == '\t' || !unicode.IsControl(char) {
			result.WriteRune(char)
		}
	}
	return result.String()
}

func (display *Display) animate() {
	defer close(display.exited)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	frame := 0
	for {
		select {
		case <-display.done:
			return
		case <-ticker.C:
			display.mu.Lock()
			if display.title != "" && display.reveal() && display.carriage {
				lines := strings.Split(strings.TrimSpace(display.tail), "\n")
				display.clear()
				width := terminalWidth(display.out) - 1
				marker := string([]rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")[frame%10])
				if !display.animated {
					marker = string("|/-\\"[frame%4])
				}
				title := display.line(marker, true)
				if display.animated {
					fmt.Fprintf(display.out, "\x1b[36m%s\x1b[0m\n\x1b[2m%s\x1b[0m", title, fit("    "+lines[len(lines)-1], width))
				} else {
					fmt.Fprint(display.out, title)
				}
				display.painted = true
				frame++
			}
			display.mu.Unlock()
		}
	}
}

func (display *Display) Finish(success bool) {
	display.once.Do(func() {
		close(display.done)
		<-display.exited
		display.mu.Lock()
		defer display.mu.Unlock()
		display.complete(success)
		if !success && !display.verbose && display.tail != "" {
			lines := strings.Split(strings.TrimSpace(display.tail), "\n")
			count := 1
			if !success {
				count = 20
			}
			if len(lines) > count {
				lines = lines[len(lines)-count:]
			}
			fmt.Fprintln(display.out, strings.Join(lines, "\n"))
		}
	})
}

type downloadReader struct {
	io.Reader
	ctx         context.Context
	total, read int64
	last        time.Time
}

func Download(ctx context.Context, reader io.Reader, total int64) io.Reader {
	return &downloadReader{Reader: reader, ctx: ctx, total: total}
}

func (reader *downloadReader) Read(data []byte) (int, error) {
	count, err := reader.Reader.Read(data)
	reader.read += int64(count)
	if time.Since(reader.last) >= time.Second || err == io.EOF {
		reader.last = time.Now()
		if display, ok := reader.ctx.Value(contextKey{}).(*Display); ok {
			display.mu.Lock()
			text := fmt.Sprintf("%d bytes", reader.read)
			if reader.total > 0 {
				text += fmt.Sprintf(" / %d bytes (%d%%)", reader.total, reader.read*100/reader.total)
			}
			display.tail = text
			if display.verbose && !display.carriage {
				fmt.Fprintln(display.out, text)
			}
			display.mu.Unlock()
		}
	}
	return count, err
}
