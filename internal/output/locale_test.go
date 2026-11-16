package output

import (
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"os"
	"testing"
)

func TestMain(tests *testing.M) {
	i18n.Configure(i18n.Chinese, false)
	os.Exit(tests.Run())
}
