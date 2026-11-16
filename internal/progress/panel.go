package progress

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"github.com/langbot-app/langbot-cli/internal/i18n"
	"golang.org/x/term"
)

func cellWidth(char rune) int {
	if unicode.Is(unicode.Mn, char) || char == 0x200d || char < 32 || char >= 0x7f && char < 0xa0 {
		return 0
	}
	if char >= 0x1100 && (char <= 0x115f || char >= 0x2e80 && char <= 0xa4cf || char >= 0xac00 && char <= 0xd7a3 || char >= 0xf900 && char <= 0xfaff || char >= 0xfe10 && char <= 0xfe6f || char >= 0xff01 && char <= 0xff60 || char >= 0xffe0 && char <= 0xffe6 || char >= 0x1f300 && char <= 0x1faff || char >= 0x20000 && char <= 0x3ffff) {
		return 2
	}
	return 1
}

func Width(text string) int {
	width := 0
	for _, char := range clean(text) {
		width += cellWidth(char)
	}
	return width
}

func fit(text string, width int) string {
	var result strings.Builder
	for _, char := range clean(text) {
		if unicode.IsControl(char) {
			char = ' '
		}
		width -= cellWidth(char)
		if width < 0 {
			break
		}
		result.WriteRune(char)
	}
	return result.String()
}

func terminalWidth(out io.Writer) int {
	if file, ok := out.(*os.File); ok {
		if width, _, err := term.GetSize(int(file.Fd())); err == nil && width > 0 {
			return width
		}
	}
	return 80
}

func styled(out io.Writer) bool {
	file, ok := out.(*os.File)
	_, noColor := os.LookupEnv("NO_COLOR")
	return ok && !noColor && os.Getenv("TERM") != "dumb" && term.IsTerminal(int(file.Fd())) && enableVT(file)
}

func Panel(out io.Writer, lines []string) error {
	return renderPanel(out, lines, terminalWidth(out), styled(out))
}

func renderPanel(out io.Writer, lines []string, width int, color bool) error {
	if !color || width < 60 || len(lines) == 0 {
		_, err := fmt.Fprintln(out, strings.Join(lines, "\n"))
		return err
	}
	width = min(width-2, 76)
	title := fit(lines[0], width-6)
	if _, err := fmt.Fprintln(out, "\x1b[36m╭─ \x1b[1m"+title+"\x1b[22m "+strings.Repeat("─", width-5-Width(title))+"╮\x1b[0m"); err != nil {
		return err
	}
	for _, line := range append([]string{""}, lines[1:]...) {
		for {
			part := fit(line, width-6)
			cells := Width(part)
			color := ""
			if strings.HasPrefix(line, "WebUI") {
				color = "\x1b[1;36m"
			} else if line == i18n.Human("Next steps") {
				color = "\x1b[1m"
			} else if strings.HasPrefix(strings.TrimSpace(line), "docs.langbot.app") {
				color = "\x1b[2m"
			}
			if _, err := fmt.Fprintf(out, "\x1b[36m│\x1b[0m  %s%s\x1b[0m%s  \x1b[36m│\x1b[0m\n", color, part, strings.Repeat(" ", width-6-cells)); err != nil {
				return err
			}
			if len(part) >= len(line) || part == "" {
				break
			}
			line = line[len(part):]
		}
	}
	_, err := fmt.Fprintln(out, "\x1b[36m╰"+strings.Repeat("─", width-2)+"╯\x1b[0m")
	return err
}

func BrandLines() []string {
	return []string{"LangBot  " + i18n.Human("Production-grade AI messaging bot platform · Open source"), i18n.Human("Connect LLMs, agents, knowledge bases and plugins to QQ, WeChat/WeCom, Feishu, DingTalk, Telegram, Discord, Slack and more"), i18n.Human("Website langbot.app · Docs docs.langbot.app · Plugins space.langbot.app")}
}

func Brand(out io.Writer) {
	for index, line := range BrandLines() {
		for _, part := range wrap(line, terminalWidth(out)-4) {
			if index == 0 && styled(out) {
				part = strings.Replace(part, "LangBot", "\x1b[1;36mLangBot\x1b[0m", 1)
			} else if index > 0 && styled(out) {
				part = "\x1b[2m" + part + "\x1b[0m"
			}
			fmt.Fprintln(out, "  "+part)
		}
	}
	fmt.Fprintln(out)
}

func Dim(out io.Writer, text string) {
	for _, line := range wrap(text, terminalWidth(out)-4) {
		if styled(out) {
			line = "\x1b[2m" + line + "\x1b[0m"
		}
		fmt.Fprintln(out, "  "+line)
	}
}

func wrap(text string, width int) []string {
	var lines []string
	for text != "" {
		part := fit(text, max(1, width))
		if len(part) < len(text) {
			if space := strings.LastIndexByte(part, ' '); space > 0 && Width(part[:space]) >= width/2 {
				part = part[:space]
			}
		}
		if part == "" {
			break
		}
		lines = append(lines, part)
		text = strings.TrimLeft(text[len(part):], " ")
	}
	return lines
}

func (display *Display) Info(lines []string) {
	display.mu.Lock()
	defer display.mu.Unlock()
	if display.machine {
		return
	}
	display.clear()
	for _, line := range lines {
		for _, part := range wrap(line, terminalWidth(display.out)-4) {
			fmt.Fprintln(display.out, "  "+part)
		}
	}
	fmt.Fprintln(display.out)
}

func Info(ctx context.Context, lines []string) {
	if display, ok := ctx.Value(contextKey{}).(*Display); ok {
		display.Info(lines)
	}
}
