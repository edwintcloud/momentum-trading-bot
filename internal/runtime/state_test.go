package runtime

import "testing"

func TestIsReadyRequiresAllDependenciesHealthy(t *testing.T) {
	state := NewState()
	state.SetDependencyStatus("database", true, "ok")
	state.SetDependencyStatus("alpaca_trading", true, "ok")
	state.SetDependencyStatus("market_data_stream", false, "waiting")

	if state.IsReady() {
		t.Fatal("expected not ready while one dependency is unhealthy")
	}

	state.SetDependencyStatus("market_data_stream", true, "ok")
	if !state.IsReady() {
		t.Fatal("expected ready when all dependencies are healthy")
	}
}
