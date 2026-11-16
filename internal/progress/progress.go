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
	title, tail       string
	number            int
	start             time.Time
	done, exited      chan struct{}
	once              sync.Once
}

func New(out io.Writer, lookup func(string) (string, bool), machine, verbose bool) *Display {
	display := &Display{out: out, verbose: verbose, done: make(chan struct{}), exited: make(chan struct{})}
	file, ok := out.(*os.File)
	_, noColor := lookup("NO_COLOR")
	terminal, _ := lookup("TERM")
	display.animated = ok && !machine && !noColor && terminal != "dumb" && term.IsTerminal(int(file.Fd())) && enableVT(file)
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
	if title == display.title {
		return
	}
	display.complete(true)
	display.number++
	display.title, display.tail, display.start = title, "", time.Now()
	fmt.Fprintf(display.out, "[%d] %s ...\n", display.number, title)
}

func (display *Display) complete(success bool) {
	if display.title == "" {
		return
	}
	if display.animated {
		fmt.Fprint(display.out, "\r\x1b[2K")
	}
	marker := "OK"
	if !success {
		marker = i18n.Human("Failed")
	}
	fmt.Fprintf(display.out, "[%d] %s ... %s %.1fs\n", display.number, display.title, marker, time.Since(display.start).Seconds())
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
		if display.animated {
			fmt.Fprint(display.out, "\r\x1b[2K")
		}
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
			if display.animated && display.title != "" {
				lines := strings.Split(strings.TrimSpace(display.tail), "\n")
				last := []rune(lines[len(lines)-1])
				if len(last) > 100 {
					last = last[:100]
				}
				fmt.Fprintf(display.out, "\r\x1b[2K%c %s %.1fs  %s", "|/-\\"[frame%4], display.title, time.Since(display.start).Seconds(), string(last))
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
		if !display.verbose && display.tail != "" {
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
			if !display.animated {
				fmt.Fprintln(display.out, text)
			}
			display.mu.Unlock()
		}
	}
	return count, err
}
