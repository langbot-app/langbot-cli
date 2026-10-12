package command

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type schemaEnvelope struct {
	OK    bool             `json:"ok"`
	Data  schemaDocument   `json:"data"`
	Error *schemaTestError `json:"error"`
}

type schemaTestError struct {
	Type string `json:"type"`
}

func TestSchemaIsOfflineStableAndDescribesCommands(t *testing.T) {
	first := executeSchemaForTest(t, "schema")
	second := executeSchemaForTest(t, "schema")
	if first.code != 0 || second.code != 0 {
		t.Fatalf("schema exit codes = %d, %d", first.code, second.code)
	}
	if first.output != second.output {
		t.Fatal("schema output is not stable")
	}
	var envelope schemaEnvelope
	if err := json.Unmarshal([]byte(first.output), &envelope); err != nil {
		t.Fatalf("schema output is not JSON: %v", err)
	}
	if !envelope.OK || envelope.Data.SchemaVersion != 1 || envelope.Data.CLIVersion != "test" {
		t.Fatalf("unexpected schema envelope: %s", first.output)
	}
	if envelope.Data.ExitCodes["input"] != 2 || envelope.Data.ExitCodes["network"] != 7 {
		t.Fatalf("stable exit codes missing: %#v", envelope.Data.ExitCodes)
	}
	commands := indexSchemaCommands(envelope.Data.Commands)
	for _, name := range []string{"bot.create", "bot.delete", "knowledge-base.ingest", "bot.list"} {
		if _, ok := commands[name]; !ok {
			t.Fatalf("schema is missing %s", name)
		}
	}
	create := commands["bot.create"]
	if create.DefaultOutput != "default" || !containsString(create.MachineOutputs, "json") || !containsString(create.MachineOutputs, "yaml") {
		t.Fatalf("resource output contract is inaccurate: %#v", create)
	}
	if !create.Mutating || !create.RequiresConnection || !create.RequiresSavedContext || !create.SupportsDryRun || create.RequiresYes {
		t.Fatalf("unexpected bot.create metadata: %#v", create)
	}
	if !hasFlag(create.Flags, "file", "local", true) || !hasFlag(create.Flags, "dry-run", "local", false) || !hasFlag(create.Flags, "output", "inherited", false) {
		t.Fatalf("bot.create flags are incomplete: %#v", create.Flags)
	}
	outputFlag, ok := findFlag(create.Flags, "output")
	if !ok || outputFlag.Default != nil || !reflect.DeepEqual(outputFlag.Enum, []string{"json", "yaml", "yml"}) {
		t.Fatalf("output flag schema is inaccurate: %#v", outputFlag)
	}
	if len(create.InputConflicts) != 1 || !hasConflictOperand(create.InputConflicts[0].Operands, "api-key-stdin", true) || !hasConflictOperand(create.InputConflicts[0].Operands, "file", "-") {
		t.Fatalf("stdin conflict is missing: %#v", create.InputConflicts)
	}
	deleteCommand := commands["bot.delete"]
	if !deleteCommand.Destructive || !deleteCommand.RequiresYes || !hasFlag(deleteCommand.Flags, "yes", "local", false) {
		t.Fatalf("unexpected bot.delete metadata: %#v", deleteCommand)
	}
	ingest := commands["knowledge-base.ingest"]
	if !ingest.Mutating || !ingest.SupportsDryRun || !ingest.SupportsWait || len(ingest.Operations) < 3 {
		t.Fatalf("knowledge-base.ingest metadata is incomplete: %#v", ingest)
	}
	if len(ingest.InputConflicts) != 1 || !hasConflictOperand(ingest.InputConflicts[0].Operands, "dry-run", true) || !hasConflictOperand(ingest.InputConflicts[0].Operands, "wait", true) {
		t.Fatalf("dry-run and wait conflict is missing: %#v", ingest.InputConflicts)
	}
	if !commands["model.create"].OperationTemplateIsModel() {
		t.Fatalf("model.create must describe its type-dependent operation: %#v", commands["model.create"])
	}
	if !hasOperationRole(create.Operations, "bot.get", "readback") || !hasOperationRole(commands["model.test"].Operations, "model.{type}.get", "precondition") || !hasOperationRole(commands["knowledge-base.file.delete"].Operations, "knowledge_base.file.list", "readback") {
		t.Fatalf("readback operation metadata is incomplete")
	}
	if commands["bot.list"].Mutating || len(commands["bot.list"].Operations) != 1 {
		t.Fatalf("bot.list must be a read operation: %#v", commands["bot.list"])
	}
	for _, name := range []string{"context.check", "capabilities"} {
		if !commands[name].RequiresConnection {
			t.Fatalf("%s must declare a connection requirement", name)
		}
	}
	if !commands["status"].RequiresConnection || !commands["doctor"].RequiresConnection {
		t.Fatalf("status/doctor must resolve an Active Environment: status=%#v doctor=%#v", commands["status"], commands["doctor"])
	}
	for _, name := range []string{"status", "doctor"} {
		for _, operation := range []string{"system.info", "system.context", "system.capabilities"} {
			if !hasOperationID(commands[name].Operations, operation) {
				t.Fatalf("%s is missing discovery operation %s: %#v", name, operation, commands[name].Operations)
			}
		}
		flags := indexSchemaFlags(commands[name].Flags)
		if flags["timeout"].Applicability != nil {
			t.Fatalf("%s must accept the global timeout: %#v", name, flags["timeout"])
		}
		for _, flag := range []string{"config", "context", "endpoint", "api-key-stdin"} {
			if flags[flag].Applicability != nil {
				t.Fatalf("%s must accept Active Environment selector %s: %#v", name, flag, flags[flag])
			}
		}
	}
	if _, exists := commands["deployment"]; exists {
		t.Fatal("schema exposes the removed deployment command group")
	}
	install := commands["install"]
	if !install.Mutating || !install.SupportsDryRun || install.RequiresConnection || !isRejected(install.Flags, "context") || !isRejected(install.Flags, "endpoint") || !isRejected(install.Flags, "api-key-stdin") {
		t.Fatalf("install lifecycle contract is inaccurate: %#v", install)
	}
	adopt := commands["adopt"]
	if !adopt.Mutating || !adopt.RequiresConnection || !hasFlag(adopt.Flags, "endpoint", "inherited", true) || !hasFlag(adopt.Flags, "file", "local", true) || !hasFlag(adopt.Flags, "project", "local", true) || !isRejected(adopt.Flags, "context") {
		t.Fatalf("adopt lifecycle contract is inaccurate: %#v", adopt)
	}
	for _, name := range []string{"start", "stop", "restart"} {
		if !commands[name].Mutating || commands[name].RequiresYes {
			t.Fatalf("%s lifecycle contract is inaccurate: %#v", name, commands[name])
		}
	}
	uninstall := commands["uninstall"]
	if !uninstall.Mutating || !uninstall.Destructive || !uninstall.RequiresYes || !hasFlag(uninstall.Flags, "yes", "local", false) || !hasFlag(uninstall.Flags, "purge-data", "local", false) {
		t.Fatalf("uninstall lifecycle contract is inaccurate: %#v", uninstall)
	}
	logs := commands["logs"]
	if logs.Mutating || len(logs.InputConflicts) != 1 || !hasConflictOperand(logs.InputConflicts[0].Operands, "follow", true) || !hasConflictOperand(logs.InputConflicts[0].Operands, "output", "yaml") {
		t.Fatalf("logs lifecycle contract is inaccurate: %#v", logs)
	}
	if len(logs.ConditionalOutputs) != 2 || logs.ConditionalOutputs[0].Encoding != "text-lines" || logs.ConditionalOutputs[1].Encoding != "json-lines" {
		t.Fatalf("logs follow output contract is missing: %#v", logs.ConditionalOutputs)
	}
	if !hasOperationID(commands["context.check"].Operations, "system.info") || !hasOperationID(commands["context.check"].Operations, "system.context") || hasOperationID(commands["context.check"].Operations, "system.capabilities") {
		t.Fatalf("context.check operation metadata is inaccurate: %#v", commands["context.check"].Operations)
	}
	if !hasOperationID(commands["capabilities"].Operations, "system.context") || !hasOperationID(commands["capabilities"].Operations, "system.capabilities") {
		t.Fatalf("capabilities operation metadata is incomplete: %#v", commands["capabilities"].Operations)
	}
	scan := commands["provider.scan-models"]
	if !scan.SupportsDryRun || !scan.RequiresSavedContext {
		t.Fatalf("provider scan metadata is inaccurate: %#v", scan)
	}
	if !commands["model.test"].Mutating || !commands["model.test"].SupportsDryRun || !commands["model.test"].RequiresSavedContext {
		t.Fatalf("model test metadata is inaccurate: %#v", commands["model.test"])
	}
	if envelope.Data.ExitCodes["server"] != 10 || envelope.Data.ExitCodes["result_unknown"] != 7 || envelope.Data.ExitCodeDescriptions["internal"] == "" {
		t.Fatalf("error exit code definitions are incomplete: %#v %#v", envelope.Data.ExitCodes, envelope.Data.ExitCodeDescriptions)
	}
	for name := range envelope.Data.ExitCodes {
		if envelope.Data.ExitCodeDescriptions[name] == "" {
			t.Fatalf("exit code %s is missing a description", name)
		}
	}
	contextList := commands["context.list"]
	if contextList.RequiresConnection || contextList.RequiresSavedContext || contextList.RequiresWorkspaceBind || !isRejected(contextList.Flags, "api-key-stdin") || !isRejected(contextList.Flags, "endpoint") {
		t.Fatalf("context.list applicability is inaccurate: %#v", contextList)
	}
	version := commands["version"]
	if version.DefaultOutput != "default" {
		t.Fatalf("version default output = %q, want default", version.DefaultOutput)
	}
	if !isConditional(version.Flags, "endpoint", "server", true) || !isConditional(version.Flags, "api-key-stdin", "server", true) {
		t.Fatalf("version applicability is inaccurate: %#v", version)
	}
	if commands["schema"].DefaultOutput != "json" || commands["schema"].OutputEnvelope != "result-envelope" {
		t.Fatalf("schema output contract is inaccurate: %#v", commands["schema"])
	}
	completion := commands["completion.bash"]
	if completion.DefaultOutput != "shell" || len(completion.MachineOutputs) != 0 || completion.OutputEnvelope != "" {
		t.Fatalf("completion output contract is inaccurate: %#v", completion)
	}
	if !isRejected(commands["raw"].Flags, "file") {
		t.Fatalf("raw file flag must be rejected: %#v", commands["raw"].Flags)
	}
	update := commands["context.update"]
	if len(update.InputConflicts) != 2 || !hasConflictOperand(update.InputConflicts[0].Operands, "clear-credential", true) || !hasConflictOperand(update.InputConflicts[1].Operands, "clear-binding", true) {
		t.Fatalf("context.update conflicts are incomplete: %#v", update.InputConflicts)
	}
	check := commands["context.check"]
	if len(check.InputConflicts) != 4 || !hasConflictOperand(check.InputConflicts[0].Operands, "all", true) || !hasConflictArgument(check.InputConflicts[3].Operands, "name") {
		t.Fatalf("context.check conflicts are incomplete: %#v", check.InputConflicts)
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func TestSchemaCommandFilterAndUnknownCommand(t *testing.T) {
	filtered := executeSchemaForTest(t, "schema", "--command", "bot.create")
	if filtered.code != 0 {
		t.Fatalf("filtered schema exit code = %d: %s", filtered.code, filtered.output)
	}
	var envelope schemaEnvelope
	if err := json.Unmarshal([]byte(filtered.output), &envelope); err != nil {
		t.Fatalf("filtered schema is not JSON: %v", err)
	}
	if len(envelope.Data.Commands) != 1 || envelope.Data.Commands[0].Name != "bot.create" {
		t.Fatalf("unexpected filtered commands: %#v", envelope.Data.Commands)
	}

	unknown := executeSchemaForTest(t, "schema", "--command", "bot.missing")
	if unknown.code != 2 {
		t.Fatalf("unknown schema command exit code = %d, want 2", unknown.code)
	}
	if unknown.output == "" || !strings.Contains(unknown.output, `"type": "input"`) {
		t.Fatalf("unknown schema command did not return input error: %s", unknown.output)
	}
}

func TestSchemaSupportsGlobalOutputFormat(t *testing.T) {
	run := executeSchemaForTest(t, "--output", "yaml", "schema", "--command", "bot.list")
	if run.code != 0 || !strings.Contains(run.output, "schema_version: 1\n") || !strings.Contains(run.output, "ok: true\n") {
		t.Fatalf("unexpected YAML schema output: code=%d output=%s", run.code, run.output)
	}
}

type schemaExecution struct {
	code   int
	output string
}

func executeSchemaForTest(t *testing.T, args ...string) schemaExecution {
	t.Helper()
	var out, diagnostic bytes.Buffer
	code := Execute(context.Background(), args, Dependencies{
		In: &panicReader{}, Out: &out, Err: &diagnostic,
		LookupEnv:         func(string) (string, bool) { panic("schema read environment") },
		DefaultConfigPath: func() (string, error) { panic("schema read config") },
		Transport:         panicRoundTripper{}, Version: "test", Commit: "test", BuildDate: "test",
	})
	if diagnostic.Len() != 0 {
		t.Fatalf("schema wrote diagnostics: %s", diagnostic.String())
	}
	return schemaExecution{code: code, output: out.String()}
}

type panicReader struct{}

func (*panicReader) Read([]byte) (int, error) { panic("schema read stdin") }

type panicRoundTripper struct{}

func (panicRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	panic("schema performed network request")
}

func indexSchemaCommands(commands []schemaCommand) map[string]schemaCommand {
	indexed := make(map[string]schemaCommand, len(commands))
	for _, command := range commands {
		indexed[command.Name] = command
	}
	return indexed
}

func hasFlag(flags []schemaFlag, name, scope string, required bool) bool {
	for _, flag := range flags {
		if flag.Name == name && flag.Scope == scope && flag.Required == required {
			return true
		}
	}
	return false
}

func indexSchemaFlags(flags []schemaFlag) map[string]schemaFlag {
	indexed := make(map[string]schemaFlag, len(flags))
	for _, flag := range flags {
		indexed[flag.Name] = flag
	}
	return indexed
}

func findFlag(flags []schemaFlag, name string) (schemaFlag, bool) {
	for _, flag := range flags {
		if flag.Name == name {
			return flag, true
		}
	}
	return schemaFlag{}, false
}

func isRejected(flags []schemaFlag, name string) bool {
	for _, flag := range flags {
		if flag.Name == name && flag.Applicability != nil && flag.Applicability.Status == "rejected" {
			return true
		}
	}
	return false
}

func isConditional(flags []schemaFlag, name, conditionFlag string, conditionValue any) bool {
	for _, flag := range flags {
		if flag.Name == name && flag.Applicability != nil && flag.Applicability.Status == "conditional" && flag.Applicability.When != nil && flag.Applicability.When.Flag == conditionFlag && flag.Applicability.When.Equals == conditionValue {
			return true
		}
	}
	return false
}

func hasConflictOperand(operands []schemaOperand, name string, equals any) bool {
	for _, operand := range operands {
		if operand.Flag == name && operand.Equals == equals {
			return true
		}
	}
	return false
}

func hasConflictArgument(operands []schemaOperand, name string) bool {
	for _, operand := range operands {
		if operand.Argument == name && operand.Present {
			return true
		}
	}
	return false
}

func hasOperationRole(operations []schemaOperation, expected, role string) bool {
	for _, operation := range operations {
		if operation.Role == role && (operation.ID == expected || operation.Template == expected) {
			return true
		}
	}
	return false
}

func (command schemaCommand) OperationTemplateIsModel() bool {
	for _, operation := range command.Operations {
		if operation.Template == "model.{type}.create" && operation.When != nil && operation.When.Flag == "type" && operation.Role == "" {
			return true
		}
	}
	return false
}

func hasOperationID(operations []schemaOperation, expected string) bool {
	for _, operation := range operations {
		if operation.ID == expected {
			return true
		}
	}
	return false
}

var _ io.Reader = (*panicReader)(nil)
