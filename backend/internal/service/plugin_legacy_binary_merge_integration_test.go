package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This executable links the preserved 4897 SDK in its own module. In particular,
// it is not a subprocess of the current test binary or a current generated server
// that merely chooses to return Unimplemented for the new RPC.
func buildLegacy4897PluginBinary(t *testing.T) string {
	t.Helper()
	source := filepath.Join("testdata", "plugin_legacy_4897")
	destination := t.TempDir()
	require.NoError(t, filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	}))
	for _, file := range []string{"plugin.proto", "plugin.pb.go", "plugin_grpc.pb.go", "runtime.go"} {
		oldSDK, err := os.ReadFile(filepath.Join(destination, "pkg", "pluginapi", "v1", file))
		require.NoError(t, err)
		require.NotContains(t, string(oldSDK), "InitHostServices", "fixture must retain the actual old generated API")
		require.NotContains(t, string(oldSDK), "status_json", "fixture must retain the actual old Health wire schema")
	}
	binaryPath := filepath.Join(destination, "legacy-plugin")
	if runtime.GOOS == "windows" {
		binaryPath += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binaryPath, ".")
	cmd.Dir = destination
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		switch strings.ToUpper(key) {
		case "GOWORK", "GOFLAGS", "GOOS", "GOARCH", "CGO_ENABLED":
			continue
		}
		cmd.Env = append(cmd.Env, value)
	}
	cmd.Env = append(cmd.Env, "GOWORK=off", "GOFLAGS=", "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "building the independent baseline SDK plugin must not be skipped: %s", output)
	return binaryPath
}

func TestPluginLegacyBinaryMergeIntegration(t *testing.T) {
	binaryPath := buildLegacy4897PluginBinary(t)
	data, err := os.ReadFile(binaryPath)
	require.NoError(t, err)
	// startPluginRuntime enforces a mandatory binary integrity checksum.
	sum := sha256.Sum256(data)
	installation := &PluginInstallation{
		PluginKey: "test.legacy-4897", Version: "0.0.1", BinaryPath: binaryPath, BinarySHA256: hex.EncodeToString(sum[:]),
	}

	type observedRequest struct {
		method, path, body string
		headers            http.Header
		length             int64
		err                error
	}
	requests := make(chan observedRequest, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		requests <- observedRequest{request.Method, request.URL.Path, string(body), request.Header.Clone(), request.ContentLength, err}
		writer.Header().Set("X-Local-Upstream", "legacy-compatible")
		writer.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(writer, "response-from-loopback")
	}))
	defer upstream.Close()

	// Repeated starts exercise broker/process teardown as well as the ability to
	// restart the same genuine old executable after an orderly drain.
	for _, cycle := range []string{"initial_start", "restart_after_drain"} {
		t.Run(cycle, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			store := newFakePluginKVStore()
			hostServer := newPluginHostServiceServer(installation.PluginKey, store, nil, PluginAccountScope{})
			pluginRuntime, err := startPluginRuntime(ctx, installation, 5*time.Second, t.TempDir(), hostServer)
			require.NoError(t, err, "old SDK plugin must start even when the new host offers HostService")
			t.Cleanup(pluginRuntime.kill)
			require.NoError(t, pluginRuntime.checkHealth(ctx))
			health, err := pluginRuntime.status(ctx)
			require.NoError(t, err)
			require.True(t, health.Healthy)
			require.Equal(t, "legacy-4897-health", health.Message)
			require.Empty(t, health.StatusJson, "the new optional field must decode as empty from the old Health message")
			_, err = pluginRuntime.api.InitHostServices(ctx, &pluginv1.InitHostServicesRequest{HostServiceId: 999, HostServiceApiVersion: pluginv1.HostServiceAPIVersion})
			require.Equal(t, codes.Unimplemented, status.Code(err), "the independently compiled old server has no new RPC")
			require.NoError(t, pluginRuntime.validateAndApplyConfig(ctx, []byte(`{"synthetic_test":true}`)))

			// Use the broker belonging to the actual production runtime, so Kill
			// must stop a listening reverse-service goroutine as well as the child.
			rpcClient, err := pluginRuntime.client.Client()
			require.NoError(t, err)
			dispensed, err := rpcClient.Dispense(pluginv1.TransportPluginName)
			require.NoError(t, err)
			transportClient, ok := dispensed.(*pluginv1.TransportClient)
			require.True(t, ok)
			require.NotNil(t, transportClient.Broker)
			brokerServing, brokerStopped := make(chan struct{}), make(chan struct{})
			brokerID := transportClient.Broker.NextId()
			go func() {
				defer close(brokerStopped)
				transportClient.Broker.AcceptAndServe(brokerID, func(options []grpc.ServerOption) *grpc.Server {
					server := grpc.NewServer(options...)
					pluginv1.RegisterHostServiceServer(server, hostServer)
					close(brokerServing)
					return server
				})
			}()
			select {
			case <-brokerServing:
			case <-ctx.Done():
				t.Fatal("runtime broker never began serving")
			}

			request, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL+"/synthetic-forward", bytes.NewBufferString("synthetic-request"))
			require.NoError(t, err)
			request.Header.Set("Authorization", "Bearer synthetic-fixture-token")
			request.Header.Set("Content-Type", "application/json")
			account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 3}
			require.True(t, pluginRuntime.beginRequest())
			response, err := pluginRuntime.roundTrip(ctx, request, "", account)
			if err != nil {
				pluginRuntime.finishRequest()
			}
			require.NoError(t, err)
			t.Cleanup(func() { _ = response.Body.Close() })
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusCreated, response.StatusCode)
			require.Equal(t, "legacy-compatible", response.Header.Get("X-Local-Upstream"))
			require.Equal(t, "response-from-loopback", string(body))
			require.Equal(t, int64(len(body)), response.ContentLength)
			select {
			case request := <-requests:
				require.NoError(t, request.err)
				require.Equal(t, http.MethodPost, request.method)
				require.Equal(t, "/synthetic-forward", request.path)
				require.Equal(t, "synthetic-request", request.body)
				require.Equal(t, int64(len(request.body)), request.length)
				require.Equal(t, "Bearer synthetic-fixture-token", request.headers.Get("Authorization"))
				require.Equal(t, "4897", request.headers.Get("X-Legacy-SDK"))
				require.Equal(t, "42", request.headers.Get("X-Legacy-Account"))
				require.Equal(t, PlatformOpenAI, request.headers.Get("X-Legacy-Platform"))
				require.Equal(t, AccountTypeOAuth, request.headers.Get("X-Legacy-Account-Type"))
			case <-ctx.Done():
				t.Fatal("old plugin did not reach the local fake upstream")
			}
			require.Equal(t, int64(1), pluginRuntime.inFlight.Load(), "response ownership must survive until Body.Close")
			drained := make(chan struct{})
			go func() { pluginRuntime.drain(5 * time.Second); close(drained) }()
			require.Eventually(t, pluginRuntime.draining.Load, time.Second, time.Millisecond)
			require.False(t, pluginRuntime.beginRequest(), "draining must reject new work")
			select {
			case <-drained:
				t.Fatal("runtime killed an owned response before Body.Close")
			default:
			}
			require.NoError(t, response.Body.Close())
			require.NoError(t, response.Body.Close(), "closing twice must not decrement the in-flight count twice")
			select {
			case <-drained:
			case <-ctx.Done():
				t.Fatal("runtime did not finish draining after response close")
			}
			require.Zero(t, pluginRuntime.inFlight.Load())
			require.True(t, pluginRuntime.client.Exited())
			select {
			case <-brokerStopped:
			case <-ctx.Done():
				t.Fatal("runtime broker listener survived process teardown")
			}
			require.Error(t, pluginRuntime.checkHealth(ctx), "a terminated process must no longer be healthy")
		})
	}
}
