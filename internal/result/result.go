package result

import "errors"

type Error struct {
	Kind       string `json:"-" yaml:"-"`
	Type       string `json:"type" yaml:"type"`
	Message    string `json:"message" yaml:"message"`
	HTTPStatus int    `json:"http_status,omitempty" yaml:"http_status,omitempty"`
	ServerCode any    `json:"server_code,omitempty" yaml:"server_code,omitempty"`
	RequestID  string `json:"request_id,omitempty" yaml:"request_id,omitempty"`
}

func (e *Error) Error() string { return e.Message }

func New(kind, message string) *Error {
	return &Error{Kind: kind, Type: kind, Message: message}
}

func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed
	}
	// 未分类错误可能包含请求或凭据，不直接呈现底层文本。
	return New("server", "操作失败，发生未分类的内部错误")
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	switch AsError(err).Kind {
	case "input":
		return 2
	case "auth":
		return 3
	case "permission":
		return 4
	case "not_found":
		return 5
	case "precondition":
		return 6
	case "network":
		return 7
	case "incompatible":
		return 8
	default:
		return 10
	}
}

type Envelope struct {
	OK    bool   `json:"ok" yaml:"ok"`
	Data  any    `json:"data,omitempty" yaml:"data,omitempty"`
	Error *Error `json:"error,omitempty" yaml:"error,omitempty"`
	Meta  any    `json:"meta,omitempty" yaml:"meta,omitempty"`
}
