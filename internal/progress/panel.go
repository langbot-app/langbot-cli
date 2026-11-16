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
	if unicode.Is(unicode.Mn, char) {
		return 0
	}
	if char >= 0x1100 && (char <= 0x115f || char >= 0x2e80 && char <= 0xa4cf || char >= 0xac00 && char <= 0xd7a3 || char >= 0xf900 && char <= 0xfaff || char >= 0xfe10 && char <= 0xfe6f || char >= 0xff01 && char <= 0xff60 || char >= 0x1f300) {
		return 2
	}
	return 1
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
	width := terminalWidth(out)
	if !styled(out) || width < 60 {
		_, err := fmt.Fprintln(out, strings.Join(lines, "\n"))
		return err
	}
	width = min(width-1, 110)
	if _, err := fmt.Fprintln(out, "\x1b[36m╭"+strings.Repeat("─", width-2)+"╮\x1b[0m"); err != nil {
		return err
	}
	for index, line := range lines {
		for {
			part := fit(line, width-4)
			cells := 0
			for _, char := range part {
				cells += cellWidth(char)
			}
			color := ""
			if strings.HasPrefix(line, i18n.Human("Deployment")+":") {
				if strings.Contains(line, i18n.Human("Running")) {
					color = "\x1b[32m"
				} else {
					color = "\x1b[31m"
				}
			}
			if index == 0 {
				color = "\x1b[1;36m"
			}
			if _, err := fmt.Fprintf(out, "\x1b[36m│\x1b[0m %s%s\x1b[0m%s \x1b[36m│\x1b[0m\n", color, part, strings.Repeat(" ", width-4-cells)); err != nil {
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
	return []string{i18n.Human("LangBot · Production-grade platform for building agentic IM bots (open source)"), i18n.Human("Connect LLMs, agents, knowledge bases and plugins to QQ, WeChat/WeCom, Feishu, DingTalk, Telegram, Discord, Slack and more"), i18n.Human("Website https://langbot.app · Docs https://docs.langbot.app · Plugins https://space.langbot.app")}
}

func (display *Display) Info(lines []string) {
	display.mu.Lock()
	defer display.mu.Unlock()
	if display.machine {
		return
	}
	display.clear()
	for index, line := range lines {
		if display.animated && index == 0 {
			fmt.Fprintln(display.out, "\x1b[1;36m"+line+"\x1b[0m")
		} else {
			fmt.Fprintln(display.out, line)
		}
	}
}

func Info(ctx context.Context, lines []string) {
	if display, ok := ctx.Value(contextKey{}).(*Display); ok {
		display.Info(lines)
	}
}
