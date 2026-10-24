package xiaomi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/pkg/xiaomi"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestGetCloudConcurrentLogin(t *testing.T) {
	entered := make(chan struct{}, 16)
	release := make(chan struct{})
	var unblock sync.Once
	_, calls := setupCloudTest(t, func(n int32) string {
		entered <- struct{}{}
		<-release
		return fmt.Sprintf("rotated-pass-%d", n)
	})
	var workers sync.WaitGroup
	t.Cleanup(func() {
		unblock.Do(func() { close(release) })
		workers.Wait()
	})

	type result struct {
		cloud *xiaomi.Cloud
		err   error
	}
	results := make(chan result, 16)
	for range cap(results) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			cloud, err := getCloud("user1")
			results <- result{cloud, err}
		}()
	}

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("initial login did not start")
	}
	// Hold the first login open while the other callers contend for the cache.
	select {
	case <-entered:
		t.Error("a second login started before the first completed")
	case <-time.After(100 * time.Millisecond):
	}
	unblock.Do(func() { close(release) })

	var first *xiaomi.Cloud
	for range cap(results) {
		select {
		case res := <-results:
			require.NoError(t, res.err)
			if first == nil {
				first = res.cloud
			}
			require.Same(t, first, res.cloud)
		case <-time.After(5 * time.Second):
			t.Fatal("getCloud did not finish")
		}
	}
	require.EqualValues(t, 1, calls.Load())
	require.Equal(t, "rotated-pass-1", tokens["user1"])
	require.Equal(t, tokens["user1"], getUserToken(clouds["user1"]))
	config, err := os.ReadFile(app.ConfigPath)
	require.NoError(t, err)
	require.Contains(t, string(config), "rotated-pass-1")
	require.NoError(t, persistRotatedToken("user1", clouds["user1"]))
}

func TestGetCloudConfigWriteFailure(t *testing.T) {
	logs, calls := setupCloudTest(t, func(int32) string { return "rotated-pass" })
	configPath := app.ConfigPath
	// Writing to a directory fails even when the tests run as root.
	app.ConfigPath = t.TempDir()

	cloud, err := getCloud("user1")
	require.NoError(t, err)
	require.Equal(t, "rotated-pass", getUserToken(cloud))
	cached, err := getCloud("user1")
	require.NoError(t, err)
	require.Same(t, cloud, cached)
	require.EqualValues(t, 1, calls.Load())
	require.Contains(t, logs.String(), "xiaomi: save passToken")
	require.Contains(t, logs.String(), `"level":"warn"`)
	require.NotContains(t, logs.String(), "passToken rotated and persisted")
	require.NotContains(t, logs.String(), "rotated-pass")

	// A later request can retry persistence without logging in again.
	app.ConfigPath = configPath
	require.NoError(t, persistRotatedToken("user1", cloud))
	require.Equal(t, "rotated-pass", tokens["user1"])
	config, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(config), "rotated-pass")
	require.EqualValues(t, 1, calls.Load())
}

func TestCloudSessionRefreshLogging(t *testing.T) {
	logs, calls := setupCloudTest(t, func(int32) string { return "old-pass" })
	cloud, err := getCloud("user1")
	require.NoError(t, err)
	require.NotContains(t, logs.String(), "cloud session refreshed")

	// The fake API always rejects requests. A successful re-login must still
	// be logged exactly once, with no second refresh after the retry fails.
	_, err = cloud.Request(GetBaseURL("cn"), "/test", "{}", nil)
	require.Error(t, err)
	require.EqualValues(t, 2, calls.Load())
	require.Equal(t, 1, strings.Count(logs.String(), "xiaomi: cloud session refreshed"))
	require.Contains(t, logs.String(), `"level":"info"`)
	require.NotContains(t, logs.String(), "passToken rotated and persisted")
	require.NotContains(t, logs.String(), "old-pass")
	require.NotContains(t, logs.String(), "service-2")
}

func TestCloudRequestPersistsRotationAfterRetryFailure(t *testing.T) {
	_, calls := setupCloudTest(t, func(n int32) string {
		if n == 1 {
			return "old-pass"
		}
		return "rotated-pass"
	})

	_, err := cloudRequest("user1", "cn", "/test", "{}")
	require.EqualError(t, err, "401 Unauthorized")
	require.EqualValues(t, 2, calls.Load())
	require.Equal(t, "rotated-pass", getUserToken(clouds["user1"]))
	require.Equal(t, "rotated-pass", tokens["user1"])
	config, err := os.ReadFile(app.ConfigPath)
	require.NoError(t, err)
	require.Contains(t, string(config), "rotated-pass")
}

func TestAPIAuthInvalidatesOldCloud(t *testing.T) {
	setupCloudTest(t, func(n int32) string {
		if n == 1 {
			return "old-pass"
		}
		return "interactive-pass"
	})
	oldCloud, err := getCloud("user1")
	require.NoError(t, err)

	transport := http.DefaultTransport
	http.DefaultTransport = cloudTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/pass/serviceLoginAuth2" {
			return transport.RoundTrip(req)
		}
		body := `&&&START&&&{"ssecurity":"dGVzdC1zZWN1cml0eQ==","passToken":"interactive-pass","location":"https://account.xiaomi.com/pass/finish?n=interactive"}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})
	form := url.Values{"username": {"test-user"}, "password": {"test-password"}}
	req := httptest.NewRequest(http.MethodPost, "/api/xiaomi", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res := httptest.NewRecorder()
	apiAuth(res, req)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	require.Equal(t, "interactive-pass", tokens["user1"])
	require.NotContains(t, clouds, "user1")

	// A request that began before the WebUI login must not save its old token.
	require.NoError(t, persistRotatedToken("user1", oldCloud))
	require.Equal(t, "interactive-pass", tokens["user1"])
	newCloud, err := getCloud("user1")
	require.NoError(t, err)
	require.NotSame(t, oldCloud, newCloud)
	require.NoError(t, persistRotatedToken("user1", oldCloud))
	require.Equal(t, "interactive-pass", tokens["user1"])
	config, err := os.ReadFile(app.ConfigPath)
	require.NoError(t, err)
	require.Contains(t, string(config), "interactive-pass")
}

func setupCloudTest(t *testing.T, loginToken func(int32) string) (*bytes.Buffer, *atomic.Int32) {
	t.Helper()
	oldTokens, oldClouds, oldLog, oldAuth := tokens, clouds, log, auth
	oldConfig, oldTransport := app.ConfigPath, http.DefaultTransport
	t.Cleanup(func() {
		tokens, clouds, log, auth = oldTokens, oldClouds, oldLog, oldAuth
		app.ConfigPath, http.DefaultTransport = oldConfig, oldTransport
	})

	tokens = map[string]string{"user1": "old-pass"}
	clouds = nil
	var logs bytes.Buffer
	log = zerolog.New(&logs)
	app.ConfigPath = filepath.Join(t.TempDir(), "go2rtc.yaml")
	require.NoError(t, os.WriteFile(app.ConfigPath, []byte("xiaomi:\n  user1: old-pass\n"), 0600))

	var calls atomic.Int32
	http.DefaultTransport = cloudTestTransport(func(req *http.Request) (*http.Response, error) {
		header := make(http.Header)
		var body string
		switch {
		case req.URL.Host == "account.xiaomi.com" && req.URL.Path == "/pass/serviceLogin":
			n := calls.Add(1)
			token := loginToken(n)
			payload, err := json.Marshal(map[string]any{
				"ssecurity": []byte("test-security"),
				"passToken": token,
				"location":  fmt.Sprintf("https://account.xiaomi.com/pass/finish?n=%d", n),
			})
			if err != nil {
				return nil, err
			}
			body = "&&&START&&&" + string(payload)
		case req.URL.Host == "account.xiaomi.com" && req.URL.Path == "/pass/finish":
			header.Add("Set-Cookie", "userId=user1")
			header.Add("Set-Cookie", "cUserId=cuser1")
			header.Add("Set-Cookie", "serviceToken=service-"+req.URL.Query().Get("n"))
		case req.URL.Host == "api.io.mi.com" && req.URL.Path == "/app/test":
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Status:     "401 Unauthorized",
				Header:     header,
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		default:
			return nil, fmt.Errorf("unexpected request: %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})
	return &logs, &calls
}

type cloudTestTransport func(*http.Request) (*http.Response, error)

func (f cloudTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
