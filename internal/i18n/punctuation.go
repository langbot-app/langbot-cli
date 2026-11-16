package i18n

import "strings"

var punctuation = strings.NewReplacer("：", ": ", "（", " (", "）", ")", "；", "; ", "、", ", ", "。", ".")

func Punct(value string) string {
	if selected := language.Load(); selected != nil && !selected.machine && selected.language == English {
		return punctuation.Replace(value)
	}
	return value
}
