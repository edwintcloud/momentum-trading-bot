package main

import (
	"sort"
	"strings"

	"github.com/edwintcloud/momentum-trading-bot/internal/alpaca"
)

var scannerETFKeywords = []string{
	" ETF ",
	" ETN ",
	" ETP ",
	" EXCHANGE TRADED ",
	" INDEX FUND ",
}

var scannerETPSponsorKeywords = []string{
	" PROSHARES ",
	" DIREXION ",
	" ISHARES ",
	" SPDR ",
	" GLOBAL X ",
	" VANECK ",
	" INVESCO ",
	" FIRST TRUST ",
	" WISDOMTREE ",
	" GRAYSCALE ",
	" VOLATILITY SHARES ",
	" ROUNDHILL ",
	" YIELDMAX ",
	" DEFIANCE ",
	" BITWISE ",
	" ARK ",
}

var scannerDerivativeKeywords = []string{
	" LEVERAGED ",
	" INVERSE ",
	" ULTRA ",
	" ULTRAPRO ",
	" 2X ",
	" 3X ",
	" -1X ",
	" -2X ",
	" -3X ",
	" SHORT ",
	" BEAR ",
	" BULL ",
	" FUTURES ",
	" VOLATILITY ",
}

func filterScannerUniverseAssets(assets []alpaca.EquityAsset, configured []string) ([]string, map[string]string) {
	assetBySymbol := make(map[string]alpaca.EquityAsset, len(assets))
	blocked := make(map[string]string)
	allowed := make([]string, 0, len(assets))
	for _, asset := range assets {
		assetBySymbol[asset.Symbol] = asset
	}

	if len(configured) > 0 {
		seen := make(map[string]struct{}, len(configured))
		for _, raw := range configured {
			symbol := strings.ToUpper(strings.TrimSpace(raw))
			if symbol == "" {
				continue
			}
			if _, exists := seen[symbol]; exists {
				continue
			}
			seen[symbol] = struct{}{}

			if asset, ok := assetBySymbol[symbol]; ok {
				if reason, shouldBlock := scannerInstrumentBlockReason(asset.Name); shouldBlock {
					blocked[symbol] = reason
					continue
				}
			}
			allowed = append(allowed, symbol)
		}
		sort.Strings(allowed)
		return allowed, blocked
	}

	for _, asset := range assets {
		if reason, shouldBlock := scannerInstrumentBlockReason(asset.Name); shouldBlock {
			blocked[asset.Symbol] = reason
			continue
		}
		allowed = append(allowed, asset.Symbol)
	}

	sort.Strings(allowed)
	return allowed, blocked
}

func scannerInstrumentBlockReason(name string) (string, bool) {
	normalized := normalizeInstrumentName(name)
	if normalized == "" {
		return "", false
	}
	for _, keyword := range scannerETFKeywords {
		if strings.Contains(normalized, keyword) {
			return "instrument-etf", true
		}
	}
	for _, keyword := range scannerETPSponsorKeywords {
		if strings.Contains(normalized, keyword) {
			return "instrument-etf", true
		}
	}
	for _, keyword := range scannerDerivativeKeywords {
		if strings.Contains(normalized, keyword) {
			return "instrument-derivative", true
		}
	}
	return "", false
}

func normalizeInstrumentName(name string) string {
	upper := strings.ToUpper(strings.TrimSpace(name))
	replacer := strings.NewReplacer(
		".", " ",
		",", " ",
		"/", " ",
		"-", " ",
		"_", " ",
		"&", " ",
	)
	upper = replacer.Replace(upper)
	return " " + strings.Join(strings.Fields(upper), " ") + " "
}
