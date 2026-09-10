package command

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const commandSchemaVersion = 1

type schemaCommandMeta struct {
	Operations           []schemaOperation
	Mutating             bool
	Destructive          bool
	RequiresYes          bool
	RequiresConnection   bool
	RequiresSavedContext bool
	RequiresWorkspace    bool
	SupportsDryRun       bool
	SupportsWait         bool
	RequiredFlags        map[string]bool
	FlagApplicability    map[string]schemaFlagApplicability
	InputConflicts       []schemaConflict
	ArgumentMin          *int
	ArgumentMax          *int
	ArgumentAllowed      []int
}

type schemaConflict struct {
	Operands []schemaOperand `json:"operands"`
	Reason   string          `json:"reason"`
}

type schemaOperand struct {
	Flag     string `json:"flag,omitempty"`
	Argument string `json:"argument,omitempty"`
	Present  bool   `json:"present,omitempty"`
	Equals   any    `json:"equals,omitempty"`
}

type schemaFlagApplicability struct {
	Status string           `json:"status"`
	When   *schemaCondition `json:"when,omitempty"`
	Reason string           `json:"reason,omitempty"`
}

type schemaCondition struct {
	Flag      string   `json:"flag,omitempty"`
	Equals    any      `json:"equals,omitempty"`
	OneOf     []string `json:"one_of,omitempty"`
	Absent    bool     `json:"absent,omitempty"`
	Argument  string   `json:"argument,omitempty"`
	NotEquals any      `json:"not_equals,omitempty"`
}

type schemaOperation struct {
	ID       string           `json:"id,omitempty"`
	Template string           `json:"template,omitempty"`
	Values   []string         `json:"values,omitempty"`
	Role     string           `json:"role,omitempty"`
	When     *schemaCondition `json:"when,omitempty"`
}

type schemaDocument struct {
	SchemaVersion        int               `json:"schema_version"`
	CLIVersion           string            `json:"cli_version"`
	Commands             []schemaCommand   `json:"commands"`
	ExitCodes            map[string]int    `json:"exit_codes"`
	ExitCodeDescriptions map[string]string `json:"exit_code_descriptions"`
}

type schemaCommand struct {
	Name                  string            `json:"name"`
	Path                  []string          `json:"path"`
	Summary               string            `json:"summary"`
	Executable            bool              `json:"executable"`
	Aliases               []string          `json:"aliases,omitempty"`
	Arguments             schemaArguments   `json:"arguments"`
	Flags                 []schemaFlag      `json:"flags"`
	Operations            []schemaOperation `json:"operations"`
	Mutating              bool              `json:"mutating"`
	Destructive           bool              `json:"destructive"`
	RequiresYes           bool              `json:"requires_yes"`
	RequiresConnection    bool              `json:"requires_connection"`
	RequiresSavedContext  bool              `json:"requires_saved_context"`
	RequiresWorkspaceBind bool              `json:"requires_workspace_binding"`
	SupportsDryRun        bool              `json:"supports_dry_run"`
	SupportsWait          bool              `json:"supports_wait"`
	InputConflicts        []schemaConflict  `json:"input_conflicts,omitempty"`
	OutputEnvelope        string            `json:"output_envelope"`
}

type schemaArguments struct {
	Min          int              `json:"min"`
	Max          int              `json:"max"`
	AllowedCount []int            `json:"allowed_count,omitempty"`
	Items        []schemaArgument `json:"items"`
}

type schemaArgument struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

type schemaFlag struct {
	Name          string                   `json:"name"`
	Shorthand     string                   `json:"shorthand,omitempty"`
	Type          string                   `json:"type"`
	Default       any                      `json:"default,omitempty"`
	Required      bool                     `json:"required"`
	Scope         string                   `json:"scope"`
	Usage         string                   `json:"usage,omitempty"`
	Applicability *schemaFlagApplicability `json:"applicability,omitempty"`
}

var placeholderPattern = regexp.MustCompile(`(<[^>]+>|\[[^]]+\])`)

var schemaMetaByName = map[string]schemaCommandMeta{
	"schema":                {},
	"completion":            {},
	"completion.bash":       {},
	"completion.fish":       {},
	"completion.powershell": {},
	"completion.zsh":        {},
	"status":                readMeta(operationIDs("system.info")...),
	"version":               {Operations: []schemaOperation{conditionalOperation("system.info", flagEquals("server", true))}, FlagApplicability: serverOnlyConnectionFlags()},
	"raw": func() schemaCommandMeta {
		meta := readMeta(operationIDs("system.info")...)
		meta.ArgumentMin, meta.ArgumentMax, meta.ArgumentAllowed = intPointer(0), intPointer(2), []int{0, 2}
		meta.FlagApplicability = map[string]schemaFlagApplicability{"file": {Status: "rejected", Reason: "阶段 1A 的 raw 不允许请求体"}}
		return meta
	}(),
	"api.get":      {Operations: []schemaOperation{{Template: "registered operation for GET {path}", Values: []string{"path"}}}, RequiresConnection: true},
	"api.post":     {Operations: []schemaOperation{{Template: "registered operation for POST {path}", Values: []string{"path"}}}, RequiresConnection: true},
	"whoami":       readMeta(operationIDs("system.context")...),
	"capabilities": readMeta(operationIDs("system.context", "system.capabilities")...),

	"provider.list":        readMeta(operationIDs("provider.list")...),
	"provider.get":         readMeta(operationIDs("provider.get")...),
	"provider.create":      writeMeta(operationIDs("provider.create"), schemaWriteOptions{SupportsDryRun: true, RequiredFlags: []string{"file"}, Readbacks: []schemaOperation{readbackID("provider.get")}}),
	"provider.update":      writeMeta(operationIDs("provider.update"), schemaWriteOptions{SupportsDryRun: true, RequiredFlags: []string{"file"}, Readbacks: []schemaOperation{readbackID("provider.get")}}),
	"provider.delete":      writeMeta(operationIDs("provider.delete"), schemaWriteOptions{SupportsDryRun: true, Destructive: true, RequiresYes: true, Readbacks: []schemaOperation{readbackID("provider.get")}}),
	"provider.scan-models": writeMeta(operationIDs("provider.scan_models"), schemaWriteOptions{SupportsDryRun: true, Preconditions: []schemaOperation{preconditionID("provider.get")}}),

	"model.list":   modelMeta("list"),
	"model.get":    modelMeta("get"),
	"model.create": modelMeta("create", "file"),
	"model.update": modelMeta("update", "file"),
	"model.delete": modelMeta("delete"),
	"model.test":   modelMeta("test"),

	"bot.list":   readMeta(operationIDs("bot.list")...),
	"bot.get":    readMeta(operationIDs("bot.get")...),
	"bot.create": writeMeta(operationIDs("bot.create"), schemaWriteOptions{SupportsDryRun: true, RequiredFlags: []string{"file"}, Readbacks: []schemaOperation{readbackID("bot.get")}}),
	"bot.update": writeMeta(operationIDs("bot.update"), schemaWriteOptions{SupportsDryRun: true, RequiredFlags: []string{"file"}, Readbacks: []schemaOperation{readbackID("bot.get")}}),
	"bot.delete": writeMeta(operationIDs("bot.delete"), schemaWriteOptions{SupportsDryRun: true, Destructive: true, RequiresYes: true, Readbacks: []schemaOperation{readbackID("bot.get")}}),

	"pipeline.list":   readMeta(operationIDs("pipeline.list")...),
	"pipeline.get":    readMeta(operationIDs("pipeline.get")...),
	"pipeline.apply":  writeMeta(operationIDs("pipeline.create", "pipeline.update"), schemaWriteOptions{SupportsDryRun: true, RequiredFlags: []string{"file"}, Readbacks: []schemaOperation{readbackID("pipeline.get")}}),
	"pipeline.copy":   writeMeta(operationIDs("pipeline.copy"), schemaWriteOptions{SupportsDryRun: true, Readbacks: []schemaOperation{readbackID("pipeline.get")}}),
	"pipeline.delete": writeMeta(operationIDs("pipeline.delete"), schemaWriteOptions{SupportsDryRun: true, Destructive: true, RequiresYes: true, Readbacks: []schemaOperation{readbackID("pipeline.get")}}),

	"task.list": readMeta(operationIDs("task.list")...),
	"task.get": func() schemaCommandMeta {
		meta := readMeta(operationIDs("task.get")...)
		meta.SupportsWait = true
		return meta
	}(),

	"knowledge-base.list":        readMeta(operationIDs("knowledge_base.list")...),
	"knowledge-base.get":         readMeta(operationIDs("knowledge_base.get")...),
	"knowledge-base.create":      writeMeta(operationIDs("knowledge_base.create"), schemaWriteOptions{SupportsDryRun: true, RequiredFlags: []string{"file"}, Readbacks: []schemaOperation{readbackID("knowledge_base.get")}}),
	"knowledge-base.update":      writeMeta(operationIDs("knowledge_base.update"), schemaWriteOptions{SupportsDryRun: true, RequiredFlags: []string{"file"}, Readbacks: []schemaOperation{readbackID("knowledge_base.get")}}),
	"knowledge-base.delete":      writeMeta(operationIDs("knowledge_base.delete"), schemaWriteOptions{SupportsDryRun: true, Destructive: true, RequiresYes: true, Readbacks: []schemaOperation{readbackID("knowledge_base.get")}}),
	"knowledge-base.retrieve":    readMetaWithFlags(operationIDs("knowledge_base.retrieve"), "file"),
	"knowledge-base.file.list":   readMeta(operationIDs("knowledge_base.file.list")...),
	"knowledge-base.file.delete": writeMeta(operationIDs("knowledge_base.file.delete"), schemaWriteOptions{SupportsDryRun: true, Destructive: true, RequiresYes: true, Readbacks: []schemaOperation{readbackID("knowledge_base.file.list")}}),
	"knowledge-base.ingest":      writeMeta(operationIDs("knowledge_base.file.store", "file.document.upload", "knowledge_base.get", conditionalID("task.get", flagEquals("wait", true))), schemaWriteOptions{SupportsDryRun: true, SupportsWait: true, RequiredFlags: []string{"file"}}),

	"plugin.list":                readMeta(operationIDs("plugin.list")...),
	"plugin.get":                 readMeta(operationIDs("plugin.get")...),
	"plugin.config.get":          readMeta(operationIDs("plugin.config.get")...),
	"plugin.config.update":       writeMeta(operationIDs("plugin.config.update"), schemaWriteOptions{SupportsDryRun: true, RequiredFlags: []string{"file"}, Readbacks: []schemaOperation{readbackID("plugin.config.get")}}),
	"plugin.logs":                readMeta(operationIDs("plugin.logs")...),
	"plugin.delete":              writeMeta(operationIDs("plugin.delete", readbackID("plugin.get"), conditionalID("task.get", flagEquals("wait", true))), schemaWriteOptions{SupportsDryRun: true, SupportsWait: true, Destructive: true, RequiresYes: true}),
	"plugin.install.github":      writeMeta(operationIDs("plugin.install.github", conditionalID("task.get", flagEquals("wait", true))), schemaWriteOptions{SupportsDryRun: true, SupportsWait: true, RequiredFlags: []string{"file"}}),
	"plugin.install.marketplace": writeMeta(operationIDs("plugin.install.marketplace", conditionalID("task.get", flagEquals("wait", true))), schemaWriteOptions{SupportsDryRun: true, SupportsWait: true, RequiredFlags: []string{"file"}}),
	"plugin.install.local":       writeMeta(operationIDs("plugin.install.local", conditionalID("task.get", flagEquals("wait", true))), schemaWriteOptions{SupportsDryRun: true, SupportsWait: true, RequiredFlags: []string{"file"}}),
	"plugin.upgrade":             writeMeta(operationIDs("plugin.upgrade", "plugin.get", conditionalID("task.get", flagEquals("wait", true))), schemaWriteOptions{SupportsDryRun: true, SupportsWait: true}),

	"skill.list":           readMeta(operationIDs("skill.list")...),
	"skill.get":            readMeta(operationIDs("skill.get")...),
	"skill.create":         writeMeta(operationIDs("skill.create"), schemaWriteOptions{SupportsDryRun: true, RequiredFlags: []string{"file"}, Readbacks: []schemaOperation{readbackID("skill.get")}}),
	"skill.update":         writeMeta(operationIDs("skill.update"), schemaWriteOptions{SupportsDryRun: true, RequiredFlags: []string{"file"}, Readbacks: []schemaOperation{readbackID("skill.get")}}),
	"skill.delete":         writeMeta(operationIDs("skill.delete"), schemaWriteOptions{SupportsDryRun: true, Destructive: true, RequiresYes: true, Readbacks: []schemaOperation{readbackID("skill.get")}}),
	"skill.file.list":      readMeta(operationIDs("skill.files.list")...),
	"skill.file.read":      readMeta(operationIDs("skill.files.read")...),
	"skill.file.write":     writeMeta(operationIDs("skill.files.write"), schemaWriteOptions{SupportsDryRun: true, RequiredFlags: []string{"file"}, Readbacks: []schemaOperation{readbackID("skill.files.read")}}),
	"skill.preview":        readMeta(operationIDs("skill.preview")...),
	"skill.install.github": writeMeta(operationIDs("skill.install.github"), schemaWriteOptions{SupportsDryRun: true, RequiredFlags: []string{"file"}}),
	"skill.install.upload": writeMeta(operationIDs("skill.install.upload"), schemaWriteOptions{SupportsDryRun: true, RequiredFlags: []string{"file"}}),

	"mcp-server.list":               readMeta(operationIDs("mcp_server.list")...),
	"mcp-server.get":                readMeta(operationIDs("mcp_server.get")...),
	"mcp-server.create":             writeMeta(operationIDs("mcp_server.create"), schemaWriteOptions{SupportsDryRun: true, RequiredFlags: []string{"file"}, Readbacks: []schemaOperation{readbackID("mcp_server.get")}}),
	"mcp-server.update":             writeMeta(operationIDs("mcp_server.update"), schemaWriteOptions{SupportsDryRun: true, RequiredFlags: []string{"file"}, Readbacks: []schemaOperation{readbackID("mcp_server.get")}}),
	"mcp-server.delete":             writeMeta(operationIDs("mcp_server.delete"), schemaWriteOptions{SupportsDryRun: true, Destructive: true, RequiresYes: true, Readbacks: []schemaOperation{readbackID("mcp_server.get")}}),
	"mcp-server.resources":          readMeta(operationIDs("mcp_server.resources")...),
	"mcp-server.resource-templates": readMeta(operationIDs("mcp_server.resource_templates")...),
	"mcp-server.resource-read":      readMetaWithFlags(operationIDs("mcp_server.resource_read"), "file"),
	"mcp-server.logs":               readMeta(operationIDs("mcp_server.logs")...),
	"mcp-server.test":               writeMeta(operationIDs("mcp_server.test", conditionalArgumentID("mcp_server.get", "server-name", "_"), conditionalID("task.get", flagEquals("wait", true))), schemaWriteOptions{SupportsDryRun: true, SupportsWait: true}),

	"context.add": {Mutating: true, RequiredFlags: map[string]bool{"endpoint": true}, FlagApplicability: localContextMutationFlags()},
	"context.update": {Mutating: true, FlagApplicability: localContextMutationFlags(), InputConflicts: []schemaConflict{
		{Operands: []schemaOperand{{Flag: "api-key-env", Present: true}, {Flag: "clear-credential", Equals: true}}, Reason: "不能同时设置 api-key-env 和 clear-credential"},
		{Operands: []schemaOperand{{Flag: "expect-workspace", Present: true}, {Flag: "clear-binding", Equals: true}}, Reason: "不能同时设置 expect-workspace 和 clear-binding"},
	}},
	"context.list":   {FlagApplicability: localContextFlags()},
	"context.show":   {},
	"context.use":    {Mutating: true, FlagApplicability: localContextFlags()},
	"context.remove": {Mutating: true, Destructive: true, RequiresYes: true, FlagApplicability: localContextFlags()},
	"context.check":  {Operations: operationIDs("system.info", "system.context"), RequiresConnection: true, InputConflicts: contextCheckConflicts()},
}

func contextCheckConflicts() []schemaConflict {
	conflicts := make([]schemaConflict, 0, 4)
	for _, operand := range []schemaOperand{
		{Flag: "context", Present: true},
		{Flag: "endpoint", Present: true},
		{Flag: "api-key-stdin", Equals: true},
		{Argument: "name", Present: true},
	} {
		conflicts = append(conflicts, schemaConflict{Operands: []schemaOperand{{Flag: "all", Equals: true}, operand}, Reason: "--all 不能与目标覆盖参数同时使用"})
	}
	return conflicts
}

func localContextFlags() map[string]schemaFlagApplicability {
	flags := map[string]schemaFlagApplicability{}
	for _, name := range []string{"api-key-stdin", "context", "endpoint", "timeout"} {
		flags[name] = schemaFlagApplicability{Status: "rejected", Reason: "本地 context 命令不接受连接覆盖参数"}
	}
	return flags
}

func localContextMutationFlags() map[string]schemaFlagApplicability {
	return map[string]schemaFlagApplicability{
		"api-key-stdin": {Status: "rejected", Reason: "本地 context 命令不接受连接覆盖参数"},
		"context":       {Status: "rejected", Reason: "本地 context 命令不接受连接覆盖参数"},
	}
}

func serverOnlyConnectionFlags() map[string]schemaFlagApplicability {
	flags := map[string]schemaFlagApplicability{}
	for _, name := range []string{"api-key-stdin", "context", "endpoint", "timeout"} {
		flags[name] = schemaFlagApplicability{Status: "conditional", When: flagEquals("server", true), Reason: "仅 --server 时适用"}
	}
	return flags
}

type schemaWriteOptions struct {
	SupportsDryRun bool
	SupportsWait   bool
	Destructive    bool
	RequiresYes    bool
	RequiredFlags  []string
	Preconditions  []schemaOperation
	Readbacks      []schemaOperation
}

func operationIDs(values ...any) []schemaOperation {
	operations := make([]schemaOperation, 0, len(values))
	for _, value := range values {
		switch typed := value.(type) {
		case string:
			operations = append(operations, schemaOperation{ID: typed})
		case schemaOperation:
			operations = append(operations, typed)
		}
	}
	return operations
}

func conditionalID(id string, condition *schemaCondition) schemaOperation {
	return schemaOperation{ID: id, When: condition}
}

func conditionalOperation(id string, condition *schemaCondition) schemaOperation {
	return conditionalID(id, condition)
}

func flagEquals(name string, value any) *schemaCondition {
	return &schemaCondition{Flag: name, Equals: value}
}

func conditionalArgumentID(id, argument string, notEquals any) schemaOperation {
	return schemaOperation{ID: id, When: &schemaCondition{Argument: argument, NotEquals: notEquals}}
}

func readbackID(id string) schemaOperation {
	return schemaOperation{ID: id, Role: "readback"}
}

func preconditionID(id string) schemaOperation {
	return schemaOperation{ID: id, Role: "precondition"}
}

func readbackTemplate(template string, values []string) schemaOperation {
	return schemaOperation{Template: template, Values: values, Role: "readback"}
}

func preconditionTemplate(template string, values []string) schemaOperation {
	return schemaOperation{Template: template, Values: values, Role: "precondition"}
}

func readMeta(operations ...schemaOperation) schemaCommandMeta {
	return schemaCommandMeta{Operations: operations, RequiresConnection: true}
}

func readMetaWithFlags(operations []schemaOperation, required ...string) schemaCommandMeta {
	meta := readMeta(operations...)
	meta.RequiredFlags = requiredFlagSet(required...)
	return meta
}

func writeMeta(operations []schemaOperation, options schemaWriteOptions) schemaCommandMeta {
	allOperations := append([]schemaOperation{}, options.Preconditions...)
	allOperations = append(allOperations, operations...)
	allOperations = append(allOperations, options.Readbacks...)
	return schemaCommandMeta{
		Operations: allOperations, Mutating: true, Destructive: options.Destructive, RequiresYes: options.RequiresYes,
		RequiresConnection: true, RequiresSavedContext: true, RequiresWorkspace: true,
		SupportsDryRun: options.SupportsDryRun, SupportsWait: options.SupportsWait,
		RequiredFlags: requiredFlagSet(options.RequiredFlags...),
	}
}

func requiredFlagSet(names ...string) map[string]bool {
	flags := make(map[string]bool, len(names))
	for _, name := range names {
		flags[name] = true
	}
	return flags
}

func modelMeta(operation string, requiredFlags ...string) schemaCommandMeta {
	meta := writeMeta(nil, schemaWriteOptions{RequiredFlags: requiredFlags})
	modelTypes := []string{"llm", "embedding", "rerank"}
	meta.Operations = []schemaOperation{{Template: "model.{type}." + operation, Values: modelTypes, When: &schemaCondition{Flag: "type", OneOf: modelTypes}}}
	if operation == "create" || operation == "update" || operation == "delete" {
		meta.Operations = append(meta.Operations, schemaOperation{Template: "model.{type}.get", Values: modelTypes, Role: "readback"})
	}
	if operation == "test" {
		meta.Operations = append(meta.Operations, preconditionTemplate("model.{type}.get", modelTypes))
	}
	if operation == "list" {
		meta.Operations = append(meta.Operations, schemaOperation{Template: "model.{type}.list", Values: modelTypes, When: &schemaCondition{Flag: "type", Absent: true}})
	} else {
		meta.RequiredFlags["type"] = true
	}
	if operation == "list" || operation == "get" {
		meta.Mutating = false
		meta.RequiresSavedContext = false
		meta.RequiresWorkspace = false
	}
	if operation == "test" {
		meta.Mutating = true
		meta.SupportsDryRun = true
	}
	if operation == "create" || operation == "update" {
		meta.SupportsDryRun = true
	}
	if operation == "delete" {
		meta.Destructive, meta.RequiresYes, meta.SupportsDryRun = true, true, true
	}
	return meta
}

func intPointer(value int) *int { return &value }

func newSchemaCommand(deps Dependencies, flags *globalFlags) *cobra.Command {
	var commandName string
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "输出机器可读的命令 schema",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := buildSchema(cmd.Root(), deps.Version, commandName)
			if err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCommand(deps, flags, app.Result{Data: data}, nil)
		},
	}
	cmd.Flags().StringVar(&commandName, "command", "", "按逻辑命令名筛选，例如 bot.create")
	return cmd
}

func buildSchema(root *cobra.Command, cliVersion, filter string) (schemaDocument, error) {
	if err := validateSchemaMetadata(root); err != nil {
		return schemaDocument{}, err
	}
	filter = strings.TrimSpace(filter)
	commands := make([]schemaCommand, 0)
	collectSchemaCommands(root, nil, &commands)
	if filter != "" {
		found := false
		filtered := commands[:0]
		for _, command := range commands {
			if command.Name == filter {
				found = true
				filtered = append(filtered, command)
			}
		}
		if !found {
			return schemaDocument{}, result.New("input", "未知命令 schema: "+filter)
		}
		commands = filtered
	}
	return schemaDocument{
		SchemaVersion: commandSchemaVersion,
		CLIVersion:    cliVersion,
		Commands:      commands,
		ExitCodes: map[string]int{
			"success": 0, "input": 2, "auth": 3, "permission": 4, "not_found": 5,
			"precondition": 6, "network": 7, "result_unknown": 7, "incompatible": 8, "server": 10, "internal": 10,
		},
		ExitCodeDescriptions: map[string]string{
			"success":        "命令执行成功",
			"input":          "输入或配置无效",
			"auth":           "凭据无效、过期或已吊销",
			"permission":     "当前凭据权限不足",
			"not_found":      "目标资源不存在",
			"precondition":   "安全前置或资源状态不满足",
			"network":        "网络、TLS、超时或取消",
			"incompatible":   "接口、协议或服务端能力不兼容",
			"internal":       "CLI schema 或未分类的内部错误",
			"server":         "服务端或未分类的服务错误",
			"result_unknown": "请求可能已提交但结果无法确认",
		},
	}, nil
}

func collectSchemaCommands(parent *cobra.Command, prefix []string, result *[]schemaCommand) {
	children := append([]*cobra.Command(nil), parent.Commands()...)
	sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
	for _, child := range children {
		if child.Hidden || child.Name() == "help" {
			continue
		}
		path := append(append([]string(nil), prefix...), child.Name())
		name := strings.Join(path, ".")
		meta := schemaMetaByName[name]
		*result = append(*result, schemaCommand{
			Name:                  name,
			Path:                  path,
			Summary:               child.Short,
			Executable:            len(child.Commands()) == 0,
			Aliases:               append([]string(nil), child.Aliases...),
			Arguments:             schemaArgumentsFor(child, meta),
			Flags:                 schemaFlagsFor(child, meta),
			Operations:            append([]schemaOperation{}, meta.Operations...),
			Mutating:              meta.Mutating,
			Destructive:           meta.Destructive,
			RequiresYes:           meta.RequiresYes,
			RequiresConnection:    meta.RequiresConnection,
			RequiresSavedContext:  meta.RequiresSavedContext,
			RequiresWorkspaceBind: meta.RequiresWorkspace,
			SupportsDryRun:        meta.SupportsDryRun,
			SupportsWait:          meta.SupportsWait,
			InputConflicts:        schemaInputConflicts(meta, schemaFlagsFor(child, meta)),
			OutputEnvelope:        "result-envelope",
		})
		collectSchemaCommands(child, path, result)
	}
}

func validateSchemaMetadata(root *cobra.Command) error {
	var validate func(*cobra.Command, []string) error
	validate = func(parent *cobra.Command, prefix []string) error {
		for _, child := range parent.Commands() {
			if child.Hidden || child.Name() == "help" {
				continue
			}
			path := append(append([]string(nil), prefix...), child.Name())
			name := strings.Join(path, ".")
			meta, registered := schemaMetaByName[name]
			if len(child.Commands()) == 0 {
				if !registered {
					return result.New("internal", "命令缺少 schema 元数据: "+name)
				}
				flags := schemaFlagsFor(child, meta)
				for flagName := range meta.RequiredFlags {
					if !schemaHasFlag(flags, flagName) {
						return result.New("internal", "schema 元数据引用了不存在的 flag: "+name+" --"+flagName)
					}
				}
				for flagName, applicability := range meta.FlagApplicability {
					if !schemaHasFlag(flags, flagName) {
						return result.New("internal", "schema 元数据引用了不存在的 flag: "+name+" --"+flagName)
					}
					if applicability.Status != "available" && applicability.Status != "conditional" && applicability.Status != "rejected" {
						return result.New("internal", "schema flag applicability 无效: "+name+" --"+flagName)
					}
				}
				if meta.SupportsDryRun && !schemaHasFlag(flags, "dry-run") {
					return result.New("internal", "schema 元数据声明了不存在的 --dry-run: "+name)
				}
				if meta.SupportsWait && !schemaHasFlag(flags, "wait") {
					return result.New("internal", "schema 元数据声明了不存在的 --wait: "+name)
				}
				if meta.RequiresYes && !schemaHasFlag(flags, "yes") {
					return result.New("internal", "schema 元数据声明了不存在的 --yes: "+name)
				}
				if meta.Destructive && !meta.RequiresYes {
					return result.New("internal", "破坏性命令必须要求 --yes: "+name)
				}
			}
			if err := validate(child, path); err != nil {
				return err
			}
		}
		return nil
	}
	return validate(root, nil)
}

func schemaHasFlag(flags []schemaFlag, name string) bool {
	for _, flag := range flags {
		if flag.Name == name {
			return true
		}
	}
	return false
}

func schemaArgumentsFor(command *cobra.Command, meta schemaCommandMeta) schemaArguments {
	arguments := parseArguments(command.Use)
	min, max := len(arguments), len(arguments)
	for _, argument := range arguments {
		if !argument.Required {
			min--
		}
	}
	if meta.ArgumentMin != nil {
		min = *meta.ArgumentMin
	}
	if meta.ArgumentMax != nil {
		max = *meta.ArgumentMax
	}
	return schemaArguments{Min: min, Max: max, AllowedCount: append([]int(nil), meta.ArgumentAllowed...), Items: arguments}
}

func parseArguments(use string) []schemaArgument {
	matches := placeholderPattern.FindAllString(use, -1)
	arguments := make([]schemaArgument, 0)
	for _, match := range matches {
		required := strings.HasPrefix(match, "<")
		inner := strings.TrimSpace(match[1 : len(match)-1])
		for _, name := range strings.Fields(inner) {
			arguments = append(arguments, schemaArgument{Name: name, Type: "string", Required: required})
		}
	}
	return arguments
}

func schemaFlagsFor(command *cobra.Command, meta schemaCommandMeta) []schemaFlag {
	flags := make([]schemaFlag, 0)
	seen := make(map[string]bool)
	appendFlags := func(set *pflag.FlagSet, scope string) {
		set.VisitAll(func(flag *pflag.Flag) {
			if seen[flag.Name] {
				return
			}
			seen[flag.Name] = true
			schemaFlagValue := schemaFlag{
				Name: flag.Name, Shorthand: flag.Shorthand, Type: flag.Value.Type(),
				Default: parseDefault(flag), Required: meta.RequiredFlags[flag.Name], Scope: scope, Usage: flag.Usage,
			}
			if applicability, ok := meta.FlagApplicability[flag.Name]; ok {
				value := applicability
				schemaFlagValue.Applicability = &value
			}
			flags = append(flags, schemaFlagValue)
		})
	}
	appendFlags(command.LocalNonPersistentFlags(), "local")
	appendFlags(command.InheritedFlags(), "inherited")
	sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
	return flags
}

func schemaInputConflicts(meta schemaCommandMeta, flags []schemaFlag) []schemaConflict {
	conflicts := append([]schemaConflict(nil), meta.InputConflicts...)
	hasAPIKeyStdin := false
	hasStdinFile := false
	hasDryRun := false
	hasWait := false
	for _, flag := range flags {
		if flag.Name == "api-key-stdin" {
			hasAPIKeyStdin = true
		}
		if flag.Name == "file" && strings.Contains(flag.Usage, "stdin") {
			hasStdinFile = true
		}
		if flag.Name == "dry-run" {
			hasDryRun = true
		}
		if flag.Name == "wait" {
			hasWait = true
		}
	}
	if hasAPIKeyStdin && hasStdinFile {
		conflicts = append(conflicts, schemaConflict{
			Operands: []schemaOperand{{Flag: "api-key-stdin", Equals: true}, {Flag: "file", Equals: "-"}},
			Reason:   "API Key 和请求体不能同时从 stdin 读取",
		})
	}
	if meta.SupportsDryRun && meta.SupportsWait && hasDryRun && hasWait {
		conflicts = append(conflicts, schemaConflict{
			Operands: []schemaOperand{{Flag: "dry-run", Equals: true}, {Flag: "wait", Equals: true}},
			Reason:   "dry-run 不能与 wait 同时使用",
		})
	}
	return conflicts
}

func parseDefault(flag *pflag.Flag) any {
	value := flag.DefValue
	switch flag.Value.Type() {
	case "bool":
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
	case "int", "int8", "int16", "int32", "int64":
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			return parsed
		}
	case "float32", "float64":
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			return parsed
		}
	case "stringSlice", "stringArray":
		if value == "[]" || value == "" {
			return []string{}
		}
		return strings.Split(value, ",")
	}
	return value
}
