package command

import "github.com/langbot-app/langbot-cli/internal/i18n"

import "strings"

func init() {
	schemaMetaByName["auth.show-key"] = schemaCommandMeta{RequiresYes: true, ConditionalOutputs: []schemaConditionalOutput{{When: flagEquals("yes", true), Format: "default", Encoding: "text-lines"}}}
	schemaMetaByName["auth.login"] = schemaCommandMeta{Mutating: true, RequiresConnection: true, RequiresSavedContext: true, RequiredFlags: requiredFlagSet("email")}
	schemaMetaByName["auth.set-token"] = schemaCommandMeta{Mutating: true, RequiresSavedContext: true, RequiredFlags: requiredFlagSet("stdin")}
	schemaMetaByName["auth.logout"] = schemaCommandMeta{Mutating: true, RequiresSavedContext: true}
	schemaMetaByName["file.image.get"] = readMeta(operationIDs("file.image.get")...)
	for path, identifier := range uploadOperations {
		schemaMetaByName[strings.ReplaceAll(path, " ", ".")] = writeMeta(operationIDs(identifier), schemaWriteOptions{RequiresYes: identifier != "skill.zip.preview", RequiredFlags: []string{"file"}})
	}
	schemaMetaByName["knowledge-base.migration.preview"] = readMeta(operationIDs("knowledge.migration.preview")...)
	schemaMetaByName["knowledge-base.migration.execute"] = writeMeta(operationIDs("knowledge.migration.execute"), schemaWriteOptions{RequiresYes: true})
	for _, spec := range operationCommands() {
		name := strings.ReplaceAll(spec.path, " ", ".")
		meta := readMeta(operationIDs(spec.id)...)
		details := friendlyOperationDetails[spec.id]
		meta.Operations = append(meta.Operations, preconditionID("operation.invoke"))
		meta.InputConflicts = []schemaConflict{{Operands: []schemaOperand{{Flag: "file", Equals: "-"}, {Flag: "api-key-stdin", Equals: true}}, Reason: i18n.T("stdin can only have one reader")}}
		if spec.stream {
			meta.Operations = append(meta.Operations, preconditionID("operation.stream"))
		}
		if details.sideEffect || details.risk == "write" || details.risk == "high" || details.risk == "dangerous" {
			meta.Mutating, meta.RequiresYes, meta.RequiresSavedContext, meta.RequiresWorkspace = true, true, true, true
		}
		schemaMetaByName[name] = meta
	}
	for _, name := range []string{"op.list", "op.describe", "op.call", "op.stream"} {
		meta := readMeta(operationIDs("operation.invoke")...)
		if name == "op.stream" {
			meta.RequiredFlags = map[string]bool{"file": true}
		}
		schemaMetaByName[name] = meta
	}
	schemaMetaByName["plugin.permissions.confirm"] = writeMeta(operationIDs("plugin.permissions.get", "plugin.permissions.update"), schemaWriteOptions{RequiresYes: true})
	meta := schemaMetaByName["plugin.install.marketplace"]
	meta.RequiredFlags = nil
	meta.ArgumentAllowed = []int{0, 2}
	schemaMetaByName["plugin.install.marketplace"] = meta
}
