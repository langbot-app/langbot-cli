package command

import "strings"

func init() {
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
		meta.InputConflicts = []schemaConflict{{Operands: []schemaOperand{{Flag: "file", Equals: "-"}, {Flag: "api-key-stdin", Equals: true}}, Reason: "stdin 只能有一个读取者"}}
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
