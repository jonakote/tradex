// Capital.com epic mapping.
//
// A Capital.com "epic" is a venue-specific market identifier, not a symbol:
// gold trades as GOLD, silver as SILVER. Passing a tradex symbol straight
// through either 404s or, worse, resolves to an unrelated market that happens
// to match the search term (searching XAUUSD returns XANUSD and XAUTUSD).
//
// The mapping is explicit and verified against GET /api/v1/markets rather than
// derived from the symbol string, and it fails closed for anything unlisted.
//
// Crypto is deliberately absent. Capital.com quotes BTCUSD, which is a
// different instrument from the Binance BTCUSDT pair already in the registry;
// treating them as one symbol would merge two venues' price series. Crypto
// research stays on Binance.
package instrument

// capitalEpics maps canonical symbol to Capital.com epic. Verified present and
// TRADEABLE via GET /api/v1/markets?epics= on 2026-11-06.
var capitalEpics = map[string]string{
	// FX majors and crosses use the plain symbol as the epic.
	"EURUSD": "EURUSD", "GBPUSD": "GBPUSD", "AUDUSD": "AUDUSD", "NZDUSD": "NZDUSD",
	"USDCAD": "USDCAD", "USDCHF": "USDCHF",
	"EURGBP": "EURGBP", "EURCHF": "EURCHF", "EURAUD": "EURAUD", "EURCAD": "EURCAD",
	"EURNZD": "EURNZD",
	"GBPAUD": "GBPAUD", "GBPCAD": "GBPCAD", "GBPCHF": "GBPCHF", "GBPNZD": "GBPNZD",
	"AUDCAD": "AUDCAD", "AUDCHF": "AUDCHF", "AUDNZD": "AUDNZD",
	"NZDCAD": "NZDCAD", "NZDCHF": "NZDCHF", "CADCHF": "CADCHF",
	"USDJPY": "USDJPY", "EURJPY": "EURJPY", "GBPJPY": "GBPJPY", "AUDJPY": "AUDJPY",
	"NZDJPY": "NZDJPY", "CADJPY": "CADJPY", "CHFJPY": "CHFJPY",

	// Metals do NOT use the symbol as the epic.
	"XAUUSD": "GOLD",
	"XAGUSD": "SILVER",

	// US equity indices. Verified present and TRADEABLE via
	// GET /api/v1/markets?searchTerm=... on 2026-11-10: tickSize 0.1,
	// pipPosition 0, scalingFactor 1 on all three (1 decimal place, NOT
	// the 2-digit guess instrument.go previously carried for these as
	// Unverified).
	"US500": "US500", // S&P 500
	"US100": "US100", // Nasdaq 100 ("US Tech 100")
	"US30":  "US30",  // Dow ("US Wall Street 30")
}

// CapitalEpic returns the Capital.com epic for a symbol in any spelling. It
// reports false when the instrument has no verified epic, so a caller cannot
// accidentally request a market that merely resembles the symbol.
func CapitalEpic(symbol string) (string, bool) {
	epic, ok := capitalEpics[Normalize(symbol)]
	return epic, ok
}

// CapitalSymbol resolves an epic back to its canonical symbol, so candles
// arriving from the venue are labelled in tradex's own vocabulary.
func CapitalSymbol(epic string) (string, bool) {
	target := Normalize(epic)
	for symbol, candidate := range capitalEpics {
		if candidate == target {
			return symbol, true
		}
	}
	return "", false
}
