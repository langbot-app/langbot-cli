package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/config"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type taskRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn taskRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type knowledgeJournalFixture struct {
	noUploadRoute      bool
	t                  *testing.T
	mu                 sync.Mutex
	server             *httptest.Server
	service            *Service
	config, file, path string
	instance           string
	workspace          string
	member             bool
	uploads, stores    int
	tasks              int
	storeStatus        int
	documentStatus     string
	unknownUpload      bool
	unknownStore       bool
	omitDocument       bool
	documents          []any
	blockStore         chan struct{}
}

func newKnowledgeJournalFixture(t *testing.T) *knowledgeJournalFixture {
	t.Helper()
	dir := t.TempDir()
	fixture := &knowledgeJournalFixture{t: t, config: filepath.Join(dir, "config.yaml"),
		file: filepath.Join(dir, "documents", "a.txt"), path: filepath.Join(dir, "progress.json"),
		instance: "instance-a", workspace: "workspace-a", documentStatus: "completed", documents: []any{}}
	if err := os.MkdirAll(filepath.Dir(fixture.file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.file, []byte("journal document"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	fixture.service = New(Dependencies{
		LookupEnv: func(name string) (string, bool) {
			if name == "LANGBOT_USER_TOKEN" && fixture.member {
				return "journal-member-token", true
			}
			return "journal-api-secret", name == "KEY"
		},
		DefaultConfigPath: func() (string, error) { return fixture.config, nil },
	})
	if _, err := fixture.service.ContextAdd("production", fixture.server.URL, "KEY", "workspace-a", ""); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture *knowledgeJournalFixture) serveHTTP(w http.ResponseWriter, request *http.Request) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if fixture.member && (request.Header.Get("Authorization") != "Bearer journal-member-token" || request.Header.Get("X-API-Key") != "") {
		fixture.t.Errorf("member credential was lost for %s", request.URL.Path)
	}
	write := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data}) }
	switch request.URL.Path {
	case "/api/v1/workspaces/current":
		workspace := map[string]any{"uuid": fixture.workspace}
		if fixture.instance != "" {
			workspace["instance_uuid"] = fixture.instance
		}
		write(map[string]any{"workspace": workspace, "permissions": []string{"resource.manage", "resource.view"}})
	case "/api/v1/operations":
		fmt.Fprint(w, `{"operations":[{"id":"knowledge.file.store"},{"id":"knowledge.base.get"},{"id":"knowledge.file.list"}]}`)
	case "/api/v1/system/context":
		write(map[string]any{"instance_uuid": fixture.instance, "workspace_uuid": fixture.workspace, "api_key_id": "key-a", "permissions": []string{"resource.manage", "resource.view"}})
	case "/api/v1/system/capabilities":
		fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{"knowledge_base.file.store":{"supported":true},"knowledge_base.get":{"supported":true},"file.document.upload":{"supported":true},"knowledge_base.file.list":{"supported":true},"task.get":{"supported":true}}}}`)
	case "/api/v1/knowledge/bases/kb-a":
		write(map[string]any{"base": map[string]any{"uuid": "kb-a", "name": "KB"}})
	case "/api/v1/files/documents":
		if request.Method == http.MethodOptions {
			if !fixture.noUploadRoute {
				w.Header().Set("Allow", "OPTIONS, POST")
			}
			w.WriteHeader(200)
			return
		}
		fixture.uploads++
		fixture.assertIntent("upload_intent")
		if fixture.unknownUpload {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"code":"fixture_failure","data":null}`)
			return
		}
		file, header, err := request.FormFile("file")
		if err != nil {
			fixture.t.Errorf("upload multipart: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		content, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil || string(content) != "journal document" || header.Filename != filepath.Base(fixture.file) {
			fixture.t.Errorf("upload content=%q name=%s err=%v", content, header.Filename, err)
		}
		write(map[string]any{"file_id": fmt.Sprintf("upload-%d", fixture.uploads)})
	case "/api/v1/knowledge/bases/kb-a/files":
		if request.Method == http.MethodGet {
			write(map[string]any{"files": fixture.documents})
			return
		}
		fixture.stores++
		fixture.assertIntent("store_intent")
		if fixture.storeStatus != 0 {
			w.WriteHeader(fixture.storeStatus)
			fmt.Fprint(w, `{"code":"fixture_rejected","data":null}`)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			fixture.t.Errorf("store body: %v", err)
		}
		if body["parser_plugin_id"] != "parser-a" {
			fixture.t.Errorf("store parser=%v", body["parser_plugin_id"])
		}
		if !fixture.omitDocument {
			fixture.documents = append(fixture.documents, map[string]any{"uuid": fmt.Sprintf("document-%d", fixture.stores),
				"file_name": body["file_id"], "kb_id": "kb-a", "workspace_uuid": fixture.workspace, "status": fixture.documentStatus})
		}
		if fixture.blockStore != nil {
			close(fixture.blockStore)
			fixture.blockStore = nil
			fixture.mu.Unlock()
			<-request.Context().Done()
			fixture.mu.Lock()
			return
		}
		if fixture.unknownStore {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"code":"fixture_failure","data":null}`)
			return
		}
		// A restarted Core may reuse this task ID for another document.
		write(map[string]any{"task_id": 1})
	default:
		if strings.HasPrefix(request.URL.Path, "/api/v1/system/tasks/") {
			fixture.tasks++
			fmt.Fprint(w, `{"code":0,"data":{"id":1,"task_type":"user","kind":"knowledge_base.store","status":"succeeded","error":null,"result":{"file_id":"unrelated-upload"},"created_at":1}}`)
			return
		}
		fixture.t.Errorf("unexpected journal request %s %s", request.Method, request.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (fixture *knowledgeJournalFixture) assertIntent(phase string) {
	fixture.t.Helper()
	journal, err := readIngestJournal(fixture.path, true)
	if err != nil || len(journal.Files) != 1 || journal.Files[0].Phase != phase {
		fixture.t.Errorf("side effect before durable %s intent: %+v, %v", phase, journal, err)
	}
}

func (fixture *knowledgeJournalFixture) run(resume bool, wait WaitOptions) (Result, error) {
	fixture.t.Helper()
	return fixture.service.KnowledgeBaseIngestJournal(context.Background(), "kb-a", []string{fixture.file}, "", "parser-a", fixture.path, resume, false, wait, CheckOptions{ContextSet: true, Context: "production"})
}

func (fixture *knowledgeJournalFixture) journal() ingestJournal {
	fixture.t.Helper()
	journal, err := readIngestJournal(fixture.path, true)
	if err != nil {
		fixture.t.Fatal(err)
	}
	return journal
}

func TestKnowledgeJournalRecordsEvidenceAndSkipsCompletedWrites(t *testing.T) {
	fixture := newKnowledgeJournalFixture(t)
	value, err := fixture.run(false, WaitOptions{Wait: true})
	if err != nil {
		t.Fatal(err)
	}
	journal := fixture.journal()
	row := journal.Files[0]
	if journal.Version != 1 || journal.InstanceUUID != "instance-a" || journal.WorkspaceUUID != "workspace-a" || journal.KnowledgeBase != "kb-a" || journal.Parser != "parser-a" || row.Path != "documents/a.txt" || row.Size != 16 || row.SHA256 != ingestContentHash([]byte("journal document")) || row.FileID != "upload-1" || row.TaskID != "1" || row.DocumentUUID != "document-1" || row.Phase != "completed" {
		t.Fatalf("journal=%+v row=%+v", journal, row)
	}
	content, _ := os.ReadFile(fixture.path)
	output, _ := json.Marshal(value)
	if strings.Contains(string(content), "journal-api-secret") || strings.Contains(string(output), "journal-api-secret") {
		t.Fatal("journal or output contains credential")
	}
	if _, err := fixture.run(true, WaitOptions{Wait: true}); err != nil || fixture.uploads != 1 || fixture.stores != 1 || fixture.tasks != 0 {
		t.Fatalf("resume err=%v upload/store/task=%d/%d/%d", err, fixture.uploads, fixture.stores, fixture.tasks)
	}
	if _, err := fixture.run(false, WaitOptions{}); result.AsError(err).Kind != "precondition" {
		t.Fatalf("existing journal overwritten: %v", err)
	}
}

func TestKnowledgeJournalMemberUnsupportedUploadStopsBeforeWrites(t *testing.T) {
	fixture := newKnowledgeJournalFixture(t)
	fixture.member, fixture.noUploadRoute = true, true
	_, err := fixture.service.KnowledgeBaseIngestJournal(config.AsMember(context.Background()), "kb-a", []string{fixture.file}, "", "parser-a", fixture.path, false, false, WaitOptions{}, CheckOptions{ContextSet: true, Context: "production"})
	if err == nil || result.AsError(err).Kind != "precondition" || fixture.uploads != 0 || fixture.stores != 0 {
		t.Fatalf("err=%v uploads=%d stores=%d", err, fixture.uploads, fixture.stores)
	}
}

func TestKnowledgeJournalMemberIdentityAndBearer(t *testing.T) {
	for _, hasInstance := range []bool{true, false} {
		t.Run(fmt.Sprintf("instance_%t", hasInstance), func(t *testing.T) {
			fixture := newKnowledgeJournalFixture(t)
			fixture.member = true
			if !hasInstance {
				fixture.instance = ""
			}
			for _, resume := range []bool{false, true} {
				_, err := fixture.service.KnowledgeBaseIngestJournal(config.AsMember(context.Background()), "kb-a", []string{fixture.file}, "", "parser-a", fixture.path, resume, false, WaitOptions{Wait: true}, CheckOptions{ContextSet: true, Context: "production"})
				if hasInstance {
					if err != nil || fixture.uploads != 1 || fixture.stores != 1 || fixture.journal().InstanceUUID != "instance-a" {
						t.Fatalf("member journal=%+v err=%v writes=%d/%d", fixture.journal(), err, fixture.uploads, fixture.stores)
					}
					continue
				}
				if result.AsError(err).Kind != "incompatible" || fixture.uploads != 0 || fixture.stores != 0 {
					t.Fatalf("unbound member identity reached writes: %v %d/%d", err, fixture.uploads, fixture.stores)
				}
				if _, err := os.Stat(fixture.path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("unbound member created progress journal: %v", err)
				}
				break
			}
		})
	}
}

func TestWriteClientPreservesResolvedCredentialAndConnection(t *testing.T) {
	for _, connection := range []config.Connection{
		{Endpoint: "https://example.test", APIKey: "api-secret", Timeout: 2 * time.Second},
		{Endpoint: "https://example.test", UserToken: "member-secret", Timeout: 3 * time.Second},
	} {
		transport := taskRoundTripFunc(func(*http.Request) (*http.Response, error) { return taskResponse(`{}`), nil })
		service := New(Dependencies{Transport: transport})
		client, target := service.writeClient(WritePreflight{Connection: connection})
		if client.Transport == nil || target.Endpoint != connection.Endpoint || target.APIKey != connection.APIKey || target.UserToken != connection.UserToken || target.Timeout != connection.Timeout {
			t.Fatalf("resolved target changed: %+v", target)
		}
	}
}

func TestKnowledgeJournalReusesConfirmedUpload(t *testing.T) {
	fixture := newKnowledgeJournalFixture(t)
	fixture.storeStatus = http.StatusBadRequest
	if _, err := fixture.run(false, WaitOptions{}); err == nil {
		t.Fatal("expected store rejection")
	}
	row := fixture.journal().Files[0]
	if row.Phase != "uploaded" || row.FileID != "upload-1" || row.TaskID != "" {
		t.Fatalf("lost upload evidence: %+v", row)
	}
	fixture.storeStatus = 0
	if _, err := fixture.run(true, WaitOptions{}); err != nil || fixture.uploads != 1 || fixture.stores != 2 || fixture.journal().Files[0].Phase != "completed" {
		t.Fatalf("resume err=%v upload/store=%d/%d", err, fixture.uploads, fixture.stores)
	}
}

func TestKnowledgeJournalReconcilesUnknownSubmission(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing_document_%t", missing), func(t *testing.T) {
			fixture := newKnowledgeJournalFixture(t)
			fixture.unknownStore, fixture.omitDocument = true, missing
			if _, err := fixture.run(false, WaitOptions{}); err == nil || fixture.journal().Files[0].Phase != "store_intent" {
				t.Fatalf("unknown submission was lost: %v %+v", err, fixture.journal())
			}
			value, err := fixture.run(true, WaitOptions{})
			if missing && result.AsError(err).Type != "journal_unresolved" || !missing && err != nil {
				t.Fatalf("reconciliation=%+v err=%v", value, err)
			}
			if fixture.uploads != 1 || fixture.stores != 1 || fixture.tasks != 0 {
				t.Fatalf("uncertain submission repeated: %d/%d/%d", fixture.uploads, fixture.stores, fixture.tasks)
			}
			row := fixture.journal().Files[0]
			if !missing && (row.DocumentUUID != "document-1" || row.Phase != "completed") {
				t.Fatalf("missing stable evidence: %+v", row)
			}
		})
	}
}

func TestKnowledgeJournalUnknownUploadNeverRepeats(t *testing.T) {
	fixture := newKnowledgeJournalFixture(t)
	fixture.unknownUpload = true
	if _, err := fixture.run(false, WaitOptions{}); err == nil {
		t.Fatal("expected unknown upload")
	}
	if _, err := fixture.run(true, WaitOptions{}); result.AsError(err).Type != "journal_unresolved" || fixture.uploads != 1 || fixture.stores != 0 {
		t.Fatalf("repeated uncertain upload: %v %d/%d", err, fixture.uploads, fixture.stores)
	}
}

func TestKnowledgeJournalUsesDocumentIdentityAcrossTaskReuse(t *testing.T) {
	fixture := newKnowledgeJournalFixture(t)
	fixture.documentStatus = "processing"
	_, err := fixture.run(false, WaitOptions{Wait: true, PollInterval: 5 * time.Millisecond, WaitTimeout: 75 * time.Millisecond})
	if result.AsError(err).Type != "wait_timeout" || fixture.journal().Files[0].Phase != "submitted" || fixture.tasks != 0 {
		t.Fatalf("unrelated succeeded task completed this document: %v %+v tasks=%d", err, fixture.journal(), fixture.tasks)
	}
	fixture.mu.Lock()
	fixture.documents[0].(map[string]any)["status"] = "completed"
	fixture.mu.Unlock()
	if _, err := fixture.run(true, WaitOptions{}); err != nil || fixture.uploads != 1 || fixture.stores != 1 || fixture.tasks != 0 {
		t.Fatalf("stable document resume: %v %d/%d/%d", err, fixture.uploads, fixture.stores, fixture.tasks)
	}
	fixture.mu.Lock()
	fixture.documents[0].(map[string]any)["uuid"] = "replacement-document"
	fixture.mu.Unlock()
	if _, err := fixture.run(true, WaitOptions{}); result.AsError(err).Type != "journal_unresolved" || fixture.journal().Files[0].DocumentUUID != "document-1" {
		t.Fatalf("bound identity silently replaced: %v %+v", err, fixture.journal())
	}
}

func TestKnowledgeJournalRefusesAmbiguousOrForeignDocuments(t *testing.T) {
	for _, kind := range []string{"duplicate", "workspace", "knowledge-base", "upload"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newKnowledgeJournalFixture(t)
			fixture.unknownStore = true
			_, _ = fixture.run(false, WaitOptions{})
			fixture.mu.Lock()
			document := fixture.documents[0].(map[string]any)
			switch kind {
			case "duplicate":
				fixture.documents = append(fixture.documents, map[string]any{"uuid": "document-other", "file_name": "upload-1", "status": "completed"})
			case "workspace":
				document["workspace_uuid"] = "workspace-other"
			case "knowledge-base":
				document["kb_id"] = "kb-other"
			case "upload":
				document["file_name"] = "upload-other"
			}
			fixture.mu.Unlock()
			if _, err := fixture.run(true, WaitOptions{}); result.AsError(err).Type != "journal_unresolved" || fixture.uploads != 1 || fixture.stores != 1 {
				t.Fatalf("accepted wrong document: %v %+v", err, fixture.journal())
			}
		})
	}
}

func TestKnowledgeJournalValidatesWholeGroupAndTargetBeforeWrites(t *testing.T) {
	for _, drift := range []string{"hash", "size", "path", "parser", "knowledge-base", "instance", "workspace", "phase", "missing-input"} {
		t.Run(drift, func(t *testing.T) {
			fixture := newKnowledgeJournalFixture(t)
			fixture.storeStatus = http.StatusBadRequest
			_, _ = fixture.run(false, WaitOptions{})
			journal := fixture.journal()
			switch drift {
			case "hash":
				if err := os.WriteFile(fixture.file, []byte("changed document"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "size":
				journal.Files[0].Size++
			case "path":
				journal.Files[0].Path = "other.txt"
			case "parser":
				journal.Parser = "other-parser"
			case "knowledge-base":
				journal.KnowledgeBase = "other-kb"
			case "instance":
				journal.InstanceUUID = "other-instance"
			case "workspace":
				journal.WorkspaceUUID = "other-workspace"
			case "phase":
				journal.Files[0].Phase = "planned"
			case "missing-input":
				journal.Files = append(journal.Files, ingestJournalFile{Path: "missing.txt", Phase: "planned"})
			}
			if err := (&ingestJournalRun{journal: journal, path: fixture.path}).save(); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.run(true, WaitOptions{}); err == nil || fixture.uploads != 1 || fixture.stores != 1 {
				t.Fatalf("drift reached server writes: %v %d/%d", err, fixture.uploads, fixture.stores)
			}
		})
	}
}

func TestKnowledgeJournalChecksEveryInputBeforeResumingFirstFile(t *testing.T) {
	fixture := newKnowledgeJournalFixture(t)
	second := filepath.Join(filepath.Dir(fixture.file), "b.txt")
	if err := os.WriteFile(second, []byte("second document"), 0o600); err != nil {
		t.Fatal(err)
	}
	journal := ingestJournal{Version: 1, InstanceUUID: "instance-a", WorkspaceUUID: "workspace-a", KnowledgeBase: "kb-a", Parser: "parser-a", Files: []ingestJournalFile{
		{Path: "documents/a.txt", Size: 16, SHA256: ingestContentHash([]byte("journal document")), Phase: "planned"},
		{Path: "documents/b.txt", Size: 15, SHA256: ingestContentHash([]byte("second document")), Phase: "planned"},
	}}
	if err := (&ingestJournalRun{journal: journal, path: fixture.path}).save(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("changed content"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.service.KnowledgeBaseIngestJournal(context.Background(), "kb-a", []string{fixture.file, second}, "", "parser-a", fixture.path, true, false, WaitOptions{}, CheckOptions{ContextSet: true, Context: "production"})
	if result.AsError(err).Kind != "precondition" || fixture.uploads != 0 || fixture.stores != 0 {
		t.Fatalf("later changed input allowed earlier write: %v %d/%d", err, fixture.uploads, fixture.stores)
	}
}

func TestKnowledgeJournalCancellationPreservesUploadedEvidence(t *testing.T) {
	fixture := newKnowledgeJournalFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := New(Dependencies{
		LookupEnv:         func(name string) (string, bool) { return "journal-api-secret", name == "KEY" },
		DefaultConfigPath: func() (string, error) { return fixture.config, nil },
		Transport: taskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			response, err := http.DefaultTransport.RoundTrip(request)
			if err == nil && request.URL.Path == "/api/v1/files/documents" {
				body, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if readErr != nil {
					return nil, readErr
				}
				response.Body = io.NopCloser(strings.NewReader(string(body)))
				cancel()
			}
			return response, err
		}),
	})
	_, err := service.KnowledgeBaseIngestJournal(ctx, "kb-a", []string{fixture.file}, "", "parser-a", fixture.path, false, false, WaitOptions{}, CheckOptions{ContextSet: true, Context: "production"})
	if result.AsError(err).Type != "wait_cancelled" || fixture.uploads != 1 || fixture.stores != 0 || fixture.journal().Files[0].Phase != "uploaded" {
		t.Fatalf("cancellation lost confirmed upload: %v %+v %d/%d", err, fixture.journal(), fixture.uploads, fixture.stores)
	}
	if _, err := fixture.run(true, WaitOptions{}); err != nil || fixture.uploads != 1 || fixture.stores != 1 || fixture.journal().Files[0].Phase != "completed" {
		t.Fatalf("cancelled upload was repeated: %v %+v", err, fixture.journal())
	}
}

func TestKnowledgeJournalConfirmedDocumentFailureRemainsBound(t *testing.T) {
	fixture := newKnowledgeJournalFixture(t)
	fixture.documentStatus = "failed"
	if _, err := fixture.run(false, WaitOptions{Wait: true}); result.AsError(err).Type != "document_failed" || fixture.journal().Files[0].Phase != "failed" {
		t.Fatalf("failed document accepted: %v %+v", err, fixture.journal())
	}
	if _, err := fixture.run(true, WaitOptions{}); result.AsError(err).Type != "document_failed" || fixture.uploads != 1 || fixture.stores != 1 {
		t.Fatalf("failed document resubmitted: %v %d/%d", err, fixture.uploads, fixture.stores)
	}
}

func TestKnowledgeJournalDryRunAndDirectoryJournalExclusion(t *testing.T) {
	fixture := newKnowledgeJournalFixture(t)
	fixture.path = filepath.Join(filepath.Dir(fixture.file), "progress.json")
	value, err := fixture.service.KnowledgeBaseIngestJournal(context.Background(), "kb-a", nil, filepath.Dir(fixture.file), "parser-a", fixture.path, false, true, WaitOptions{}, CheckOptions{ContextSet: true, Context: "production"})
	if err != nil || fixture.uploads != 0 || fixture.stores != 0 || value.Data.(map[string]any)["server_write"] != false {
		t.Fatalf("dry-run wrote server: %+v %v", value, err)
	}
	if _, err := os.Stat(fixture.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run wrote journal: %v", err)
	}
	if _, err := os.Stat(fixture.path + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("dry-run created a lock file: %v", err)
	}
	absentDir := filepath.Join(filepath.Dir(fixture.file), "preview-only")
	_, err = fixture.service.KnowledgeBaseIngestJournal(context.Background(), "kb-a", []string{fixture.file}, "", "parser-a", filepath.Join(absentDir, "progress.json"), false, true, WaitOptions{}, CheckOptions{ContextSet: true, Context: "production"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(absentDir); !os.IsNotExist(err) {
		t.Fatalf("dry-run created a parent directory: %v", err)
	}

	for _, resume := range []bool{false, true} {
		if _, err := fixture.service.KnowledgeBaseIngestJournal(context.Background(), "kb-a", nil, filepath.Dir(fixture.file), "parser-a", fixture.path, resume, false, WaitOptions{}, CheckOptions{ContextSet: true, Context: "production"}); err != nil {
			t.Fatal(err)
		}
	}
	if fixture.uploads != 1 || len(fixture.journal().Files) != 1 {
		t.Fatalf("included own journal/lock: %d %+v", fixture.uploads, fixture.journal())
	}
}

func TestKnowledgeJournalRejectsZipAndMalformedJournal(t *testing.T) {
	fixture := newKnowledgeJournalFixture(t)
	zip := filepath.Join(filepath.Dir(fixture.file), "archive.zip")
	if err := os.WriteFile(zip, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.KnowledgeBaseIngestJournal(context.Background(), "kb-a", []string{fixture.file, zip}, "", "parser-a", fixture.path, false, false, WaitOptions{}, CheckOptions{}); result.AsError(err).Kind != "input" || fixture.uploads != 0 {
		t.Fatalf("ZIP accepted in journal mode: %v", err)
	}
	for _, content := range []string{`{`, `{"unknown":true}`, `{"version":1} {}`} {
		if err := os.WriteFile(fixture.path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.run(true, WaitOptions{}); result.AsError(err).Kind != "input" || fixture.uploads != 0 {
			t.Fatalf("malformed journal accepted: %v", err)
		}
	}
}

func TestKnowledgeJournalProcessHelper(t *testing.T) {
	if os.Getenv("LBCTL_JOURNAL_TEST_CHILD") != "1" {
		return
	}
	service := New(Dependencies{
		LookupEnv:         func(name string) (string, bool) { return "journal-api-secret", name == "KEY" },
		DefaultConfigPath: func() (string, error) { return os.Getenv("LBCTL_JOURNAL_TEST_CONFIG"), nil },
	})
	_, err := service.KnowledgeBaseIngestJournal(context.Background(), "kb-a", []string{os.Getenv("LBCTL_JOURNAL_TEST_FILE")}, "", "parser-a", os.Getenv("LBCTL_JOURNAL_TEST_PATH"), false, false, WaitOptions{}, CheckOptions{ContextSet: true, Context: "production"})
	t.Fatalf("child returned before simulated interruption: %v", err)
}

func TestKnowledgeJournalSurvivesProcessInterruptionAndLocksAcrossProcesses(t *testing.T) {
	fixture := newKnowledgeJournalFixture(t)
	entered := make(chan struct{})
	fixture.blockStore = entered
	command := exec.Command(os.Args[0], "-test.run=^TestKnowledgeJournalProcessHelper$")
	command.Env = append(os.Environ(), "LBCTL_JOURNAL_TEST_CHILD=1", "LBCTL_JOURNAL_TEST_CONFIG="+fixture.config,
		"LBCTL_JOURNAL_TEST_FILE="+fixture.file, "LBCTL_JOURNAL_TEST_PATH="+fixture.path)
	var output strings.Builder
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill() })
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("child did not submit: %s", output.String())
	}
	if _, err := fixture.run(true, WaitOptions{}); result.AsError(err).Kind != "precondition" || fixture.uploads != 1 || fixture.stores != 1 {
		t.Fatalf("concurrent process acquired same journal: %v %d/%d", err, fixture.uploads, fixture.stores)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	if row := fixture.journal().Files[0]; row.Phase != "store_intent" || row.FileID != "upload-1" || row.TaskID != "" {
		t.Fatalf("interrupted submission lost intent: %+v", row)
	}
	if _, err := fixture.run(true, WaitOptions{}); err != nil || fixture.uploads != 1 || fixture.stores != 1 || fixture.journal().Files[0].Phase != "completed" {
		t.Fatalf("restart duplicated interrupted write: %v %d/%d %+v", err, fixture.uploads, fixture.stores, fixture.journal())
	}
}

func taskResponse(body string) *http.Response {
	return taskHTTPResponse(http.StatusOK, body)
}

func taskHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestWaitForTaskPollsUntilSuccess(t *testing.T) {
	var calls atomic.Int32
	client := api.Client{Transport: taskRoundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return taskResponse(`{"code":0,"data":{"id":1,"task_type":"user","kind":"knowledge_base.store","status":"running","error":null,"result":null,"created_at":1.0}}`), nil
		}
		return taskResponse(`{"code":0,"data":{"id":1,"task_type":"user","kind":"knowledge_base.store","status":"succeeded","error":null,"result":{"file_id":"file-a"},"created_at":1.0}}`), nil
	})}
	task, err := waitForTask(context.Background(), client, api.Target{Endpoint: "http://example.test"}, "1", WaitOptions{
		PollInterval: time.Millisecond,
		WaitTimeout:  100 * time.Millisecond,
	})
	if err != nil || task.Status != "succeeded" || calls.Load() != 2 {
		t.Fatalf("waitForTask() = %#v, error = %v, calls = %d", task, err, calls.Load())
	}
}

func TestWaitForTaskDistinguishesTimeoutAndCancellation(t *testing.T) {
	client := api.Client{Transport: taskRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return taskResponse(`{"code":0,"data":{"id":1,"task_type":"user","kind":"knowledge_base.store","status":"running","error":null,"result":null,"created_at":1.0}}`), nil
	})}
	_, err := waitForTask(context.Background(), client, api.Target{Endpoint: "http://example.test"}, "1", WaitOptions{
		PollInterval: 50 * time.Millisecond,
		WaitTimeout:  5 * time.Millisecond,
	})
	if result.AsError(err).Type != "wait_timeout" {
		t.Fatalf("timeout error = %+v", result.AsError(err))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = waitForTask(ctx, client, api.Target{Endpoint: "http://example.test"}, "1", WaitOptions{
		PollInterval: time.Millisecond,
		WaitTimeout:  time.Second,
	})
	if result.AsError(err).Type != "wait_cancelled" || !strings.Contains(result.AsError(err).Message, "未被取消") {
		t.Fatalf("cancel error = %+v", result.AsError(err))
	}
}

func TestWaitForTaskPreservesNetworkErrors(t *testing.T) {
	client := api.Client{Transport: taskRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport failure")
	})}
	_, err := waitForTask(context.Background(), client, api.Target{Endpoint: "http://example.test"}, "1", WaitOptions{
		PollInterval: time.Millisecond,
		WaitTimeout:  time.Second,
	})
	if result.AsError(err).Kind != "network" {
		t.Fatalf("network error = %+v", result.AsError(err))
	}
}

func TestWaitForTaskTimeoutIncludesInFlightRequest(t *testing.T) {
	client := api.Client{Transport: taskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	started := time.Now()
	_, err := waitForTask(context.Background(), client, api.Target{
		Endpoint: "http://example.test",
		Timeout:  time.Second,
	}, "1", WaitOptions{
		PollInterval: time.Second,
		WaitTimeout:  10 * time.Millisecond,
	})
	if result.AsError(err).Type != "wait_timeout" {
		t.Fatalf("timeout error = %+v", result.AsError(err))
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("wait timeout did not bound the in-flight request: %s", elapsed)
	}
}

func TestKnowledgeBaseIngestRunsUploadAndStoreOnce(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	filePath := filepath.Join(t.TempDir(), "document.txt")
	if err := os.WriteFile(filePath, []byte("document body"), 0600); err != nil {
		t.Fatal(err)
	}
	var uploadCalls, storeCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{"knowledge_base.file.store":{"supported":true},"knowledge_base.get":{"supported":true},"file.document.upload":{"supported":true},"task.get":{"supported":true}}}}`)
		case "/api/v1/knowledge/bases/kb-a":
			fmt.Fprint(w, `{"code":0,"data":{"base":{"uuid":"kb-a","name":"KB"}}}`)
		case "/api/v1/files/documents":
			uploadCalls++
			if err := request.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("ParseMultipartForm() error = %v", err)
			}
			fmt.Fprint(w, `{"code":0,"data":{"file_id":"file-a"}}`)
		case "/api/v1/knowledge/bases/kb-a/files":
			storeCalls++
			fmt.Fprint(w, `{"code":0,"data":{"task_id":17}}`)
		case "/api/v1/system/tasks/17":
			fmt.Fprint(w, `{"code":0,"data":{"id":17,"task_type":"user","kind":"knowledge_base.store","status":"succeeded","error":null,"result":{"stored":true},"created_at":1.0}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	lookup := func(name string) (string, bool) {
		if name == "KEY" {
			return "secret", true
		}
		return "", false
	}
	service := New(Dependencies{LookupEnv: lookup, DefaultConfigPath: func() (string, error) { return configPath, nil }})
	if _, err := service.ContextAdd("production", server.URL, "KEY", "workspace-a", ""); err != nil {
		t.Fatalf("ContextAdd() error = %v", err)
	}
	value, err := service.KnowledgeBaseIngest(context.Background(), "kb-a", filePath, "", false, WaitOptions{
		Wait: true, PollInterval: time.Millisecond, WaitTimeout: time.Second,
	}, CheckOptions{ContextSet: true, Context: "production"})
	if err != nil {
		t.Fatalf("KnowledgeBaseIngest() error = %v", err)
	}
	data := value.Data.(map[string]any)
	if data["step"] != "completed" || data["file_id"] != "file-a" || data["task_id"] != "17" {
		t.Fatalf("KnowledgeBaseIngest() data = %#v", data)
	}
	if uploadCalls != 1 || storeCalls != 1 {
		t.Fatalf("upload/store calls = %d/%d", uploadCalls, storeCalls)
	}
}

func TestKnowledgeBaseIngestPreservesPartialAndUnknownSubmissionState(t *testing.T) {
	for _, test := range []struct {
		name          string
		storeResponse *http.Response
		storeError    error
		wantType      string
		wantStep      string
		wantSubmitted any
	}{
		{
			name:          "known rejection",
			storeResponse: taskHTTPResponse(http.StatusBadRequest, `{"code":"invalid_file","data":null}`),
			wantType:      "partial_write",
			wantStep:      "store_failed",
			wantSubmitted: false,
		},
		{
			name:          "unknown result",
			storeError:    errors.New("connection reset after request"),
			wantType:      "result_unknown",
			wantStep:      "store_unknown",
			wantSubmitted: "unknown",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			filePath := filepath.Join(t.TempDir(), "document.txt")
			if err := os.WriteFile(filePath, []byte("document body"), 0o600); err != nil {
				t.Fatal(err)
			}
			var uploadCalls, storeCalls atomic.Int32
			transport := taskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/api/v1/system/context":
					return taskResponse(`{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`), nil
				case "/api/v1/system/capabilities":
					return taskResponse(`{"code":0,"data":{"schema_version":1,"operations":{"knowledge_base.file.store":{"supported":true},"knowledge_base.get":{"supported":true},"file.document.upload":{"supported":true}}}}`), nil
				case "/api/v1/knowledge/bases/kb-a":
					return taskResponse(`{"code":0,"data":{"base":{"uuid":"kb-a","name":"KB"}}}`), nil
				case "/api/v1/files/documents":
					uploadCalls.Add(1)
					return taskResponse(`{"code":0,"data":{"file_id":"file-a"}}`), nil
				case "/api/v1/knowledge/bases/kb-a/files":
					storeCalls.Add(1)
					return test.storeResponse, test.storeError
				default:
					return nil, fmt.Errorf("unexpected path %s", request.URL.Path)
				}
			})
			service := New(Dependencies{
				LookupEnv:         func(name string) (string, bool) { return "secret", name == "KEY" },
				DefaultConfigPath: func() (string, error) { return configPath, nil },
				Transport:         transport,
			})
			if _, err := service.ContextAdd("production", "http://example.test", "KEY", "workspace-a", ""); err != nil {
				t.Fatalf("ContextAdd() error = %v", err)
			}
			value, err := service.KnowledgeBaseIngest(
				context.Background(), "kb-a", filePath, "", false, WaitOptions{},
				CheckOptions{ContextSet: true, Context: "production"},
			)
			failure := result.AsError(err)
			if failure.Type != test.wantType {
				t.Fatalf("error = %+v, want type %q", failure, test.wantType)
			}
			data := value.Data.(map[string]any)
			if data["step"] != test.wantStep || data["submitted"] != test.wantSubmitted || data["file_id"] != "file-a" {
				t.Fatalf("data = %#v", data)
			}
			if uploadCalls.Load() != 1 || storeCalls.Load() != 1 {
				t.Fatalf("upload/store calls = %d/%d", uploadCalls.Load(), storeCalls.Load())
			}
		})
	}
}

func TestKnowledgeBaseIngestWaitRequiresTaskCapabilityBeforeWriting(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	filePath := filepath.Join(t.TempDir(), "document.txt")
	if err := os.WriteFile(filePath, []byte("document body"), 0o600); err != nil {
		t.Fatal(err)
	}
	var writes atomic.Int32
	transport := taskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v1/system/context":
			return taskResponse(`{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`), nil
		case "/api/v1/system/capabilities":
			return taskResponse(`{"code":0,"data":{"schema_version":1,"operations":{"knowledge_base.file.store":{"supported":true},"knowledge_base.get":{"supported":true},"file.document.upload":{"supported":true}}}}`), nil
		case "/api/v1/knowledge/bases/kb-a":
			return taskResponse(`{"code":0,"data":{"base":{"uuid":"kb-a","name":"KB"}}}`), nil
		default:
			writes.Add(1)
			return nil, fmt.Errorf("unexpected request %s", request.URL.Path)
		}
	})
	service := New(Dependencies{
		LookupEnv:         func(name string) (string, bool) { return "secret", name == "KEY" },
		DefaultConfigPath: func() (string, error) { return configPath, nil },
		Transport:         transport,
	})
	if _, err := service.ContextAdd("production", "http://example.test", "KEY", "workspace-a", ""); err != nil {
		t.Fatalf("ContextAdd() error = %v", err)
	}
	if _, err := service.KnowledgeBaseIngest(
		context.Background(), "kb-a", filePath, "", true, WaitOptions{},
		CheckOptions{ContextSet: true, Context: "production"},
	); err != nil {
		t.Fatalf("dry-run should not require task capability: %v", err)
	}
	_, err := service.KnowledgeBaseIngest(
		context.Background(), "kb-a", filePath, "", false, WaitOptions{Wait: true},
		CheckOptions{ContextSet: true, Context: "production"},
	)
	if failure := result.AsError(err); failure.Kind != "precondition" {
		t.Fatalf("error = %+v, want precondition", failure)
	}
	if writes.Load() != 0 {
		t.Fatalf("missing task capability allowed %d resource requests", writes.Load())
	}
}

func TestCollectIngestFilesRecursesDeduplicatesAndSkipsDotEntries(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "nested/b.md", "comma,name.txt", ".env", ".git/config", "nested/.hidden"} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Symlink creation may be unavailable to non-administrator Windows users.
	_ = os.Symlink(filepath.Join(dir, "a.txt"), filepath.Join(dir, "linked.txt"))
	files, err := collectIngestFiles(context.Background(), []string{filepath.Join(dir, "a.txt"), filepath.Join(dir, ".", "a.txt")}, dir)
	if err != nil || len(files) != 3 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	for index, name := range []string{"a.txt", "comma,name.txt", "b.md"} {
		if filepath.Base(files[index].path) != name {
			t.Fatalf("unexpected order or duplicate: %v", files)
		}
	}
	link := filepath.Join(t.TempDir(), "linked-directory")
	if os.Symlink(dir, link) == nil {
		if _, err := collectIngestFiles(context.Background(), nil, link); err == nil {
			t.Fatal("followed a symlink supplied as the directory root")
		}
	}
}

func TestKnowledgeBatchRejectsInvalidInputsBeforeAnyRequest(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.txt")
	archive := filepath.Join(dir, "archive.zip")
	large := filepath.Join(dir, "large.pdf")
	for _, path := range []string{valid, archive, large} {
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(large, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(maxExtensionUploadBytes + 1)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	for _, test := range []struct {
		name  string
		files []string
		dir   string
		wait  bool
	}{
		{"missing", nil, "", false},
		{"stdin", []string{valid, "-"}, "", false},
		{"missing file", []string{valid, filepath.Join(dir, "missing.txt")}, "", false},
		{"oversized", []string{valid, large}, "", false},
		{"directory as file", []string{valid, dir}, "", false},
		{"file as directory", nil, valid, false},
		{"empty directory", nil, empty, false},
		{"archive batch wait", []string{valid, archive}, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests int
			service := New(Dependencies{Transport: taskRoundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				return nil, errors.New("unexpected network request")
			})})
			_, err := service.KnowledgeBaseIngestFiles(context.Background(), "kb-a", test.files, test.dir, "", false, WaitOptions{Wait: test.wait}, CheckOptions{})
			if result.AsError(err).Kind != "input" || requests != 0 {
				t.Fatalf("err=%v requests=%d", err, requests)
			}
		})
	}
}

func TestKnowledgeBatchKeepsPerFileOutcomesAndUsesOnePreflight(t *testing.T) {
	for _, test := range []struct {
		name    string
		want    []string
		uploads int
	}{
		{"completed", []string{"completed", "completed", "completed"}, 3},
		{"submitted", []string{"submitted", "submitted", "submitted"}, 3},
		{"dry-run", []string{"planned", "planned", "planned"}, 0},
		{"stdin-key", []string{"completed", "completed", "completed"}, 3},
		{"task-failure", []string{"completed", "failed", "completed"}, 3},
		{"store-rejection", []string{"completed", "failed", "completed"}, 3},
		{"unknown", []string{"completed", "unknown", "not_started"}, 2},
		{"wait-timeout", []string{"completed", "pending", "not_started"}, 2},
		{"cancel", []string{"completed", "not_started", "not_started"}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			paths := []string{}
			for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
				path := filepath.Join(dir, name)
				if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, path)
			}
			var uploads, stores, identities, capabilities int
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			transport := taskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/api/v1/system/context":
					identities++
					return taskResponse(`{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`), nil
				case "/api/v1/system/capabilities":
					capabilities++
					return taskResponse(`{"code":0,"data":{"schema_version":1,"operations":{"knowledge_base.file.store":{"supported":true},"knowledge_base.get":{"supported":true},"file.document.upload":{"supported":true},"task.get":{"supported":true}}}}`), nil
				case "/api/v1/knowledge/bases/kb-a":
					return taskResponse(`{"code":0,"data":{"base":{"uuid":"kb-a","name":"KB"}}}`), nil
				case "/api/v1/files/documents":
					uploads++
					file, header, err := request.FormFile("file")
					if err != nil {
						t.Fatal(err)
					}
					content, err := io.ReadAll(file)
					_ = file.Close()
					if err != nil || string(content) != header.Filename || header.Filename != filepath.Base(paths[uploads-1]) {
						t.Fatalf("upload mismatch: %q %q %v", content, header.Filename, err)
					}
					return taskResponse(fmt.Sprintf(`{"code":0,"data":{"file_id":"file-%d"}}`, uploads)), nil
				case "/api/v1/knowledge/bases/kb-a/files":
					stores++
					var body map[string]any
					if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["parser_plugin_id"] != "parser-a" || body["file_id"] != fmt.Sprintf("file-%d", stores) {
						t.Fatalf("store body=%v err=%v", body, err)
					}
					if stores == 2 && test.name == "unknown" {
						return nil, errors.New("response lost after submission")
					}
					if stores == 2 && test.name == "store-rejection" {
						return taskHTTPResponse(400, `{"code":"invalid_file","data":null}`), nil
					}
					return taskResponse(fmt.Sprintf(`{"code":0,"data":{"task_id":%d}}`, stores)), nil
				default:
					if strings.HasPrefix(request.URL.Path, "/api/v1/system/tasks/") {
						id := strings.TrimPrefix(request.URL.Path, "/api/v1/system/tasks/")
						status, taskError := "succeeded", "null"
						if id == "2" && test.name == "task-failure" {
							status, taskError = "failed", `{"type":"parse_error","message":"fixture rejected"}`
						}
						if id == "2" && test.name == "wait-timeout" {
							status = "running"
						}
						if test.name == "cancel" {
							cancel()
						}
						return taskResponse(fmt.Sprintf(`{"code":0,"data":{"id":%s,"task_type":"user","kind":"knowledge-operation","status":%q,"error":%s,"result":null,"created_at":1}}`, id, status, taskError)), nil
					}
					return nil, errors.New("unexpected request")
				}
			})
			service := New(Dependencies{In: strings.NewReader("secret\n"), Transport: transport,
				LookupEnv:         func(name string) (string, bool) { return "secret", name == "KEY" },
				DefaultConfigPath: func() (string, error) { return filepath.Join(dir, "config.yaml"), nil },
			})
			if _, err := service.ContextAdd("production", "http://example.test", "KEY", "workspace-a", ""); err != nil {
				t.Fatal(err)
			}
			waitTimeout := time.Second
			if test.name == "wait-timeout" {
				waitTimeout = 200 * time.Millisecond
			}
			value, err := service.KnowledgeBaseIngestFiles(ctx, "kb-a", paths, "", "parser-a", test.name == "dry-run", WaitOptions{
				Wait: test.name != "submitted", PollInterval: time.Millisecond, WaitTimeout: waitTimeout,
			}, CheckOptions{Context: "production", ContextSet: true, APIKeyStdin: test.name == "stdin-key"})
			data, ok := value.Data.(map[string]any)
			if !ok {
				t.Fatalf("batch did not return data: %v", err)
			}
			rows := data["files"].([]map[string]any)
			for index, want := range test.want {
				if rows[index]["status"] != want {
					t.Fatalf("row %d: want %s got %#v (err=%v)", index, want, rows[index], err)
				}
			}
			wantError := test.want[1] == "failed" || test.want[1] == "unknown" || test.want[1] == "pending" || test.name == "cancel"
			if (err != nil) != wantError || uploads != test.uploads || stores != test.uploads || identities != 1 || capabilities != 1 {
				t.Fatalf("err=%v upload/store=%d/%d identity/capability=%d/%d", err, uploads, stores, identities, capabilities)
			}
			if test.name == "unknown" && (rows[1]["file_id"] != "file-2" || data["summary"].(map[string]int)["not_started"] != 1) {
				t.Fatalf("lost partial state: %#v", data)
			}
		})
	}
}
