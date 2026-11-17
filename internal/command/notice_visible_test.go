package command

import (
	"os"
	"testing"
)

func TestNoticeHiddenForHelpAndRedirectedStderr(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	if noticeVisible(Dependencies{Err: devnull}, nil, []string{"install"}) {
		t.Fatal("notice must not be consumed when stderr is not a terminal")
	}
	if noticeVisible(Dependencies{}, nil, []string{"install", "--help"}) {
		t.Fatal("notice must not be consumed by --help")
	}
	if !noticeVisible(Dependencies{}, nil, []string{"version"}) {
		t.Fatal("notice should be shown for regular commands")
	}
}
