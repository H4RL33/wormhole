package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/mcp"
	"github.com/H4RL33/wormhole/internal/types"
)

func TestLinkedVersionAppearsInServerMetadata(t *testing.T) {
	want := os.Getenv("WORMHOLE_EXPECT_LINKED_VERSION")
	if want == "" {
		want = "dev"
	}
	if version != want {
		t.Fatalf("linked version = %q, want %q", version, want)
	}

	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
	))
	response := httptest.NewRecorder()
	fabricMCPHandler(mcp.NewRegistry(), nil).ServeHTTP(response, request)

	var envelope struct {
		Result struct {
			ServerInfo map[string]string `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	if got := envelope.Result.ServerInfo["version"]; got != want {
		t.Fatalf("Fabric metadata version = %q, want %q", got, want)
	}
}

func TestServerMainHelperProcess(t *testing.T) {
	if os.Getenv("WORMHOLE_SERVER_MAIN_HELPER") != "1" {
		return
	}
	log.SetFlags(0)
	runServerMain = func(types.Config, func(*http.Server) error) error {
		return errors.New("injected database failure")
	}
	main()
	t.Fatal("main returned without exiting")
}

func TestServerMainExitsOneWhenWiringFails(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestServerMainHelperProcess$")
	command.Env = append(os.Environ(), "WORMHOLE_SERVER_MAIN_HELPER=1")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("server main exited successfully, want status 1")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("server main error = %v, want exit status 1", err)
	}
	if got, want := string(output), "fabric: injected database failure\n"; got != want {
		t.Fatalf("server main output = %q, want %q", got, want)
	}
}

func TestRunServerWithOpenReturnsDatabaseFailureBeforeServing(t *testing.T) {
	wantErr := errors.New("database unavailable")
	served := false
	err := runServerWithOpen(types.Config{}, func(types.Config) (*sql.DB, error) {
		return nil, wantErr
	}, func(*http.Server) error {
		served = true
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runServerWithOpen error = %v, want %v", err, wantErr)
	}
	if got, want := err.Error(), "open database: database unavailable"; got != want {
		t.Fatalf("runServerWithOpen error text = %q, want %q", got, want)
	}
	if served {
		t.Fatal("runServerWithOpen called serve after database open failed")
	}
}

func TestRunServerBuildsBoundedMCPAndDashboardMux(t *testing.T) {
	cfg := types.LoadConfig()
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.AdminKey = "admin-key"
	wantErr := errors.New("stop after inspecting server")

	err := runServer(cfg, func(server *http.Server) error {
		if server.Addr != cfg.ListenAddr {
			t.Fatalf("server address = %q, want %q", server.Addr, cfg.ListenAddr)
		}
		if server.ReadTimeout != 10*time.Second || server.WriteTimeout != 10*time.Second || server.IdleTimeout != 60*time.Second || server.ReadHeaderTimeout != 5*time.Second {
			t.Fatalf("server timeouts = read=%v write=%v idle=%v headers=%v", server.ReadTimeout, server.WriteTimeout, server.IdleTimeout, server.ReadHeaderTimeout)
		}

		for _, request := range []*http.Request{
			httptest.NewRequest(http.MethodGet, "/healthz", nil),
			httptest.NewRequest(http.MethodGet, "/dashboard/", nil),
		} {
			response := httptest.NewRecorder()
			server.Handler.ServeHTTP(response, request)
			wantStatus := http.StatusOK
			if request.URL.Path == "/healthz" {
				wantStatus = http.StatusNoContent
			}
			if response.Code != wantStatus {
				t.Fatalf("%s status = %d, want %d", request.URL.Path, response.Code, wantStatus)
			}
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runServer error = %v, want %v", err, wantErr)
	}
}
