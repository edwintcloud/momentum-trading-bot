package api

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
