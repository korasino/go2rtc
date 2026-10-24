package xiaomi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsTokenExpired(t *testing.T) {
	for _, msg := range []string{
		"401 Unauthorized", "421 Unauthorized", "xiaomi: code=2 message=expired",
		"xiaomi: code=3 message=auth err", "auth err", "invalid signature",
		"serviceToken_expired", "xiaomi: unauthorized",
	} {
		t.Run(msg, func(t *testing.T) {
			require.True(t, isTokenExpired(errors.New(msg)))
		})
	}
	for _, msg := range []string{
		"500 Internal Server Error", "xiaomi: code=1 message=bad request",
		"xiaomi: code=20 message=unrelated", "network timeout",
	} {
		t.Run(msg, func(t *testing.T) {
			require.False(t, isTokenExpired(errors.New(msg)))
		})
	}
}

func TestRequestRefreshesAfterUnauthorized(t *testing.T) {
	for _, status := range []string{"401 Unauthorized", "401 Token Expired"} {
		t.Run(status, func(t *testing.T) {
			testRequestRefresh(t, func(t *testing.T, req *http.Request) *http.Response {
				return testResponse(req, http.StatusUnauthorized, status, nil, nil)
			})
		})
	}
}

func TestRequestRefreshesAfterAuthError(t *testing.T) {
	testRequestRefresh(t, func(t *testing.T, req *http.Request) *http.Response {
		body := testEncryptedBody(t, req, []byte("old-ssecurity"), map[string]any{
			"code":    3,
			"message": "auth err",
		})
		return testResponse(req, http.StatusOK, "OK", []byte(body), nil)
	})
}

func testRequestRefresh(t *testing.T, firstResponse func(t *testing.T, req *http.Request) *http.Response) {
	const (
		userID     = "user1"
		oldToken   = "old-pass"
		newToken   = "new-pass"
		oldCookies = "userId=user1; cUserId=cuser1; serviceToken=old-service"
		newCookies = "userId=user1; cUserId=cuser1; serviceToken=new-service"
	)

	oldSsecurity := []byte("old-ssecurity")
	newSsecurity := []byte("new-ssecurity")

	var apiCalls, loginCalls, refreshCalls int
	var firstBody *trackingReadCloser

	cloud := NewCloud("xiaomiio")
	cloud.OnRefresh = func() { refreshCalls++ }
	cloud.userID = userID
	cloud.passToken = oldToken
	cloud.cookies = oldCookies
	cloud.ssecurity = oldSsecurity
	cloud.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Host == "api.io.mi.com" && req.URL.Path == "/app/test":
			apiCalls++
			if apiCalls == 1 {
				res := firstResponse(t, req)
				firstBody = &trackingReadCloser{ReadCloser: res.Body}
				res.Body = firstBody
				return res, nil
			}

			require.Equal(t, newCookies, req.Header.Get("Cookie"))

			body := testEncryptedBody(t, req, newSsecurity, map[string]any{
				"code":   0,
				"result": map[string]bool{"ok": true},
			})
			return testResponse(req, http.StatusOK, "OK", []byte(body), nil), nil

		case req.URL.Host == "account.xiaomi.com" && req.URL.Path == "/pass/serviceLogin":
			loginCalls++
			require.True(t, firstBody.closed, "the failed response must be closed before re-login")
			require.Equal(t, "userId="+userID+"; passToken="+oldToken, req.Header.Get("Cookie"))

			body := testLoginBody(t, map[string]any{
				"ssecurity": newSsecurity,
				"passToken": newToken,
				"location":  "https://account.xiaomi.com/pass/finish",
			})
			return testResponse(req, http.StatusOK, "OK", []byte(body), nil), nil

		case req.URL.Host == "account.xiaomi.com" && req.URL.Path == "/pass/finish":
			header := http.Header{}
			body, err := json.Marshal(struct {
				Ssecurity []byte `json:"ssecurity"`
			}{Ssecurity: newSsecurity})
			require.NoError(t, err)
			header.Set("Extension-Pragma", string(body))

			res := testResponse(req, http.StatusOK, "OK", nil, header)
			res.Header.Add("Set-Cookie", "userId="+userID)
			res.Header.Add("Set-Cookie", "cUserId=cuser1")
			res.Header.Add("Set-Cookie", "serviceToken=new-service")
			res.Header.Add("Set-Cookie", "passToken="+newToken)
			return res, nil
		}

		t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
		return nil, nil
	})

	res, err := cloud.Request("https://api.io.mi.com", "/app/test", `{"test":true}`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(res))

	_, token := cloud.UserToken()
	require.Equal(t, newToken, token)
	require.Equal(t, newCookies, cloud.cookies)
	require.Equal(t, 2, apiCalls)
	require.Equal(t, 1, loginCalls)
	require.Equal(t, 1, refreshCalls)
}

func TestRefreshAuthFailureDoesNotNotify(t *testing.T) {
	cloud := NewCloud("xiaomiio")
	var refreshCalls int
	cloud.OnRefresh = func() { refreshCalls++ }
	cloud.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("login unavailable")
	})

	require.Error(t, cloud.refreshAuth("", ""))
	require.Zero(t, refreshCalls)
}

func TestLoginWithTokenMissingLocationDoesNotLeakCredentials(t *testing.T) {
	cloud := NewCloud("xiaomiio")
	body := testLoginBody(t, map[string]any{
		"passToken": "sensitive-pass",
		"ssecurity": []byte("sensitive-security"),
	})
	cloud.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return testResponse(req, http.StatusOK, "OK", []byte(body), nil), nil
	})

	err := cloud.LoginWithToken("user1", "old-pass")
	require.EqualError(t, err, "xiaomi: login response missing location")
}

func TestRefreshAuthReplacedSessionDoesNotNotify(t *testing.T) {
	cloud := NewCloud("xiaomiio")
	cloud.cookies = "new-session"
	var refreshCalls int
	cloud.OnRefresh = func() { refreshCalls++ }
	cloud.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("a replaced session must not trigger another login")
		return nil, errors.New("unexpected login")
	})

	require.NoError(t, cloud.refreshAuth("", "old-session"))
	require.Zero(t, refreshCalls)
}

func TestConcurrentRequestsRefreshOnce(t *testing.T) {
	cloud := NewCloud("xiaomiio")
	cloud.userID, cloud.passToken, cloud.cookies = "user1", "old-pass", "old-session"
	var logins, notifications, oldRequests atomic.Int32
	const consumers = 16
	captured := make(chan struct{})
	cloud.OnRefresh = func() { notifications.Add(1) }
	loginBody := testLoginBody(t, map[string]any{
		"ssecurity": []byte("new-security"),
		"passToken": "new-pass",
		"location":  "https://account.xiaomi.com/pass/finish",
	})
	cloud.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/app/test":
			if req.Header.Get("Cookie") == "old-session" {
				if oldRequests.Add(1) == consumers {
					close(captured)
				}
				// All consumers must capture the old session before any sees 401.
				select {
				case <-captured:
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
				return testResponse(req, http.StatusUnauthorized, "401 Unauthorized", nil, nil), nil
			}
			if req.Header.Get("Cookie") != "userId=user1; cUserId=; serviceToken=new-service" {
				return nil, errors.New("retry did not use the refreshed session")
			}
			return testResponse(req, http.StatusInternalServerError, "500 Internal Server Error", nil, nil), nil
		case "/pass/serviceLogin":
			logins.Add(1)
			return testResponse(req, http.StatusOK, "OK", []byte(loginBody), nil), nil
		case "/pass/finish":
			header := http.Header{}
			header.Add("Set-Cookie", "serviceToken=new-service")
			return testResponse(req, http.StatusOK, "OK", nil, header), nil
		default:
			return nil, errors.New("unexpected request")
		}
	})

	errs := make(chan error, consumers)
	var workers sync.WaitGroup
	for range cap(errs) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := cloud.Request("https://api.io.mi.com", "/app/test", "{}", nil)
			errs <- err
		}()
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		require.EqualError(t, err, "500 Internal Server Error")
	}
	require.EqualValues(t, consumers, oldRequests.Load())
	require.EqualValues(t, 1, logins.Load())
	require.EqualValues(t, 1, notifications.Load())
	_, token := cloud.UserToken()
	require.Equal(t, "new-pass", token)
}

func TestRequestDoesNotRefreshUnrelatedError(t *testing.T) {
	cloud := NewCloud("xiaomiio")
	var requests, notifications int
	cloud.OnRefresh = func() { notifications++ }
	cloud.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		return testResponse(req, http.StatusInternalServerError, "500 Internal Server Error", nil, nil), nil
	})

	_, err := cloud.Request("https://api.io.mi.com", "/app/test", "{}", nil)
	require.EqualError(t, err, "500 Internal Server Error")
	require.Equal(t, 1, requests)
	require.Zero(t, notifications)
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

type trackingReadCloser struct {
	io.ReadCloser
	closed bool
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return r.ReadCloser.Close()
}

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testLoginBody(t *testing.T, payload any) string {
	t.Helper()

	body, err := json.Marshal(payload)
	require.NoError(t, err)

	return "&&&START&&&" + string(body)
}

func testEncryptedBody(t *testing.T, req *http.Request, ssecurity []byte, payload any) string {
	t.Helper()

	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)

	values, err := url.ParseQuery(string(body))
	require.NoError(t, err)

	nonce, err := base64.StdEncoding.DecodeString(values.Get("_nonce"))
	require.NoError(t, err)

	signedNonce := genSignedNonce(ssecurity, nonce)

	plaintext, err := json.Marshal(payload)
	require.NoError(t, err)

	ciphertext, err := crypt(signedNonce, plaintext)
	require.NoError(t, err)

	return base64.StdEncoding.EncodeToString(ciphertext)
}

func testResponse(req *http.Request, code int, status string, body []byte, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	if body == nil {
		body = []byte{}
	}

	return &http.Response{
		StatusCode: code,
		Status:     status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Request:    req,
	}
}
