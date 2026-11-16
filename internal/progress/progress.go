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
	title, tail       string
	number            int
	start             time.Time
	done, exited      chan struct{}
	once              sync.Once
}

func New(out io.Writer, lookup func(string) (string, bool), machine, verbose bool) *Display {
	display := &Display{out: out, verbose: verbose, machine: machine, done: make(chan struct{}), exited: make(chan struct{})}
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
	if title == display.title {
		return
	}
	display.complete(true)
	display.number++
	display.title, display.tail, display.start = title, "", time.Now()
	if !display.carriage {
		fmt.Fprintf(display.out, "[%d] %s ...\n", display.number, title)
	}
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
	marker := "OK"
	if !success {
		marker = i18n.Human("Failed")
	}
	if display.animated {
		marker = "\x1b[32m✓\x1b[0m"
		if !success {
			marker = "\x1b[31m✗\x1b[0m"
		}
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
			if display.carriage && display.title != "" {
				lines := strings.Split(strings.TrimSpace(display.tail), "\n")
				display.clear()
				width := terminalWidth(display.out) - 1
				title := fit(fmt.Sprintf("%c [%d] %s  %.1fs", "|/-\\"[frame%4], display.number, display.title, time.Since(display.start).Seconds()), width)
				if display.animated {
					fmt.Fprintf(display.out, "\x1b[36m%s\x1b[0m\n\x1b[2m%s\x1b[0m", title, fit(lines[len(lines)-1], width))
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
