package diagnostic

import (
	"encoding/json"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const FieldLimit = 8 << 10
const EventLimit = 32 << 10

var urls = regexp.MustCompile("(?i)https?://[^\\s<>\"'`]+")
var homes = regexp.MustCompile(`(?i)((?:[a-z]:[\\/]+Users[\\/]+)|/(?:home|Users)/)[^\\/\s<>"']+`)
var spacedHomes = regexp.MustCompile(`(?i)((?:[a-z]:[\\/]+Users[\\/]+)|/(?:home|Users)/)[^\\/\r\n<>"']+([\\/])`)
var ipv4 = regexp.MustCompile(`\b[0-9]{1,3}(?:\.[0-9]{1,3}){3}\b`)
var ipv6 = regexp.MustCompile(`[a-fA-F0-9:]*:[a-fA-F0-9:]+(?:%[a-zA-Z0-9]+)?`)
var sensitive = regexp.MustCompile(`(?i)(?:token|password|passwd|secret|api[_-]?key|authorization|credential|username|^user$)`)
var assignments = regexp.MustCompile(`(?im)((?:["']?[a-z0-9_.-]*(?:token|password|passwd|secret|api[_-]?key|credential)[a-z0-9_.-]*["']?|["']?(?:authorization|username|user)["']?)[ \t]*[:=][ \t]*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;&\x60]+)`)
var secretFlags = regexp.MustCompile(`(?i)(--?[a-z0-9_-]*(?:token|password|passwd|secret|api[_-]?key|credential)[a-z0-9_-]*[ \t]+)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;]+)`)
var yamlBlock = regexp.MustCompile(`^([ \t]*)(["']?[a-zA-Z0-9_.-]+["']?)[ \t]*:[ \t]*[|>][+-]?[ \t]*$`)
var secrets = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[a-z0-9._~+/=\-]+`),
	regexp.MustCompile(`\b(?:sk-|ghp_|github_pat_|lbg_)[a-zA-Z0-9_\-]+`),
	regexp.MustCompile(`\beyJ[a-zA-Z0-9_\-]+\.[a-zA-Z0-9_\-]+\.[a-zA-Z0-9_\-]+`),
}

func privateIP(candidate string) string {
	address, _, _ := strings.Cut(candidate, "%")
	parsed := net.ParseIP(address)
	if parsed != nil && !parsed.IsPrivate() && !parsed.IsLoopback() && !parsed.IsLinkLocalUnicast() && !parsed.IsUnspecified() {
		return "[redacted]"
	}
	return candidate
}

func Text(value string) string {
	value = strings.ToValidUTF8(value, "")
	lines := strings.Split(value, "\n")
	blockIndent := -1
	for index, line := range lines {
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if blockIndent >= 0 && (indent > blockIndent || strings.TrimSpace(line) == "") {
			lines[index] = line[:indent] + "[redacted]"
			continue
		}
		blockIndent = -1
		if match := yamlBlock.FindStringSubmatch(line); len(match) > 0 && sensitive.MatchString(match[2]) {
			blockIndent = len(match[1])
		}
	}
	value = strings.Join(lines, "\n")
	value = urls.ReplaceAllStringFunc(value, func(raw string) string {
		parsed, err := url.Parse(raw)
		if err != nil {
			return "[redacted]"
		}
		parsed.User = nil
		parsed.Fragment = ""
		if parsed.RawQuery != "" {
			parts := strings.Split(parsed.RawQuery, "&")
			for index, part := range parts {
				key, _, _ := strings.Cut(part, "=")
				parts[index] = key + "=[redacted]"
			}
			parsed.RawQuery = strings.Join(parts, "&")
		}
		return parsed.String()
	})
	value = spacedHomes.ReplaceAllString(value, "${1}<user>${2}")
	value = homes.ReplaceAllString(value, "${1}<user>")
	for _, pattern := range secrets {
		value = pattern.ReplaceAllString(value, "[redacted]")
	}
	value = assignments.ReplaceAllString(value, "${1}[redacted]")
	value = secretFlags.ReplaceAllString(value, "${1}[redacted]")
	value = ipv4.ReplaceAllStringFunc(value, privateIP)
	return ipv6.ReplaceAllStringFunc(value, privateIP)
}

func Tail(value string, lines, limit int) string {
	value = strings.ToValidUTF8(value, "")
	parts := strings.Split(strings.TrimRight(value, "\r\n"), "\n")
	if lines > 0 && len(parts) > lines {
		value = strings.Join(parts[len(parts)-lines:], "\n")
	}
	if len(value) > limit {
		value = value[len(value)-limit:]
		if _, tail, found := strings.Cut(value, "\n"); found {
			value = tail
		}
		for !utf8.ValidString(value) {
			value = value[1:]
		}
	}
	return value
}

func Args(args []string) []string {
	result := make([]string, len(args))
	redactNext := false
	for index, arg := range args {
		if redactNext {
			result[index] = "[redacted]"
			redactNext = false
			continue
		}
		result[index] = Tail(Text(arg), 0, FieldLimit)
		redactNext = strings.HasPrefix(arg, "-") && !strings.Contains(arg, "=") && sensitive.MatchString(arg)
	}
	return result
}

func Object(object map[string]any) map[string]any {
	if object == nil {
		return nil
	}
	result := make(map[string]any, len(object))
	for key, value := range object {
		cleanKey := Tail(Text(key), 0, 128)
		if sensitive.MatchString(key) {
			result[cleanKey] = "[redacted]"
		} else {
			result[cleanKey] = cleanValue(value)
		}
	}
	return result
}

func cleanValue(value any) any {
	switch typed := value.(type) {
	case string:
		return Tail(Text(typed), 0, FieldLimit)
	case map[string]any:
		return Object(typed)
	case []string:
		args := Args(typed)
		result := make([]any, len(args))
		for index, arg := range args {
			result[index] = arg
		}
		return result
	case []any:
		result := make([]any, len(typed))
		redactNext := false
		for index, item := range typed {
			if redactNext {
				result[index] = "[redacted]"
				redactNext = false
				continue
			}
			result[index] = cleanValue(item)
			if arg, ok := item.(string); ok {
				redactNext = strings.HasPrefix(arg, "-") && !strings.Contains(arg, "=") && sensitive.MatchString(arg)
			}
		}
		return result
	default:
		return value
	}
}

func Fit(object map[string]any, limit int) {
	for {
		body, err := json.Marshal(object)
		if err != nil || len(body) <= limit || len(object) == 0 {
			return
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		largest, size := "", -1
		for _, key := range keys {
			encoded, _ := json.Marshal(object[key])
			if len(encoded) > size {
				largest, size = key, len(encoded)
			}
		}
		switch value := object[largest].(type) {
		case string:
			if len(value) > 32 {
				object[largest] = Tail(value, 0, len(value)/2)
			} else {
				delete(object, largest)
			}
		case map[string]any:
			Fit(value, size/2)
			if len(value) == 0 {
				delete(object, largest)
			}
		case []any:
			if len(value) > 1 {
				object[largest] = value[:len(value)/2]
			} else {
				delete(object, largest)
			}
		default:
			delete(object, largest)
		}
	}
}
