package i18n

import "golang.org/x/sys/windows"

func systemLanguage() string {
	languages, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err == nil && len(languages) > 0 {
		return languages[0]
	}
	language, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage").Call()
	if language&0x3ff == 4 {
		return "zh"
	}
	return "en"
}
