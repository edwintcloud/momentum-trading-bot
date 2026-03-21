package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

// Server wraps the HTTP API.
type Server struct {
	portfolio *portfolio.Manager
	runtime   *runtime.State
	closeAll  chan<- domain.OrderRequest
	upgrader  websocket.Upgrader
	authToken string
}

// NewServer creates an API server.
func NewServer(portfolioManager *portfolio.Manager, runtimeState *runtime.State, closeAll chan<- domain.OrderRequest, appConfig config.AppConfig) *Server {
	return &Server{
		portfolio: portfolioManager,
		runtime:   runtimeState,
		closeAll:  closeAll,
		upgrader:  websocket.Upgrader{CheckOrigin: sameOriginRequest},
		authToken: strings.TrimSpace(appConfig.ControlPlaneAuthToken),
	}
}

// Start begins serving HTTP on the given address.
func (s *Server) Start(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("api: listening on %s", addr)
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.HandleFunc("/api/status", s.requireControlPlaneAuth(s.handleStatus))
	mux.HandleFunc("/api/positions", s.requireControlPlaneAuth(s.handlePositions))
	mux.HandleFunc("/api/candidates", s.requireControlPlaneAuth(s.handleCandidates))
	mux.HandleFunc("/api/trades", s.requireControlPlaneAuth(s.handleTrades))
	mux.HandleFunc("/api/logs", s.requireControlPlaneAuth(s.handleLogs))
	mux.HandleFunc("/api/dashboard", s.requireControlPlaneAuth(s.handleDashboard))
	mux.HandleFunc("/api/pause", s.requireControlPlaneAuth(s.handlePause))
	mux.HandleFunc("/api/resume", s.requireControlPlaneAuth(s.handleResume))
	mux.HandleFunc("/api/close-all", s.requireControlPlaneAuth(s.handleCloseAll))
	mux.HandleFunc("/api/emergency-stop", s.requireControlPlaneAuth(s.handleEmergencyStop))
	mux.HandleFunc("/ws", s.requireControlPlaneAuth(s.handleWebSocket))
	mux.Handle("/", s.requireControlPlaneAuth(s.webHandler().ServeHTTP))
	return mux
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.portfolio.StatusSnapshot())
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now()})
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	payload := map[string]any{
		"ready":        s.runtime.IsReady(),
		"dependencies": s.runtime.DependencyStatuses(),
	}
	if !s.runtime.IsReady() {
		s.writeJSON(w, http.StatusServiceUnavailable, payload)
		return
	}
	s.writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handlePositions(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.portfolio.GetPositions())
}

func (s *Server) handleCandidates(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.runtime.Candidates())
}

func (s *Server) handleTrades(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.portfolio.GetClosedTrades())
}

func (s *Server) handleLogs(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.runtime.Logs())
}

func (s *Server) handleDashboard(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.snapshot())
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.runtime.Pause()
	s.runtime.RecordLog("warn", "control", "trading paused")
	s.writeJSON(w, http.StatusOK, map[string]any{"paused": true})
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resumed := s.runtime.Resume()
	if resumed {
		s.runtime.RecordLog("info", "control", "trading resumed")
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"resumed": resumed, "emergencyStop": s.runtime.IsEmergencyStopped()})
}

func (s *Server) handleCloseAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	orders := s.portfolio.PendingCloseAll("operator-close-all")
	for _, order := range orders {
		s.closeAll <- order
	}
	s.runtime.RecordLog("warn", "control", "close-all triggered")
	s.writeJSON(w, http.StatusOK, map[string]any{"queued": len(orders)})
}

func (s *Server) handleEmergencyStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.runtime.EmergencyStop()
	orders := s.portfolio.PendingCloseAll("emergency-stop")
	for _, order := range orders {
		s.closeAll <- order
	}
	s.runtime.RecordLog("error", "control", "emergency stop triggered")
	s.writeJSON(w, http.StatusOK, map[string]any{"emergencyStop": true, "queued": len(orders)})
}

func (s *Server) requireControlPlaneAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.authToken == "" {
			s.unauthorized(w)
			return
		}
		if s.hasValidControlPlaneSession(r) {
			next(w, r)
			return
		}
		username, password, ok := r.BasicAuth()
		if !ok || username != "operator" || subtle.ConstantTimeCompare([]byte(password), []byte(s.authToken)) != 1 {
			s.unauthorized(w)
			return
		}
		s.setControlPlaneSession(w, r)
		next(w, r)
	}
}

func (s *Server) unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="Momentum Trading Bot"`)
	http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
}

const (
	wsWriteTimeout = 5 * time.Second
	wsPingInterval = 25 * time.Second
	wsPongTimeout  = 60 * time.Second

	controlPlaneSessionCookieName = "mtb_control_plane_session"
	controlPlaneSessionTTL        = 30 * 24 * time.Hour
)

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Extend read deadline on every pong so we can detect dead connections.
	conn.SetReadDeadline(time.Now().Add(wsPongTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(wsPongTimeout))
		return nil
	})

	// Reader goroutine drains control frames (pings/pongs) and closes done
	// when the client disconnects or the read deadline fires.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	pingTicker := time.NewTicker(wsPingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case <-done:
			return
		case <-r.Context().Done():
			return
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := conn.WriteJSON(s.snapshot()); err != nil {
				return
			}
		case <-pingTicker.C:
			conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (s *Server) snapshot() domain.DashboardSnapshot {
	return domain.DashboardSnapshot{
		Status:       s.portfolio.StatusSnapshot(),
		MarketRegime: s.runtime.MarketRegime(),
		Candidates:   s.runtime.Candidates(),
		Positions:    s.portfolio.GetPositions(),
		ClosedTrades: s.portfolio.GetClosedTrades(),
		Logs:         s.runtime.Logs(),
		UpdatedAt:    time.Now(),
	}
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) webHandler() http.Handler {
	fs := http.FileServer(http.Dir("web/dist"))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		indexPath := filepath.Join("web", "dist", "index.html")
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			if _, err := os.Stat(indexPath); err != nil {
				http.Error(w, "dashboard assets not built", http.StatusServiceUnavailable)
				return
			}
			http.ServeFile(w, r, indexPath)
			return
		}

		assetPath := filepath.Join("web", "dist", filepath.Clean(r.URL.Path))
		if !strings.HasPrefix(assetPath, filepath.Join("web", "dist")) {
			http.NotFound(w, r)
			return
		}
		if _, err := os.Stat(assetPath); err != nil {
			if _, statErr := os.Stat(indexPath); statErr != nil {
				http.Error(w, "dashboard assets not built", http.StatusServiceUnavailable)
				return
			}
			http.ServeFile(w, r, indexPath)
			return
		}
		fs.ServeHTTP(w, r)
	})
}

func sameOriginRequest(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	originURL, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if originURL.Host == "" || r.Host == "" {
		return false
	}
	return strings.EqualFold(originURL.Host, r.Host)
}

func (s *Server) hasValidControlPlaneSession(r *http.Request) bool {
	cookie, err := r.Cookie(controlPlaneSessionCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}

	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return false
	}

	expiresAt, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > expiresAt {
		return false
	}

	expected := s.signControlPlaneSession(parts[0])
	return subtle.ConstantTimeCompare([]byte(parts[1]), []byte(expected)) == 1
}

func (s *Server) setControlPlaneSession(w http.ResponseWriter, r *http.Request) {
	expiresAt := time.Now().Add(controlPlaneSessionTTL)
	payload := strconv.FormatInt(expiresAt.Unix(), 10)
	http.SetCookie(w, &http.Cookie{
		Name:     controlPlaneSessionCookieName,
		Value:    payload + "." + s.signControlPlaneSession(payload),
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(controlPlaneSessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
}

func (s *Server) signControlPlaneSession(payload string) string {
	mac := hmac.New(sha256.New, []byte(s.authToken))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
