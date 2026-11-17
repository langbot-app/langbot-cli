package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const purgeDockerData = `import os, shutil
def fail(error):
    raise error
for root, directories, files in os.walk('/data', onerror=fail):
    for name in directories + files:
        path = os.path.join(root, name)
        if os.path.islink(path):
            raise ValueError('Data directory contains symbolic links: ' + path)
for name in os.listdir('/data'):
    path = os.path.join('/data', name)
    if os.path.isdir(path):
        shutil.rmtree(path)
    else:
        os.unlink(path)
`

func PurgeDockerData(ctx context.Context, runner Runner, dir string, record Record) (bool, string, error) {
	purged, recoveryPath, err := PurgeData(dir, record)
	if err == nil || !errors.Is(err, os.ErrPermission) {
		return purged, recoveryPath, err
	}
	if recoveryPath == "" {
		recoveryPath = record.DataDir
	}
	image, err := dockerBackupImage(record)
	if err != nil {
		return false, recoveryPath, err
	}
	target, err := filepath.Abs(recoveryPath)
	if err != nil {
		return false, recoveryPath, err
	}
	output, err := runner.Docker(ctx, "run", "--rm", "--network", "none", "--user", "0:0", "--mount", "type=bind,src="+target+",dst=/data", "--entrypoint", "python", image, "-c", purgeDockerData)
	if err != nil {
		return false, recoveryPath, fmt.Errorf("%w: %s", err, lastLine(output.Stderr))
	}
	if err := os.Remove(target); err != nil {
		return false, recoveryPath, err
	}
	return true, "", nil
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
