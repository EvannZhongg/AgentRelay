package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"AgentRelay/internal/protocol"
)

func TestLimitedOutputThroughCopy(t *testing.T) {
	var out limitedOutput
	data := strings.Repeat("x", outputLimit+100)
	n, err := io.Copy(&out, io.LimitReader(strings.NewReader(data), int64(len(data))))
	if err != nil || n != int64(len(data)) || len(out.String()) != outputLimit || !out.truncated {
		t.Fatalf("copy: n=%d len=%d truncated=%v err=%v", n, len(out.String()), out.truncated, err)
	}
}

func TestTaskRetention(t *testing.T) {
	s := newTaskStore()
	now := time.Now()
	expired, recent := now.Add(-2*storeRetention), now.Add(-time.Minute)
	s.values["expired"] = &protocol.Task{ID: "expired", FinishedAt: &expired}
	s.values["recent"] = &protocol.Task{ID: "recent", FinishedAt: &recent}
	s.values["active"] = &protocol.Task{ID: "active", StartedAt: expired}
	if _, ok := s.get("expired"); ok {
		t.Fatal("expired task retained")
	}
	if _, ok := s.get("recent"); !ok {
		t.Fatal("recent task removed")
	}
	s.pruneLocked(now, 1)
	if len(s.values) != 1 || s.values["active"] == nil {
		t.Fatal("active task evicted")
	}
}

func TestTaskCapacityAndOldestEviction(t *testing.T) {
	s := newTaskStore()
	for i := 0; i < storeRecordLimit; i++ {
		id := fmt.Sprint(i)
		s.values[id] = &protocol.Task{ID: id, Status: "running"}
	}
	run := func(context.Context, protocol.ExecRequest) (*protocol.ExecResponse, error) {
		return &protocol.ExecResponse{}, nil
	}
	if _, err := s.start(protocol.ExecRequest{}, run); !errors.Is(err, errStoreCapacity) {
		t.Fatalf("capacity: %v", err)
	}
	old, recent := time.Now().Add(-time.Minute), time.Now()
	s.values["0"].FinishedAt = &old
	s.values["1"].FinishedAt = &recent
	task, err := s.start(protocol.ExecRequest{}, run)
	if err != nil {
		t.Fatal(err)
	}
	waitTask(t, s, task.ID)
	if _, ok := s.get("0"); ok {
		t.Fatal("oldest finished task retained at capacity")
	}
	if _, ok := s.get("1"); !ok {
		t.Fatal("newer finished task removed")
	}
	if len(s.list()) != storeRecordLimit {
		t.Fatal("wrong store size")
	}
}

func TestSessionRetentionAndCapacity(t *testing.T) {
	s := newSessionStore()
	expired, recent := time.Now().Add(-2*storeRetention), time.Now()
	for _, item := range []struct {
		id    string
		at    *time.Time
		ended bool
	}{
		{"expired", &expired, true}, {"recent", &recent, true}, {"unreaped", &expired, false}, {"active", nil, false},
	} {
		state := &sessionState{task: protocol.Session{ID: item.id, FinishedAt: item.at}, done: make(chan struct{})}
		if item.ended {
			close(state.done)
		}
		s.values[item.id] = state
	}
	if _, ok := s.get("expired"); ok {
		t.Fatal("expired session retained")
	}
	s.pruneLocked(time.Now(), 2)
	if len(s.values) != 2 || s.values["unreaped"] == nil || s.values["active"] == nil {
		t.Fatal("active or unreaped session evicted")
	}
	for len(s.values) < storeRecordLimit {
		id := fmt.Sprint(len(s.values))
		s.values[id] = &sessionState{done: make(chan struct{})}
	}
	if _, err := s.start(t.TempDir(), protocol.SessionRequest{}); !errors.Is(err, errStoreCapacity) {
		t.Fatalf("capacity: %v", err)
	}
}

func waitTask(t *testing.T, s *taskStore, id string) *protocol.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, ok := s.get(id)
		if !ok {
			t.Fatal("task missing")
		}
		if task.FinishedAt != nil {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("task did not finish")
	return nil
}

func TestFailedTaskErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *protocol.ExecResponse
		err    error
		want   string
	}{
		{"exit", &protocol.ExecResponse{ExitCode: 7}, nil, "command exited with code 7"},
		{"timeout", &protocol.ExecResponse{ExitCode: -1, TimedOut: true}, nil, "execution timed out"},
		{"startup", nil, errors.New("cannot launch shell"), "cannot launch shell"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTaskStore()
			task, err := s.start(protocol.ExecRequest{}, func(context.Context, protocol.ExecRequest) (*protocol.ExecResponse, error) { return tc.result, tc.err })
			if err != nil {
				t.Fatal(err)
			}
			task = waitTask(t, s, task.ID)
			if task.Status != "failed" || task.Error != tc.want {
				t.Fatalf("task = %+v", task)
			}
		})
	}
}

func TestRetrieveTaskHTTP(t *testing.T) {
	s, err := New(Config{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s.tasks.values["test"] = &protocol.Task{ID: "test", Status: "running"}
	request := func(want int) {
		t.Helper()
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/tasks/test/retrieve", nil))
		if response.Code != want {
			t.Fatalf("retrieve: %d %s", response.Code, response.Body.String())
		}
	}
	request(http.StatusConflict)
	now := time.Now()
	s.tasks.values["test"].FinishedAt = &now
	s.tasks.values["test"].Status = "completed"
	if _, ok := s.tasks.get("test"); !ok {
		t.Fatal("get removed task")
	}
	request(http.StatusOK)
	request(http.StatusNotFound)
	if len(s.tasks.list()) != 0 {
		t.Fatal("retrieve retained task")
	}
}

func TestRetrieveIsAtomic(t *testing.T) {
	s := newTaskStore()
	now := time.Now()
	s.values["test"] = &protocol.Task{ID: "test", Status: "completed", FinishedAt: &now}
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.retrieve("test"); results <- err }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, errTaskNotFound) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful retrieves = %d", successes)
	}
}
