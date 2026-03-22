package sector

// SectorForSymbol returns the GICS sector for a symbol using a static lookup.
// Falls back to "unknown" if the symbol is not in the map.
func SectorForSymbol(symbol string) string {
	if s, ok := sectorMap[symbol]; ok {
		return s
	}
	return "unknown"
}

// sectorMap provides a static mapping of commonly traded momentum stocks to their GICS sectors.
// This is a pragmatic initial implementation; a future version could fetch from the Alpaca assets API.
var sectorMap = map[string]string{
	// Technology
	"AAPL": "technology", "MSFT": "technology", "GOOGL": "technology", "GOOG": "technology",
	"META": "technology", "NVDA": "technology", "AMD": "technology", "INTC": "technology",
	"TSM": "technology", "AVGO": "technology", "QCOM": "technology", "MU": "technology",
	"AMAT": "technology", "LRCX": "technology", "KLAC": "technology", "MRVL": "technology",
	"CRM": "technology", "ORCL": "technology", "NOW": "technology", "ADBE": "technology",
	"SNPS": "technology", "CDNS": "technology", "PLTR": "technology", "DELL": "technology",
	"NET": "technology", "CRWD": "technology", "PANW": "technology", "ZS": "technology",
	"DDOG": "technology", "SNOW": "technology", "MDB": "technology", "SHOP": "technology",
	"SQ": "technology", "COIN": "technology", "MSTR": "technology", "SMCI": "technology",
	"ARM": "technology", "IONQ": "technology", "RGTI": "technology", "QUBT": "technology",

	// Healthcare / Biotech
	"JNJ": "healthcare", "UNH": "healthcare", "PFE": "healthcare", "MRK": "healthcare",
	"ABBV": "healthcare", "LLY": "healthcare", "TMO": "healthcare", "ABT": "healthcare",
	"AMGN": "healthcare", "BMY": "healthcare", "GILD": "healthcare", "REGN": "healthcare",
	"VRTX": "healthcare", "ISRG": "healthcare", "MRNA": "healthcare", "BNTX": "healthcare",
	"NVAX": "healthcare", "DNA": "healthcare", "CRSP": "healthcare", "EDIT": "healthcare",
	"NTLA": "healthcare", "BEAM": "healthcare", "SGEN": "healthcare", "BIIB": "healthcare",

	// Financials
	"JPM": "financials", "BAC": "financials", "WFC": "financials", "GS": "financials",
	"MS": "financials", "C": "financials", "BLK": "financials", "SCHW": "financials",
	"V": "financials", "MA": "financials", "AXP": "financials", "PYPL": "financials",
	"COF": "financials", "USB": "financials", "PNC": "financials", "TFC": "financials",
	"SOFI": "financials", "AFRM": "financials", "HOOD": "financials", "NU": "financials",

	// Consumer Discretionary
	"AMZN": "consumer_discretionary", "TSLA": "consumer_discretionary", "HD": "consumer_discretionary",
	"NKE": "consumer_discretionary", "SBUX": "consumer_discretionary", "MCD": "consumer_discretionary",
	"TGT": "consumer_discretionary", "LOW": "consumer_discretionary", "BABA": "consumer_discretionary",
	"JD": "consumer_discretionary", "PDD": "consumer_discretionary", "RIVN": "consumer_discretionary",
	"LCID": "consumer_discretionary", "NIO": "consumer_discretionary", "XPEV": "consumer_discretionary",
	"LI": "consumer_discretionary", "GM": "consumer_discretionary", "F": "consumer_discretionary",

	// Consumer Staples
	"PG": "consumer_staples", "KO": "consumer_staples", "PEP": "consumer_staples",
	"WMT": "consumer_staples", "COST": "consumer_staples", "CL": "consumer_staples",
	"MDLZ": "consumer_staples", "PM": "consumer_staples", "MO": "consumer_staples",

	// Energy
	"XOM": "energy", "CVX": "energy", "COP": "energy", "SLB": "energy",
	"EOG": "energy", "OXY": "energy", "MPC": "energy", "VLO": "energy",
	"PSX": "energy", "DVN": "energy", "HAL": "energy", "FANG": "energy",

	// Industrials
	"CAT": "industrials", "DE": "industrials", "BA": "industrials", "RTX": "industrials",
	"HON": "industrials", "GE": "industrials", "LMT": "industrials", "NOC": "industrials",
	"UPS": "industrials", "FDX": "industrials", "UNP": "industrials", "CSX": "industrials",

	// Materials
	"LIN": "materials", "APD": "materials", "SHW": "materials", "DD": "materials",
	"NEM": "materials", "FCX": "materials", "NUE": "materials", "CLF": "materials",
	"X": "materials", "AA": "materials", "GOLD": "materials",

	// Communication Services
	"DIS": "communication_services", "NFLX": "communication_services", "CMCSA": "communication_services",
	"T": "communication_services", "VZ": "communication_services", "TMUS": "communication_services",
	"ROKU": "communication_services", "SNAP": "communication_services", "PINS": "communication_services",
	"SPOT": "communication_services", "RBLX": "communication_services", "TTWO": "communication_services",
	"EA": "communication_services", "ATVI": "communication_services",

	// Real Estate
	"AMT": "real_estate", "PLD": "real_estate", "CCI": "real_estate",
	"EQIX": "real_estate", "SPG": "real_estate", "O": "real_estate",

	// Utilities
	"NEE": "utilities", "DUK": "utilities", "SO": "utilities",
	"D": "utilities", "AEP": "utilities", "EXC": "utilities",

	// ETFs / Indices (treated as their own category)
	"SPY": "etf", "QQQ": "etf", "IWM": "etf", "DIA": "etf",
	"XLF": "etf", "XLE": "etf", "XLK": "etf", "XLV": "etf",
	"ARKK": "etf", "SOXL": "etf", "TQQQ": "etf", "SQQQ": "etf",
}
