package xiaomi

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsTokenExpired(t *testing.T) {
	require.True(t, isTokenExpired(errString("401 Unauthorized")))
	require.True(t, isTokenExpired(errString("xiaomi: code=3 message=auth err")))
	require.False(t, isTokenExpired(errString("xiaomi: code=1 message=bad request")))
}

func TestRequestRefreshesAfterUnauthorized(t *testing.T) {
	testRequestRefresh(t, func(t *testing.T, req *http.Request) *http.Response {
		return testResponse(req, http.StatusUnauthorized, "401 Unauthorized", nil, nil)
	})
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

	var apiCalls, loginCalls int

	cloud := NewCloud("xiaomiio")
	cloud.userID = userID
	cloud.passToken = oldToken
	cloud.cookies = oldCookies
	cloud.ssecurity = oldSsecurity
	cloud.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Host == "api.io.mi.com" && req.URL.Path == "/app/test":
			apiCalls++
			if apiCalls == 1 {
				return firstResponse(t, req), nil
			}

			require.Equal(t, newCookies, req.Header.Get("Cookie"))

			body := testEncryptedBody(t, req, newSsecurity, map[string]any{
				"code":   0,
				"result": map[string]bool{"ok": true},
			})
			return testResponse(req, http.StatusOK, "OK", []byte(body), nil), nil

		case req.URL.Host == "account.xiaomi.com" && req.URL.Path == "/pass/serviceLogin":
			loginCalls++
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
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type errString string

func (e errString) Error() string {
	return string(e)
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
