package deploy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/clientidentity"
	"go.yaml.in/yaml/v3"
)

func TestLocalHealthProbeIdentity(t *testing.T) {
	ctx := clientidentity.WithContext(context.Background(), "0.4.1", "upgrade")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.UserAgent() != clientidentity.UserAgent(ctx) || request.Header.Get("X-LangBot-Client-Command") != "upgrade" {
			t.Error("health probe lost identity")
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if _, err := (Diagnostics{}).probeHTTP(ctx, Record{Endpoint: server.URL}); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentIdentityInstallAndLegacyUpgrade(t *testing.T) {
	record, dir := upgradeFixture(t)
	content, err := os.ReadFile(record.ComposeFile)
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(string(content), deployComposeEnvironment, "", 1)
	if err := os.WriteFile(record.ComposeFile, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	custom := "# user settings\nCUSTOM_TOKEN='keep=exactly'\nLANGBOT_BOX_ENABLED=true\n"
	if err := os.WriteFile(record.EnvFile, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	updated, _, err := ComposeForUpgrade(record, dir, "v4.10.10")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), deployComposeEnvironment) {
		t.Fatal("upgrade did not inject Core environment")
	}
	if err := WriteUpgradeCompose(record, updated); err != nil {
		t.Fatal(err)
	}
	if err := WriteDeployIdentity(record, "0.4.2"); err != nil {
		t.Fatal(err)
	}
	record.CoreVersion, record.Asset.Name = "v4.10.10", "rockchin/langbot:v4.10.10"
	if err := ValidateOwnedLayout(record, dir); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"0.4.2", "0.4.3"} {
		if err := WriteDeployIdentity(record, version); err != nil {
			t.Fatal(err)
		}
		environment, err := os.ReadFile(record.EnvFile)
		if err != nil {
			t.Fatal(err)
		}
		want := custom + "LANGBOT_DEPLOY_SOURCE=lbctl\nLANGBOT_DEPLOY_TOOL_VERSION=" + version + "\n"
		if string(environment) != want {
			t.Fatalf("env mismatch: %s", environment)
		}
	}
	assets, err := NewInstallAssets(filepath.Join(t.TempDir(), "new"), "v4.10.10", "basic", 15300, InstallNetworkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"", "0.4.1"} {
		assets.SetDeployIdentity(version)
		if version == "" {
			version = "dev"
		}
		if !strings.Contains(string(assets.Environment), "LANGBOT_DEPLOY_SOURCE=lbctl\nLANGBOT_DEPLOY_TOOL_VERSION="+version+"\n") {
			t.Fatal("missing install identity")
		}
		if !strings.Contains(string(assets.Compose), deployComposeEnvironment) {
			t.Fatal("missing Core environment")
		}
	}
}

func TestAdoptedDeploymentIdentityPreservesCustomEnvironment(t *testing.T) {
	for _, environment := range []string{
		"",
		"    environment: {}\n",
		"    environment: {KEEP: value, LANGBOT_DEPLOY_TOOL_VERSION: old}\n",
		"    environment:\n      KEEP: value\n      LANGBOT_DEPLOY_SOURCE: old\n",
		"    environment: [KEEP=value, LANGBOT_DEPLOY_TOOL_VERSION=old]\n",
		"    environment:\n      - KEEP=value\n      - LANGBOT_DEPLOY_SOURCE=old\n",
	} {
		t.Run(environment, func(t *testing.T) {
			content := "# custom compose\nservices:\n  langbot:\n    image: rockchin/langbot:v4.10.10\n" + environment + "    ports: ['15300:5300'] # keep\n"
			updated, err := addDeployEnvironment([]byte(content))
			if err != nil {
				t.Fatal(err)
			}
			var document yaml.Node
			if err := yaml.Unmarshal(updated, &document); err != nil {
				t.Fatalf("%v\n%s", err, updated)
			}
			node := mappingValue(mappingValue(mappingValue(document.Content[0], "services"), "langbot"), "environment")
			values := map[string]string{}
			if node.Kind == yaml.SequenceNode {
				for _, item := range node.Content {
					key, value, _ := strings.Cut(item.Value, "=")
					values[key] = value
				}
			} else if err := node.Decode(&values); err != nil {
				t.Fatal(err)
			}
			if values["LANGBOT_DEPLOY_SOURCE"] != "${LANGBOT_DEPLOY_SOURCE}" || values["LANGBOT_DEPLOY_TOOL_VERSION"] != "${LANGBOT_DEPLOY_TOOL_VERSION}" {
				t.Fatalf("environment = %v", values)
			}
			if strings.Contains(environment, "KEEP") && values["KEEP"] != "value" {
				t.Fatal("custom environment changed")
			}
			if !strings.HasPrefix(string(updated), "# custom compose\n") || !strings.HasSuffix(string(updated), "    ports: ['15300:5300'] # keep\n") {
				t.Fatal("unrelated bytes changed")
			}
			again, err := addDeployEnvironment(updated)
			if err != nil || string(again) != string(updated) {
				t.Fatalf("not idempotent: %v\n%s", err, again)
			}
		})
	}
	record := Record{ComposeFile: filepath.Join(t.TempDir(), "compose.yaml")}
	if err := WriteDeployIdentity(record, "0.4.1"); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(filepath.Dir(record.ComposeFile), ".env")); err != nil || !strings.Contains(string(content), "LANGBOT_DEPLOY_TOOL_VERSION=0.4.1") {
		t.Fatalf("adopted env: %s, %v", content, err)
	}
}
