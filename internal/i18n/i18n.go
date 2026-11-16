package i18n

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

const English = "en"
const Chinese = "zh-Hans"

type selection struct {
	language string
	machine  bool
}

var language atomic.Pointer[selection]
var session sync.Mutex
var changed = sync.NewCond(&session)
var active int
var previous *selection

func Normalize(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "zh" || strings.HasPrefix(value, "zh-") || strings.HasPrefix(value, "zh_") {
		return Chinese
	}
	return English
}

func Detect(explicit string, lookup func(string) (string, bool), system func() string) string {
	if meaningfulLocale(explicit) {
		return Normalize(explicit)
	}
	if lookup == nil {
		lookup = os.LookupEnv
	}
	for _, key := range []string{"LBCTL_LANG", "LANGBOT_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
		if value, _ := lookup(key); meaningfulLocale(value) {
			return Normalize(value)
		}
	}
	if system != nil {
		return Normalize(system())
	}
	return English
}

func meaningfulLocale(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	value, _, _ = strings.Cut(value, ".")
	value, _, _ = strings.Cut(value, "@")
	return value != "" && value != "c" && value != "posix"
}

func SystemLanguage() string { return systemLanguage() }

func Current() string {
	if value := language.Load(); value != nil {
		return value.language
	}
	return English
}

func Use(value string) func() {
	return UseMode(value, false)
}

func UseMode(value string, machine bool) func() {
	session.Lock()
	for active > 0 {
		current := language.Load()
		if current.language == value && current.machine == machine {
			break
		}
		changed.Wait()
	}
	if active == 0 {
		previous = language.Load()
		Configure(value, machine)
	}
	active++
	session.Unlock()
	return func() {
		session.Lock()
		active--
		if active == 0 {
			language.Store(previous)
			changed.Broadcast()
		}
		session.Unlock()
	}
}

func T(key string) string {
	if original, ok := english[key]; ok {
		key = original
	}
	if value := language.Load(); value != nil && value.machine {
		return Source(key)
	}
	return Human(key)
}

func Human(key string) string {
	if original, ok := english[key]; ok {
		key = original
	}
	if Current() == Chinese {
		if value, ok := chinese[key]; ok && value != "" {
			return value
		}
	}
	return key
}

func Configure(lang string, machine bool) { language.Store(&selection{lang, machine}) }

func Source(key string) string {
	if value, ok := originals[key]; ok {
		return value
	}
	return key
}

var originals = func() map[string]string {
	values := make(map[string]string, len(chinese))
	for key, value := range chinese {
		values[key] = value
	}
	for key, value := range map[string]string{
		"Autostart enabled": "已开启开机/登录自启 / Autostart enabled", "Autostart disabled": "自启未开启 / Autostart disabled",
		"Instance name ": "本机实例名称 / Instance name", "Instance directory": "本机部署目录 / Instance directory",
		"Manage startup": "管理开机/登录自启 / Manage startup", "Autostart ": "自启 / Autostart ", "Disable: %v\n": "关闭 / Disable: %v\n",
	} {
		values[key] = value
	}
	return values
}()

var english = func() map[string]string {
	values := make(map[string]string, len(chinese))
	for key, value := range chinese {
		values[value] = key
	}
	return values
}()

type messageError struct{ key string }

func (message *messageError) Error() string { return T(message.key) }
func Error(key string) error                { return &messageError{key} }
