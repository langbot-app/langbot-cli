package telemetry

import (
	"context"
	"testing"
)

func TestPrivateCommandDoesNotRecordCredentials(t *testing.T) {
	operation := &Operation{}
	ctx := context.WithValue(context.Background(), operationKey{}, operation)
	RecordCommand(PrivateCommand(ctx), []string{"python", "-c", "read-config"}, 1, "custom-private-key", "custom-private-key")
	if operation.process != nil {
		t.Fatal("credential helper output was recorded")
	}
}
