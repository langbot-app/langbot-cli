package result

import "regexp"

// AuthGuidance contains setup instructions, never a credential value.
type AuthGuidance struct {
	EnvironmentVariable string            `json:"environment_variable,omitempty" yaml:"environment_variable,omitempty"`
	Steps               []string          `json:"steps" yaml:"steps"`
	Examples            map[string]string `json:"examples,omitempty" yaml:"examples,omitempty"`
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func AuthSetupError(reason, variable string) *Error {
	message := "尚未提供 API Key"
	switch reason {
	case "environment_variable_unavailable":
		message = "API Key 环境变量未设置或为空"
	case "empty_stdin":
		message = "stdin 中的 API Key 为空"
	case "endpoint_changed":
		message = "endpoint 已覆盖，必须显式提供新的 API Key"
	}
	err := New("auth", message)
	AddAuthGuidance(err, reason, variable)
	return err
}

// AddAuthGuidance preserves the error type, status and exit-code contract.
func AddAuthGuidance(err *Error, reason, variable string) {
	if err == nil || err.Kind != "auth" {
		return
	}
	err.Reason = reason
	verify := "配置后，对同一目标运行 lbctl whoami；保留原命令的 --config、--context、--endpoint 等连接选项。"
	if reason == "authentication_rejected" {
		err.Guidance = &AuthGuidance{Steps: []string{
			"服务端拒绝了已提供的 API Key；检查目标地址是否正确，以及 Key 是否填错、过期或已吊销。",
			"如需更换 Key，请在目标 LangBot 管理页面的 API Key 管理中创建 Workspace API Key；通过原凭据来源提供，不要打印或记录 Key。",
			verify,
		}}
		if environmentName.MatchString(variable) {
			err.Guidance.EnvironmentVariable = variable
		}
		return
	}
	if !environmentName.MatchString(variable) {
		variable = "LANGBOT_API_KEY"
	}
	err.Guidance = &AuthGuidance{
		EnvironmentVariable: variable,
		Steps: []string{
			"打开目标 LangBot 管理页面，完成首次初始化后，在 API Key 管理中创建 Workspace API Key。",
			"在运行 lbctl 的终端设置环境变量 " + variable + "；以下示例使用隐藏输入，不将 Key 写入命令历史。也可显式使用 --api-key-stdin。",
			verify,
		},
		Examples: map[string]string{
			"powershell": "$env:" + variable + " = [System.Net.NetworkCredential]::new('', (Read-Host 'LangBot API Key' -AsSecureString)).Password",
			"bash":       "read -r -s -p 'LangBot API Key: ' " + variable + "; echo; export " + variable,
		},
	}
}
