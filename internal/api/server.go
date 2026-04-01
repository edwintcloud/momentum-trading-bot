package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/optimizer"
	"github.com/edwintcloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
)

const (
	wsWriteTimeout = 5 * time.Second
	wsPingInterval = 25 * time.Second
	wsPongTimeout  = 60 * time.Second

	controlPlaneSessionCookieName = "mtb_control_plane_session"
	controlPlaneSessionTTL        = 30 * 24 * time.Hour
)

// ConfigUpdater can receive updated trading configs.
type ConfigUpdater interface {
	UpdateConfig(cfg config.TradingConfig)
}

// Server wraps the HTTP API.
type Server struct {
	portfolio        *portfolio.Manager
	runtime          *runtime.State
	closeAll         chan<- domain.OrderRequest
	upgrader         websocket.Upgrader
	authToken        string
	tradingConfig    config.TradingConfig
	configUpdaters   []ConfigUpdater
	optimizerDir     string
	cachedArtifact   optimizer.ArtifactStatus
	cachedArtifactAt time.Time
	historyLoader    interface{}
	refreshPrices    func() // called before close-all to get fresh prices from broker
}

// NewServer creates an API server.
func NewServer(
	portfolioManager *portfolio.Manager,
	runtimeState *runtime.State,
	closeAll chan<- domain.OrderRequest,
	appConfig config.AppConfig,
	tradingConfig config.TradingConfig,
	optimizerDir string,
	historyLoader interface{},
) *Server {
	return &Server{
		portfolio:     portfolioManager,
		runtime:       runtimeState,
		closeAll:      closeAll,
		upgrader:      websocket.Upgrader{CheckOrigin: sameOriginRequest},
		authToken:     strings.TrimSpace(appConfig.ControlPlaneAuthToken),
		tradingConfig: tradingConfig,
		optimizerDir:  optimizerDir,
		historyLoader: historyLoader,
	}
}

// RegisterConfigUpdater adds a component that will be notified of config changes.
func (s *Server) RegisterConfigUpdater(u ConfigUpdater) {
	s.configUpdaters = append(s.configUpdaters, u)
}

// SetPriceRefresher sets a callback to refresh portfolio prices from the broker.
func (s *Server) SetPriceRefresher(fn func()) {
	s.refreshPrices = fn
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
	// Public health endpoints
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)

	// Protected API endpoints
	mux.HandleFunc("/api/status", s.requireAuth(s.handleStatus))
	mux.HandleFunc("/api/positions", s.requireAuth(s.handlePositions))
	mux.HandleFunc("/api/candidates", s.requireAuth(s.handleCandidates))
	mux.HandleFunc("/api/trades", s.requireAuth(s.handleTrades))
	mux.HandleFunc("/api/trades/history", s.requireAuth(s.handleTradeHistory))
	mux.HandleFunc("/api/trades/export", s.requireAuth(s.handleTradeExport))
	mux.HandleFunc("/api/trades/dates", s.requireAuth(s.handleTradeDates))
	mux.HandleFunc("/api/logs", s.requireAuth(s.handleLogs))
	mux.HandleFunc("/api/dashboard", s.requireAuth(s.handleDashboard))
	mux.HandleFunc("/api/performance", s.requireAuth(s.handlePerformance))
	mux.HandleFunc("/api/config", s.requireAuth(s.handleConfig))
	mux.HandleFunc("/api/pause", s.requireAuth(s.handlePause))
	mux.HandleFunc("/api/resume", s.requireAuth(s.handleResume))
	mux.HandleFunc("/api/close-all", s.requireAuth(s.handleCloseAll))
	mux.HandleFunc("/api/emergency-stop", s.requireAuth(s.handleEmergencyStop))
	mux.HandleFunc("/ws", s.requireAuth(s.handleWebSocket))
	mux.Handle("/", s.requireAuth(s.webHandler().ServeHTTP))
	return mux
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

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	snap := s.portfolio.StatusSnapshot()
	snap.Paused = s.runtime.IsPaused()
	snap.EmergencyStop = s.runtime.IsEmergencyStopped()
	s.writeJSON(w, http.StatusOK, snap)
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

func (s *Server) handleTradeHistory(w http.ResponseWriter, r *http.Request) {
	loc := markethours.Location()
	dateStr := r.URL.Query().Get("date")
	if dateStr == "" {
		dateStr = time.Now().In(loc).Format("2006-01-02")
	}
	date, err := time.ParseInLocation("2006-01-02", dateStr, loc)
	if err != nil {
		http.Error(w, "invalid date format, use YYYY-MM-DD", http.StatusBadRequest)
		return
	}

	loader, ok := s.historyLoader.(domain.HistoryLoader)
	if !ok {
		s.writeJSON(w, http.StatusOK, s.portfolio.GetClosedTrades())
		return
	}

	trades, err := loader.LoadClosedTradesByDate(date)
	if err != nil {
		http.Error(w, "failed to load trades: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, trades)
}

func (s *Server) handleTradeExport(w http.ResponseWriter, r *http.Request) {
	loc := markethours.Location()
	dateStr := r.URL.Query().Get("date")
	if dateStr == "" {
		dateStr = time.Now().In(loc).Format("2006-01-02")
	}
	date, err := time.ParseInLocation("2006-01-02", dateStr, loc)
	if err != nil {
		http.Error(w, "invalid date format", http.StatusBadRequest)
		return
	}

	var trades []domain.ClosedTrade
	if loader, ok := s.historyLoader.(domain.HistoryLoader); ok {
		trades, err = loader.LoadClosedTradesByDate(date)
		if err != nil {
			http.Error(w, "failed to load trades", http.StatusInternalServerError)
			return
		}
	} else {
		trades = s.portfolio.GetClosedTrades()
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=trades_%s.csv", dateStr))

	csvWriter := csv.NewWriter(w)
	csvWriter.Write([]string{
		"Symbol", "Side", "Quantity", "Entry Price", "Exit Price", "PnL", "R-Multiple",
		"Setup Type", "Exit Reason", "Market Regime", "Sector",
		"Opened At", "Closed At", "Duration",
	})

	for _, t := range trades {
		dur := ""
		if !t.OpenedAt.IsZero() && !t.ClosedAt.IsZero() {
			dur = t.ClosedAt.Sub(t.OpenedAt).Round(time.Second).String()
		}
		csvWriter.Write([]string{
			t.Symbol, t.Side, fmt.Sprintf("%d", t.Quantity),
			fmt.Sprintf("%.2f", t.EntryPrice), fmt.Sprintf("%.2f", t.ExitPrice),
			fmt.Sprintf("%.2f", t.PnL), fmt.Sprintf("%.4f", t.RMultiple),
			t.SetupType, t.ExitReason, t.MarketRegime, t.Sector,
			t.OpenedAt.In(loc).Format("2006-01-02 15:04:05"),
			t.ClosedAt.In(loc).Format("2006-01-02 15:04:05"),
			dur,
		})
	}
	csvWriter.Flush()
}

func (s *Server) handleTradeDates(w http.ResponseWriter, _ *http.Request) {
	loader, ok := s.historyLoader.(domain.HistoryLoader)
	if !ok {
		s.writeJSON(w, http.StatusOK, []string{time.Now().In(markethours.Location()).Format("2006-01-02")})
		return
	}
	dates, err := loader.ListTradeDates()
	if err != nil {
		http.Error(w, "failed to list dates", http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, dates)
}

func (s *Server) handleLogs(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.runtime.Logs())
}

func (s *Server) handleDashboard(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.snapshot())
}

func (s *Server) handlePerformance(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.portfolio.PerformanceMetrics())
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.writeJSON(w, http.StatusOK, s.tradingConfig)
		return
	}
	if r.Method == http.MethodPost {
		var cfg config.TradingConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, "invalid config body", http.StatusBadRequest)
			return
		}
		s.tradingConfig = cfg
		for _, u := range s.configUpdaters {
			u.UpdateConfig(cfg)
		}
		s.runtime.RecordLog("warn", "control", "trading config updated via API")
		s.writeJSON(w, http.StatusOK, map[string]any{"updated": true})
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	// Refresh portfolio prices from broker before generating close orders
	// so limit prices reflect the current market, not stale cached values.
	if s.refreshPrices != nil {
		s.refreshPrices()
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
	// Refresh portfolio prices from broker before generating close orders.
	if s.refreshPrices != nil {
		s.refreshPrices()
	}
	orders := s.portfolio.PendingCloseAll("emergency-stop")
	for _, order := range orders {
		s.closeAll <- order
	}
	s.runtime.RecordLog("error", "control", "emergency stop triggered")
	s.writeJSON(w, http.StatusOK, map[string]any{"emergencyStop": true, "queued": len(orders)})
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(wsPongTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(wsPongTimeout))
		return nil
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(500 * time.Millisecond)
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
	status := s.portfolio.StatusSnapshot()
	status.Paused = s.runtime.IsPaused()
	status.EmergencyStop = s.runtime.IsEmergencyStopped()

	// Merge optimizer artifact status
	artifactStatus := s.optimizerStatus()
	status.LastOptimizerRun = artifactStatus.LastOptimizerRun
	status.PendingProfile = artifactStatus.PendingProfileName
	status.PendingVersion = artifactStatus.PendingProfileVersion
	status.PaperValidation = artifactStatus.LastPaperValidationResult

	return domain.DashboardSnapshot{
		Status:       status,
		MarketRegime: s.runtime.MarketRegime(),
		Candidates:   s.runtime.Candidates(),
		Positions:    s.portfolio.GetPositions(),
		ClosedTrades: s.portfolio.GetClosedTrades(),
		Logs:         s.runtime.Logs(),
		UpdatedAt:    time.Now(),
	}
}

func (s *Server) optimizerStatus() optimizer.ArtifactStatus {
	if time.Since(s.cachedArtifactAt) < 60*time.Second {
		return s.cachedArtifact
	}
	if status, err := optimizer.LoadArtifactStatus(s.optimizerDir); err == nil {
		s.cachedArtifact = status
		s.cachedArtifactAt = time.Now()
	}
	return s.cachedArtifact
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
				http.Error(w, "dashboard assets not built — run: cd web && npm install && npm run build", http.StatusServiceUnavailable)
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

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.authToken == "" {
			s.unauthorized(w)
			return
		}
		if s.hasValidSession(r) {
			next(w, r)
			return
		}
		username, password, ok := r.BasicAuth()
		if !ok || username != "operator" || subtle.ConstantTimeCompare([]byte(password), []byte(s.authToken)) != 1 {
			s.unauthorized(w)
			return
		}
		s.setSession(w, r)
		next(w, r)
	}
}

func (s *Server) unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="Momentum Trading Bot"`)
	http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
}

func (s *Server) hasValidSession(r *http.Request) bool {
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
	expected := s.signSession(parts[0])
	return subtle.ConstantTimeCompare([]byte(parts[1]), []byte(expected)) == 1
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request) {
	expiresAt := time.Now().Add(controlPlaneSessionTTL)
	payload := strconv.FormatInt(expiresAt.Unix(), 10)
	http.SetCookie(w, &http.Cookie{
		Name:     controlPlaneSessionCookieName,
		Value:    payload + "." + s.signSession(payload),
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(controlPlaneSessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
}

func (s *Server) signSession(payload string) string {
	mac := hmac.New(sha256.New, []byte(s.authToken))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func sameOriginRequest(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true // non-browser clients don't send Origin
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
