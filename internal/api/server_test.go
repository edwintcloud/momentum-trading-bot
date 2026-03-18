package api

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

func TestServerRequiresAuthForOperatorRoutes(t *testing.T) {
	t.Setenv("CONTROL_PLANE_AUTH_TOKEN", "secret")

	server := newTestServer()
	handler := server.handler()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized response, got %d", rr.Code)
	}
	if got := rr.Header().Get("WWW-Authenticate"); got == "" {
		t.Fatal("expected basic auth challenge header")
	}

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("expected health endpoint to remain public, got %d", health.Code)
	}

	authed := httptest.NewRecorder()
	authedReq := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	authedReq.Header.Set("Authorization", basicAuthHeader("operator", "secret"))
	handler.ServeHTTP(authed, authedReq)
	if authed.Code != http.StatusOK {
		t.Fatalf("expected authorized dashboard request to succeed, got %d", authed.Code)
	}
	sessionCookie := findCookie(t, authed.Result().Cookies(), controlPlaneSessionCookieName)
	if !sessionCookie.HttpOnly {
		t.Fatal("expected control-plane session cookie to be HttpOnly")
	}
	if sessionCookie.MaxAge < int((24 * time.Hour).Seconds()) {
		t.Fatalf("expected a long-lived session cookie, got max-age=%d", sessionCookie.MaxAge)
	}

	cookieOnly := httptest.NewRecorder()
	cookieOnlyReq := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	cookieOnlyReq.AddCookie(sessionCookie)
	handler.ServeHTTP(cookieOnly, cookieOnlyReq)
	if cookieOnly.Code != http.StatusOK {
		t.Fatalf("expected session cookie to authorize dashboard request, got %d", cookieOnly.Code)
	}
}

func TestServerRequiresSameOriginForWebSocket(t *testing.T) {
	t.Setenv("CONTROL_PLANE_AUTH_TOKEN", "secret")

	server := newTestServer()
	httptestServer := newSandboxAwareServer(t, server.handler())
	defer httptestServer.Close()

	wsURL := "ws" + strings.TrimPrefix(httptestServer.URL, "http") + "/ws"

	t.Run("accepts same-origin with auth", func(t *testing.T) {
		header := http.Header{}
		header.Set("Authorization", basicAuthHeader("operator", "secret"))
		header.Set("Origin", httptestServer.URL)

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
		if err != nil {
			t.Fatalf("expected websocket upgrade to succeed, got %v", err)
		}
		conn.Close()
	})

	t.Run("accepts same-origin with session cookie", func(t *testing.T) {
		sessionCookie := issueSessionCookie(t, httptestServer.URL)
		header := http.Header{}
		header.Set("Cookie", sessionCookie.String())
		header.Set("Origin", httptestServer.URL)

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
		if err != nil {
			t.Fatalf("expected websocket upgrade with session cookie to succeed, got %v", err)
		}
		conn.Close()
	})

	t.Run("rejects cross-origin with auth", func(t *testing.T) {
		header := http.Header{}
		header.Set("Authorization", basicAuthHeader("operator", "secret"))
		header.Set("Origin", "http://evil.example")

		_, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
		if err == nil {
			t.Fatal("expected cross-origin websocket upgrade to fail")
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected forbidden websocket response, got %#v", resp)
		}
	})

	t.Run("rejects missing auth", func(t *testing.T) {
		header := http.Header{}
		header.Set("Origin", httptestServer.URL)

		_, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
		if err == nil {
			t.Fatal("expected websocket upgrade without auth to fail")
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected unauthorized websocket response, got %#v", resp)
		}
	})
}

func newTestServer() *Server {
	runtimeState := runtime.NewState()
	appCfg := config.AppConfig{ControlPlaneAuthToken: "secret"}
	portfolioManager := portfolio.NewManager(config.DefaultTradingConfig(), runtimeState)
	closeAll := make(chan domain.OrderRequest, 1)
	return NewServer(portfolioManager, runtimeState, closeAll, appCfg)
}

func basicAuthHeader(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

func newSandboxAwareServer(t *testing.T, handler http.Handler) (server *httptest.Server) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			message := fmt.Sprint(recovered)
			if strings.Contains(message, "operation not permitted") {
				t.Skipf("sandbox does not allow local listener for websocket test: %s", message)
			}
			panic(recovered)
		}
	}()
	return httptest.NewServer(handler)
}

func issueSessionCookie(t *testing.T, baseURL string) *http.Cookie {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/dashboard", nil)
	if err != nil {
		t.Fatalf("failed to build session bootstrap request: %v", err)
	}
	req.Header.Set("Authorization", basicAuthHeader("operator", "secret"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to bootstrap session cookie: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected bootstrap request to succeed, got %d", resp.StatusCode)
	}

	return findCookie(t, resp.Cookies(), controlPlaneSessionCookieName)
}

func findCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("expected cookie %q to be set", name)
	return nil
}
