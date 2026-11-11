package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type multiDeploymentRunner struct {
	projects map[string]string
	states   map[string]string
	mutated  []string
	files    map[string]string
}

func (r *multiDeploymentRunner) Docker(_ context.Context, args ...string) (deploy.CommandResult, error) {
	if strings.Join(args, " ") == "info --format {{.ServerVersion}}" {
		return deploy.CommandResult{Stdout: "29"}, nil
	}
	if strings.Join(args, " ") == "compose ls --all --format json" {
		projects := []map[string]string{}
		for project, file := range r.files {
			projects = append(projects, map[string]string{"Name": project, "ConfigFiles": file})
		}
		data, _ := json.Marshal(projects)
		return deploy.CommandResult{Stdout: string(data)}, nil
	}
	return deploy.CommandResult{}, fmt.Errorf("unexpected Docker command %v", args)
}

func (r *multiDeploymentRunner) Compose(_ context.Context, record deploy.Record, args ...string) (deploy.CommandResult, error) {
	project := record.ComposeProject
	switch strings.Join(args, " ") {
	case "config --services":
		if project == "unrelated" {
			return deploy.CommandResult{Stdout: "redis\n"}, nil
		}
		return deploy.CommandResult{Stdout: "langbot\nlangbot_plugin_runtime\n"}, nil
	case "port langbot 5300":
		return deploy.CommandResult{Stdout: "127.0.0.1:" + r.projects[project]}, nil
	case "stop":
		r.mutated = append(r.mutated, project)
		r.states[project] = "exited"
		return deploy.CommandResult{}, nil
	case "ps --format json":
		state := r.states[project]
		if state == "" {
			state = "running"
		}
		return deploy.CommandResult{Stdout: fmt.Sprintf(`[{"Service":"langbot","State":%q},{"Service":"langbot_plugin_runtime","State":%q}]`, state, state)}, nil
	}
	return deploy.CommandResult{}, fmt.Errorf("unexpected Compose command %v", args)
}

func TestMultipleAdoptionsAndLifecycleSelectCorrectDeployment(t *testing.T) {
	root := t.TempDir()
	runner := &multiDeploymentRunner{projects: map[string]string{"first": "15301", "second": "15302", "duplicate": "15301"}, states: map[string]string{}, files: map[string]string{}}
	transport := taskRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return taskResponse(`{"code":0,"data":{"version":"v4.10.10","edition":"community"}}`), nil
	})
	service := New(Dependencies{
		LocalLocatorPath: filepath.Join(root, "locator.yaml"), LocalRunner: runner,
		Transport: transport, LocalHTTP: &http.Client{Transport: transport},
		LookupEnv:         func(string) (string, bool) { return "", false },
		DefaultConfigPath: func() (string, error) { return filepath.Join(root, "config.yaml"), nil },
	})
	ctx := context.Background()
	ids := map[string]string{}
	for _, project := range []string{"first", "second"} {
		file := filepath.Join(root, project+".yaml")
		if err := os.WriteFile(file, []byte("services: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runner.files[project] = file
		adopted, err := service.Adopt(ctx, AdoptOptions{Dir: filepath.Join(root, project), ComposeFile: file, ComposeProject: project, Endpoint: "http://127.0.0.1:" + runner.projects[project]})
		if err != nil {
			t.Fatalf("adopt %s: %v", project, err)
		}
		ids[project] = adopted.Data.(map[string]any)["record"].(deploy.Record).DeploymentID
	}
	listed, err := service.localDeployments()
	if err != nil || len(listed) != 2 {
		t.Fatalf("list=%+v error=%v", listed, err)
	}
	// A context can select second while an explicit first endpoint still binds first.
	for _, name := range []string{"first", "second"} {
		if _, err := service.ContextAdd(name, "http://127.0.0.1:"+runner.projects[name], "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.ContextUse("second"); err != nil {
		t.Fatal(err)
	}
	options := CheckOptions{Endpoint: "http://127.0.0.1:15301", EndpointSet: true}
	status, err := service.Status(ctx, options)
	if err != nil || status.Data.(ActiveEnvironment).Runtime.DeploymentID != ids["first"] {
		t.Fatalf("status=%+v error=%v", status, err)
	}
	if _, err := service.Stop(ctx, LifecycleOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(runner.mutated) != 1 || runner.mutated[0] != "second" || runner.states["first"] != "" {
		t.Fatalf("wrong project mutated: %+v", runner)
	}
	if _, err := service.ContextUse("first"); err != nil {
		t.Fatal(err)
	}
	fallback, err := service.Status(ctx, CheckOptions{})
	if err != nil || fallback.Data.(ActiveEnvironment).Connection.Endpoint != options.Endpoint {
		t.Fatalf("fallback=%+v error=%v", fallback, err)
	}
	// Duplicate ownership is rejected without replacing either registration.
	_, err = service.Adopt(ctx, AdoptOptions{Dir: filepath.Join(root, "duplicate"), ComposeFile: runner.files["first"], ComposeProject: "duplicate", Endpoint: options.Endpoint})
	if result.AsError(err).Kind != "precondition" {
		t.Fatalf("duplicate adopt error=%v", err)
	}
	dirs, err := (deploy.Locator{Path: service.deps.LocalLocatorPath}).Directories()
	if err != nil || len(dirs) != 2 {
		t.Fatalf("duplicate changed registry: %v %v", dirs, err)
	}
}

func TestDiscoveryFindsMultipleProjectsWithoutRegisteringOrMutating(t *testing.T) {
	root := t.TempDir()
	runner := &multiDeploymentRunner{projects: map[string]string{"first": "15301", "second": "15302"}, files: map[string]string{}}
	for _, project := range []string{"first", "second", "unrelated"} {
		file := filepath.Join(root, project+".yaml")
		if err := os.WriteFile(file, []byte("services: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runner.files[project] = file
	}
	runner.files["overrides"] = runner.files["first"] + "," + runner.files["second"]
	locatorPath := filepath.Join(root, "locator.yaml")
	service := New(Dependencies{LocalLocatorPath: locatorPath, LocalRunner: runner,
		DefaultConfigPath: func() (string, error) { return filepath.Join(root, "config.yaml"), nil },
		LookupEnv:         func(string) (string, bool) { return "", false },
	})
	discovered, err := service.DeploymentDiscover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data := discovered.Data.(map[string]any)
	entries := data["deployments"].([]discoveredDeployment)
	if len(entries) != 2 || len(data["skipped_projects"].([]string)) != 1 {
		t.Fatalf("discovery=%+v", data)
	}
	for _, entry := range entries {
		if entry.Binding != "unmanaged" || entry.Endpoint != "http://127.0.0.1:"+runner.projects[entry.Project] {
			t.Fatalf("entry=%+v", entry)
		}
	}
	if len(runner.mutated) != 0 {
		t.Fatalf("discovery mutated Docker: %v", runner.mutated)
	}
	if _, err := service.ContextAdd("saved", "https://cloud.example", "", "", ""); err != nil {
		t.Fatal(err)
	}
	contexts, err := service.ContextList(true)
	if err != nil {
		t.Fatal(err)
	}
	contextData := contexts.Data.(map[string]any)
	if len(contextData["contexts"].([]map[string]any)) != 1 || len(contextData["discovered_deployments"].([]discoveredDeployment)) != 2 {
		t.Fatalf("combined context discovery=%+v", contextData)
	}
	if _, err := os.Stat(locatorPath); !os.IsNotExist(err) {
		t.Fatalf("discovery wrote registry: %v", err)
	}
}

func TestAmbiguousOrMismatchedDeploymentIsNeverSelected(t *testing.T) {
	root := t.TempDir()
	locator := deploy.Locator{Path: filepath.Join(root, "locator.yaml")}
	for _, name := range []string{"first", "second"} {
		dir := filepath.Join(root, name)
		if err := (deploy.Store{Dir: dir}).Write(context.Background(), deploy.Record{DeploymentID: name, InstanceUUID: "shared", Endpoint: "http://127.0.0.1:15301"}); err != nil {
			t.Fatal(err)
		}
		if err := locator.Write(dir); err != nil {
			t.Fatal(err)
		}
	}
	service := New(Dependencies{LocalLocatorPath: locator.Path})
	if _, err := service.matchingLocalDiagnostics("http://127.0.0.1:15301", api.Context{}, false); result.AsError(err).Kind != "precondition" {
		t.Fatalf("ambiguous error=%v", err)
	}
	// A verified identity mismatch must not fall back to a matching endpoint.
	diagnostics, err := service.matchingLocalDiagnostics("http://127.0.0.1:15301", api.Context{InstanceUUID: "different"}, true)
	if err != nil {
		t.Fatal(err)
	}
	record, err := diagnostics.Store.Read()
	if err != nil || deploymentMatches(record, "http://127.0.0.1:15301", api.Context{InstanceUUID: "different"}, true) {
		t.Fatalf("accepted mismatched identity: %v", err)
	}
}

func TestDeploymentListPreservesUnavailableRegistration(t *testing.T) {
	root := t.TempDir()
	locator := deploy.Locator{Path: filepath.Join(root, "locator.yaml")}
	service := New(Dependencies{LocalLocatorPath: locator.Path})
	listed, err := service.localDeployments()
	if err != nil || len(listed) != 0 {
		t.Fatalf("unexpected initial list=%+v %v", listed, err)
	}
	if err := locator.Write(filepath.Join(root, "missing")); err != nil {
		t.Fatal(err)
	}
	listed, err = service.localDeployments()
	if err != nil {
		t.Fatal(err)
	}
	entries := listed
	if len(entries) != 1 || entries[0].Binding != "unavailable" {
		t.Fatalf("missing registration was hidden: %+v", entries)
	}
}
