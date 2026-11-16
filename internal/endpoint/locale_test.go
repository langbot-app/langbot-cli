package endpoint

import (
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"os"
	"testing"
)

func TestMain(tests *testing.M) {
	i18n.Configure(i18n.Chinese, true)
	os.Exit(tests.Run())
}
