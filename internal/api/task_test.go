package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTasksParsesFilteredPublicTaskList(t *testing.T) {
	var requestPath string
	client := Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestPath = request.URL.RequestURI()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"tasks":[{"id":7,"task_type":"user","kind":"extension-operation","status":"succeeded","error":null,"result":null,"created_at":1.0}]}}`)),
		}, nil
	})}
	tasks, err := client.Tasks(context.Background(), Target{Endpoint: "http://example.test"}, TaskListFilters{
		Type: "user",
		Kind: "extension-operation",
	})
	if err != nil {
		t.Fatalf("Tasks() error = %v", err)
	}
	if requestPath != "/api/v1/system/tasks?kind=extension-operation&type=user" {
		t.Fatalf("request path = %q", requestPath)
	}
	if len(tasks) != 1 || tasks[0].ID.String() != "7" || tasks[0].Kind != "extension-operation" {
		t.Fatalf("tasks = %#v", tasks)
	}
}
