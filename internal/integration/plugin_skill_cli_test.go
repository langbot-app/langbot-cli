package integration_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSkillDirectoryUploadUsesExistingPreviewAndInstall(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "demo")
	for name, content := range map[string]string{
		"SKILL.md":       "---\nname: demo\ndescription: demo\n---\nUse this skill.",
		"scripts/run.py": "print('hello')\n", ".settings": "mode=test\n", ".git/config": "local git metadata",
	} {
		path := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var previews, installs atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{"skill.install.upload":{"supported":true}}}}`)
		case "/api/v1/skills/install/upload", "/api/v1/skills/install/upload/preview":
			if r.URL.Path == "/api/v1/skills/install/upload/preview" {
				previews.Add(1)
			} else {
				installs.Add(1)
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			payload, err := io.ReadAll(file)
			if err != nil || header.Filename != "demo.zip" {
				t.Errorf("archive name=%q error=%v", header.Filename, err)
			}
			archive, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
			if err != nil {
				t.Error(err)
				return
			}
			found := map[string]string{}
			for _, entry := range archive.File {
				reader, err := entry.Open()
				if err != nil {
					t.Error(err)
					continue
				}
				data, err := io.ReadAll(reader)
				reader.Close()
				if err != nil {
					t.Error(err)
				}
				found[entry.Name] = string(data)
			}
			if len(found) != 3 || found["scripts/run.py"] != "print('hello')\n" || found[".settings"] != "mode=test\n" {
				t.Errorf("archive contents=%v", found)
			}
			if values := r.MultipartForm.Value["source_paths"]; len(values) != 1 || values[0] != "." {
				t.Errorf("source paths=%v", values)
			}
			fmt.Fprint(w, `{"code":0,"data":{"skills":[{"name":"demo"}]}}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	env := map[string]string{"KEY": "directory-upload-key"}
	requireSuccess(t, run(t, configPath, env, "", "context", "add", "managed", "--endpoint", server.URL, "--api-key-env", "KEY", "--expect-workspace", "workspace-a"))
	requireSuccess(t, run(t, configPath, env, "", "context", "use", "managed"))
	for _, dryRun := range []bool{true, false} {
		args := []string{"skill", "install", "upload", "--dir", directory, "--source-path", "."}
		if dryRun {
			args = append(args, "--dry-run")
		}
		value := run(t, configPath, env, "", args...)
		requireSuccess(t, value)
		data, _ := value.data["data"].(map[string]any)
		if data["source_directory"] != directory || data["file_count"] != float64(3) || data["server_write"] != !dryRun {
			t.Fatalf("directory result=%s", value.out)
		}
	}
	if previews.Load() != 1 || installs.Load() != 1 {
		t.Fatalf("preview=%d install=%d", previews.Load(), installs.Load())
	}
	for _, args := range [][]string{
		{"skill", "install", "upload"},
		{"skill", "install", "upload", "--file", "demo.zip", "--dir", directory},
		{"skill", "install", "upload", "--dir", filepath.Join(directory, "missing")},
		{"skill", "file", "write", "demo", "SKILL.md", "--file", "-", "--base-revision", " "},
	} {
		value := run(t, configPath, env, "content", args...)
		if value.code != 2 {
			t.Fatalf("invalid input accepted: args=%v result=%s", args, value.out)
		}
	}
	if previews.Load() != 1 || installs.Load() != 1 {
		t.Fatal("invalid directory input submitted a write")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, _, _, err := invokeRawLookupContext(ctx, configPath, env, func(key string) (string, bool) { value, ok := env[key]; return value, ok }, strings.NewReader(""), "skill", "install", "upload", "--dir", directory, "-o", "json")
	if err != nil || code != 7 {
		t.Fatalf("cancelled packaging code=%d error=%v", code, err)
	}
}

func TestPluginSkillManagementCommandsEnforceSafety(t *testing.T) {
	var configWrites atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view","audit.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{"plugin.get":{"supported":true},"plugin.config.get":{"supported":true},"plugin.config.update":{"supported":true},"plugin.delete":{"supported":true},"skill.get":{"supported":true},"skill.update":{"supported":true}}}}`)
		case "/api/v1/plugins/author/plugin":
			fmt.Fprint(w, `{"code":0,"data":{"plugin":{"manifest":{"manifest":{"metadata":{"author":"author","name":"plugin"}}}}}}`)
		case "/api/v1/plugins/author/plugin/config":
			if r.Method == http.MethodPut {
				configWrites.Add(1)
				fmt.Fprint(w, `{"code":0,"data":{}}`)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"config":{"token":"***","mode":"safe"}}}`)
		case "/api/v1/skills/demo":
			fmt.Fprint(w, `{"code":0,"data":{"skill":{"name":"demo"}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	env := map[string]string{"KEY": "connection-secret"}
	requireSuccess(t, run(t, configPath, env, "", "context", "add", "managed", "--endpoint", server.URL, "--api-key-env", "KEY", "--expect-workspace", "workspace-a"))
	requireSuccess(t, run(t, configPath, env, "", "context", "use", "managed"))

	dryRun := run(t, configPath, env, `{"token":"***","mode":"safe"}`, "plugin", "config", "update", "author", "plugin", "--file", "-", "--dry-run")
	requireSuccess(t, dryRun)
	if configWrites.Load() != 0 || strings.Contains(dryRun.out, "connection-secret") {
		t.Fatalf("config dry-run wrote or leaked credential: %s", dryRun.out)
	}

	blocked := run(t, configPath, env, "", "plugin", "delete", "author", "plugin")
	if blocked.code == 0 || blocked.data["ok"] != false {
		t.Fatalf("plugin delete without --yes was accepted: %s", blocked.out)
	}

	rename := run(t, configPath, env, `{"name":"other"}`, "skill", "update", "demo", "--file", "-")
	if rename.code == 0 || rename.data["ok"] != false {
		t.Fatalf("skill rename was accepted: %s", rename.out)
	}
}

func TestSkillPushDirectoryPreviewPublishConflictAndReadback(t *testing.T) {
	for _, scenario := range []string{"preview", "publish", "conflict", "readback-failure", "readback-mismatch", "permission"} {
		t.Run(scenario, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "packet")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			binary := []byte{0, 255, 1, 2}
			for name, content := range map[string][]byte{"SKILL.md": []byte("---\nname: packet\ndescription: Package\n---\nupdated"), "resource.bin": binary} {
				if err := os.WriteFile(filepath.Join(directory, name), content, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			updates, reads := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/system/context":
					permissions := `["resource.view","resource.manage"]`
					if scenario == "permission" {
						permissions = `["resource.view"]`
					}
					fmt.Fprintf(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":%s}}`, permissions)
				case "/api/v1/system/capabilities":
					fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{"skill.package.preview":{"supported":true},"skill.package.publish":{"supported":true},"skill.get":{"supported":true}}}}`)
				case "/api/v1/skills/packet/package", "/api/v1/skills/packet/package/preview":
					updates++
					preview := scenario == "preview"
					if strings.HasSuffix(r.URL.Path, "/preview") != preview || r.Method != http.MethodPost {
						t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					}
					file, _, err := r.FormFile("file")
					if err != nil {
						t.Error(err)
						return
					}
					defer file.Close()
					content, _ := io.ReadAll(file)
					archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
					if err != nil {
						t.Error(err)
						return
					}
					reader, err := archive.Open("resource.bin")
					if err != nil {
						t.Error(err)
						return
					}
					got, _ := io.ReadAll(reader)
					reader.Close()
					if !bytes.Equal(got, binary) || r.FormValue("base_revision") != "sha256:old" {
						t.Errorf("binary=%v revision=%q", got, r.FormValue("base_revision"))
					}
					if scenario == "conflict" {
						w.WriteHeader(409)
						fmt.Fprint(w, `{"code":-1,"msg":"stale revision"}`)
						return
					}
					fmt.Fprintf(w, `{"code":0,"data":{"skill_name":"packet","base_revision":"sha256:old","proposed_revision":"sha256:new","dry_run":%t,"changes":{"added":["resource.bin"],"modified":["SKILL.md"],"deleted":["old.bin"]},"can_publish":true,"required_permissions":["resource.view","resource.manage"],"skill":{"name":"packet","revision":"sha256:new"}}}`, preview)
				case "/api/v1/skills/packet":
					reads++
					if scenario == "readback-failure" {
						w.WriteHeader(503)
						return
					}
					revision := "sha256:new"
					if scenario == "readback-mismatch" {
						revision = "sha256:other"
					}
					fmt.Fprintf(w, `{"code":0,"data":{"skill":{"name":"packet","revision":%q}}}`, revision)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			env := map[string]string{"KEY": "package-key"}
			requireSuccess(t, run(t, configPath, env, "", "context", "add", "managed", "--endpoint", server.URL, "--api-key-env", "KEY", "--expect-workspace", "workspace-a"))
			args := []string{"--context", "managed", "skill", "push", "packet", "--dir", directory, "--base-revision", "sha256:old"}
			if scenario == "preview" {
				args = append(args, "--dry-run")
			}
			value := run(t, configPath, env, "", args...)
			success := scenario == "preview" || scenario == "publish"
			if success {
				requireSuccess(t, value)
			} else if value.code == 0 {
				t.Fatalf("expected failure: %s", value.out)
			}
			if scenario == "conflict" && !strings.Contains(value.out, "skill_revision_conflict") {
				t.Fatal(value.out)
			}
			if scenario == "publish" && !strings.Contains(value.out, `"verified": true`) {
				t.Fatal(value.out)
			}
			if scenario == "preview" && (!strings.Contains(value.out, "old.bin") || !strings.Contains(value.out, `"dry_run": true`)) {
				t.Fatal(value.out)
			}
			wantUpdates, wantReads := 1, 0
			if scenario == "permission" {
				wantUpdates = 0
			}
			if scenario == "publish" || strings.HasPrefix(scenario, "readback-") {
				wantReads = 1
			}
			if updates != wantUpdates || reads != wantReads {
				t.Fatalf("updates=%d reads=%d", updates, reads)
			}
		})
	}
}
