package i18n

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func FirstRun(home string, out io.Writer, lang string) error {
	directory := filepath.Join(home, ".langbot")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(directory, "terms-notice"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	message := "By continuing to use lbctl, you agree to the LangBot Terms of Service: https://langbot.app/terms\nAnonymous operation telemetry includes redacted diagnostic information on failure; disable with LANGBOT_TELEMETRY=0 or DO_NOT_TRACK=1.\n"
	if lang == Chinese {
		message = "继续使用 lbctl 即表示你同意 LangBot 服务条款：https://langbot.app/terms\n默认启用匿名操作遥测，失败时会上报脱敏后的诊断信息；可使用 LANGBOT_TELEMETRY=0 或 DO_NOT_TRACK=1 关闭。\n"
	}
	_, err = fmt.Fprint(out, message)
	if err != nil {
		file.Close()
		os.Remove(file.Name())
	}
	return err
}
