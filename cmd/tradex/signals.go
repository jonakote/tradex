package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tradex/internal/feed"
	"tradex/internal/feed/dukascopy"
	sig "tradex/internal/signal"
)

// SignalsConfig is the JSON configuration for the signals command.
type SignalsConfig struct {
	Binance      *feed.BinanceConfig    `json:"binance,omitempty"`
	TwelveData   *feed.TwelveDataConfig `json:"twelve_data,omitempty"`
	FCS          *feed.FCSConfig        `json:"fcs,omitempty"`
	Finnhub      *feed.FinnhubConfig    `json:"finnhub,omitempty"`
	Webhook      *feed.WebhookConfig    `json:"webhook,omitempty"`
	Dukascopy    *dukascopy.LiveConfig  `json:"dukascopy,omitempty"`
	Engine       *sig.EngineConfig      `json:"engine,omitempty"`
	PollInterval string                 `json:"poll_interval,omitempty"` // e.g. "5m", "15m"
}

func runSignals(args []string, stdout, stderr io.Writer) int {
	cfg := defaultSignalsConfig()

	if len(args) >= 1 {
		file, err := os.Open(args[0])
		if err != nil {
			fmt.Fprintf(stderr, "tradex signals: open config: %v\n", err)
			return 1
		}
		defer file.Close()
		dec := json.NewDecoder(file)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			fmt.Fprintf(stderr, "tradex signals: parse config: %v\n", err)
			return 1
		}
	}

	// Binance public market-data endpoints do not require an API key. If one is
	// supplied, pass it through as the standard Binance API-key header.
	if cfg.Binance != nil && cfg.Binance.APIKey == "" {
		cfg.Binance.APIKey = os.Getenv("BINANCE_API_KEY")
	}

	// Check for API keys from env if not in config
	if cfg.TwelveData != nil && cfg.TwelveData.APIKey == "" {
		cfg.TwelveData.APIKey = os.Getenv("TWELVE_DATA_API_KEY")
	}
	if cfg.FCS != nil && cfg.FCS.APIKey == "" {
		cfg.FCS.APIKey = os.Getenv("FCS_API_KEY")
	}
	if cfg.Finnhub != nil && cfg.Finnhub.APIKey == "" {
		cfg.Finnhub.APIKey = os.Getenv("FINNHUB_API_KEY")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(stderr, "\ntradex signals: shutting down...")
		cancel()
	}()

	// Create signal engine
	engineCfg := sig.DefaultEngineConfig()
	if cfg.Engine != nil {
		engineCfg = *cfg.Engine
	}
	engineCfg.Verbose = engineCfg.Verbose || os.Getenv("TRADEX_VERBOSE") != ""
	engine, signals := sig.NewEngine(engineCfg)

	// Print header
	fmt.Fprintln(stdout, "═══════════════════════════════════════════════════")
	fmt.Fprintln(stdout, "  TRADEX LIVE SIGNAL MONITOR")
	fmt.Fprintln(stdout, "═══════════════════════════════════════════════════")
	fmt.Fprintf(stdout, "  Started:  %s\n", time.Now().Format("2006-01-02 15:04 UTC"))

	// Start the Dukascopy tick-derived candle poller. It needs no API key,
	// but publishes a bar only after its clock hour completes, so it lags by
	// roughly an hour and must not be used to act on short-timeframe limits.
	if cfg.Dukascopy != nil {
		dukaFeed, err := dukascopy.NewLiveFeed(*cfg.Dukascopy)
		if err != nil {
			fmt.Fprintf(stderr, "tradex signals: dukascopy feed: %v\n", err)
			return 1
		}
		dukaPollInterval := pollIntervalOrDefault(cfg.PollInterval, 10*time.Minute)
		fmt.Fprintf(stdout, "  Dukascopy: %d symbols (%s %s, poll every %s, ~1h publication lag)\n",
			len(dukaFeed.Symbols()), dukaFeed.Interval(), dukaFeed.Side(), dukaPollInterval)

		go func() {
			poll := func() {
				start := time.Now()
				candles, err := dukaFeed.FetchAllSymbols(ctx)
				if err != nil && ctx.Err() == nil {
					fmt.Fprintf(stdout, "%s [dukascopy-poll] failed after %s: %v\n",
						time.Now().UTC().Format(time.RFC3339Nano), time.Since(start).Round(time.Millisecond), err)
					return
				}
				fmt.Fprintf(stdout, "%s [dukascopy-poll] ok in %s: %d candles\n",
					time.Now().UTC().Format(time.RFC3339Nano), time.Since(start).Round(time.Millisecond), len(candles))
				for _, candle := range candles {
					engine.ProcessCandle(candle)
				}
			}
			poll()
			ticker := time.NewTicker(dukaPollInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					poll()
				}
			}
		}()
	}

	// Start Binance public Spot kline poller. Only completed candles are
	// forwarded, so an in-progress bar cannot repaint a signal.
	if cfg.Binance != nil {
		binanceClient, err := feed.NewBinanceClient(*cfg.Binance)
		if err != nil {
			fmt.Fprintf(stderr, "tradex signals: binance client: %v\n", err)
			return 1
		}

		binancePollInterval := pollIntervalOrDefault(cfg.PollInterval, 5*time.Minute)
		fmt.Fprintf(stdout, "  Binance:  %d symbols (%s, poll every %s)\n",
			len(cfg.Binance.Symbols), cfg.Binance.Interval, binancePollInterval)

		go func() {
			poll := func() {
				start := time.Now()
				candles, err := binanceClient.FetchAllSymbols(ctx)
				if err != nil && ctx.Err() == nil {
					fmt.Fprintf(stdout, "%s [binance-poll] failed after %s: %v\n",
						time.Now().UTC().Format(time.RFC3339Nano), time.Since(start).Round(time.Millisecond), err)
					return
				}
				fmt.Fprintf(stdout, "%s [binance-poll] ok in %s: %d candles\n",
					time.Now().UTC().Format(time.RFC3339Nano), time.Since(start).Round(time.Millisecond), len(candles))
				for _, candle := range candles {
					engine.ProcessCandle(candle)
				}
			}
			poll()
			ticker := time.NewTicker(binancePollInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					poll()
				}
			}
		}()
	}

	// Start Twelve Data poller
	if cfg.TwelveData != nil && cfg.TwelveData.APIKey != "" {
		client, err := feed.NewTwelveDataClient(*cfg.TwelveData)
		if err != nil {
			fmt.Fprintf(stderr, "tradex signals: twelve data client: %v\n", err)
			return 1
		}

		pollInterval := 5 * time.Minute
		if cfg.PollInterval != "" {
			if d, err := time.ParseDuration(cfg.PollInterval); err == nil && d > 0 {
				pollInterval = d
			}
		}

		poller, candleCh := feed.NewPoller(client, pollInterval)
		poller.SetLogWriter(stdout)
		go poller.Run(ctx)

		fmt.Fprintf(stdout, "  Feed:     Twelve Data (%d symbols, %s, poll every %s)\n",
			len(cfg.TwelveData.Symbols), cfg.TwelveData.Interval, pollInterval)

		// Route candles to engine
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case candle, ok := <-candleCh:
					if !ok {
						return
					}
					engine.ProcessCandle(candle)
				}
			}
		}()
	} else {
		fmt.Fprintln(stdout, "  Feed:     None (set TWELVE_DATA_API_KEY or provide config)")
	}

	// Start FCS API poller (for silver, oil, indices)
	if cfg.FCS != nil && cfg.FCS.APIKey != "" {
		fcsClient, err := feed.NewFCSClient(*cfg.FCS)
		if err != nil {
			fmt.Fprintf(stderr, "tradex signals: FCS client: %v\n", err)
			return 1
		}

		// FCS has 500 req/month free — poll less frequently (every 30 min)
		fcsPollInterval := 30 * time.Minute

		fmt.Fprintf(stdout, "  FCS Feed: %d symbols (%s, poll every %s)\n",
			len(cfg.FCS.Symbols), cfg.FCS.Interval, fcsPollInterval)

		// FCS polling goroutine
		go func() {
			logPoll := func(outcome string, n int, elapsed time.Duration, err error) {
				if err != nil {
					fmt.Fprintf(stdout, "%s [fcs-poll] %s after %s: %v\n",
						time.Now().UTC().Format(time.RFC3339Nano), outcome, elapsed.Round(time.Millisecond), err)
					return
				}
				fmt.Fprintf(stdout, "%s [fcs-poll] %s in %s: %d candles\n",
					time.Now().UTC().Format(time.RFC3339Nano), outcome, elapsed.Round(time.Millisecond), n)
			}
			// Initial fetch
			start := time.Now()
			candles, err := fcsClient.FetchAllSymbols(ctx)
			if err != nil {
				logPoll("poll failed", 0, time.Since(start), err)
			} else {
				logPoll("poll ok", len(candles), time.Since(start), nil)
			}
			for _, c := range candles {
				engine.ProcessCandle(c)
			}
			ticker := time.NewTicker(fcsPollInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					start := time.Now()
					candles, err := fcsClient.FetchAllSymbols(ctx)
					if err != nil {
						logPoll("poll failed", 0, time.Since(start), err)
						continue
					}
					logPoll("poll ok", len(candles), time.Since(start), nil)
					for _, c := range candles {
						engine.ProcessCandle(c)
					}
				}
			}
		}()
	} else if os.Getenv("FCS_API_KEY") == "" {
		// Only show message if key not set at all
	}

	// Start Finnhub poller (forex via OANDA, stocks/ETFs)
	if cfg.Finnhub != nil && cfg.Finnhub.APIKey != "" {
		finnhubClient, err := feed.NewFinnhubClient(*cfg.Finnhub)
		if err != nil {
			fmt.Fprintf(stderr, "tradex signals: finnhub client: %v\n", err)
			return 1
		}

		// Finnhub free tier allows 60 calls/min — poll on the shared interval
		finnhubPollInterval := pollIntervalOrDefault(cfg.PollInterval, 5*time.Minute)

		fmt.Fprintf(stdout, "  Finnhub:  %d symbols (%s, poll every %s)\n",
			len(cfg.Finnhub.Symbols), cfg.Finnhub.Interval, finnhubPollInterval)

		// Finnhub polling goroutine
		go func() {
			logPoll := func(outcome string, n int, elapsed time.Duration, err error) {
				if err != nil {
					fmt.Fprintf(stdout, "%s [finnhub-poll] %s after %s: %v\n",
						time.Now().UTC().Format(time.RFC3339Nano), outcome, elapsed.Round(time.Millisecond), err)
					return
				}
				fmt.Fprintf(stdout, "%s [finnhub-poll] %s in %s: %d candles\n",
					time.Now().UTC().Format(time.RFC3339Nano), outcome, elapsed.Round(time.Millisecond), n)
			}
			poll := func() {
				start := time.Now()
				candles, err := finnhubClient.FetchAllSymbols(ctx)
				if err != nil && ctx.Err() == nil {
					logPoll("poll failed", 0, time.Since(start), err)
					return
				}
				logPoll("poll ok", len(candles), time.Since(start), nil)
				for _, c := range candles {
					engine.ProcessCandle(c)
				}
			}
			poll()
			ticker := time.NewTicker(finnhubPollInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					poll()
				}
			}
		}()
	}

	// Start webhook receiver
	if cfg.Webhook != nil {
		receiver, alertsCh, err := feed.NewWebhookReceiver(*cfg.Webhook)
		if err != nil {
			fmt.Fprintf(stderr, "tradex signals: webhook receiver: %v\n", err)
			return 1
		}

		go func() {
			if err := receiver.ListenAndServe(ctx); err != nil && ctx.Err() == nil {
				fmt.Fprintf(stderr, "tradex signals: webhook server: %v\n", err)
			}
		}()

		fmt.Fprintf(stdout, "  Webhook:  %s%s\n", cfg.Webhook.ListenAddr, webhookPath(cfg.Webhook))

		// Route webhook candles to engine
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case alert, ok := <-alertsCh:
					if !ok {
						return
					}
					switch alert.Action {
					case "candle_close":
						if candle, err := alert.ToCandle(); err == nil {
							engine.ProcessCandle(candle)
						}
					case "signal_buy", "signal_sell":
						// TradingView signal pass-through
						direction := "BUY"
						if alert.Action == "signal_sell" {
							direction = "SELL"
						}
						tvSignal := sig.Signal{
							Time:       time.Now().UTC(),
							Symbol:     alert.Symbol,
							Direction:  direction,
							Entry:      alert.Entry,
							StopLoss:   alert.StopLoss,
							TakeProfit: alert.TakeProfit,
							Timeframe:  alert.Interval,
							Strategy:   "TradingView: " + alert.Strategy,
							Reason:     "TradingView alert: " + alert.AlertName,
						}
						if tvSignal.Entry > 0 && tvSignal.StopLoss > 0 && tvSignal.TakeProfit > 0 {
							risk := tvSignal.Entry - tvSignal.StopLoss
							if direction == "SELL" {
								risk = tvSignal.StopLoss - tvSignal.Entry
							}
							if risk > 0 {
								reward := tvSignal.TakeProfit - tvSignal.Entry
								if direction == "SELL" {
									reward = tvSignal.Entry - tvSignal.TakeProfit
								}
								tvSignal.RiskReward = reward / risk
							}
						}
						fmt.Fprint(stdout, tvSignal.Format())
					}
				}
			}
		}()
	}

	fmt.Fprintln(stdout, "───────────────────────────────────────────────────")
	fmt.Fprintln(stdout, "  Waiting for signals... (Ctrl+C to exit)")
	fmt.Fprintln(stdout, "")

	// Signal output loop
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(stdout, "\ntradex signals: stopped")
			return 0
		case s, ok := <-signals:
			if !ok {
				return 0
			}
			fmt.Fprint(stdout, s.Format())
		}
	}
}

func defaultSignalsConfig() SignalsConfig {
	return SignalsConfig{
		Binance: &feed.BinanceConfig{
			Symbols:  feed.DefaultBinanceSymbols(),
			Interval: "1h",
			Limit:    100,
		},
		TwelveData: &feed.TwelveDataConfig{
			Symbols:  feed.DefaultSymbols(),
			Interval: "1h",
		},
		FCS: &feed.FCSConfig{
			Symbols:  feed.DefaultFCSSymbols(),
			Interval: "1h",
		},
		Finnhub: &feed.FinnhubConfig{
			Symbols:  feed.DefaultFinnhubSymbols(),
			Interval: "1h",
		},
		Webhook: &feed.WebhookConfig{
			ListenAddr: ":8080",
			Path:       "/webhook",
		},
		PollInterval: "5m",
	}
}

func webhookPath(cfg *feed.WebhookConfig) string {
	if cfg.Path != "" {
		return cfg.Path
	}
	return "/webhook"
}

// pollIntervalOrDefault parses s as a duration, falling back to def when
// empty or invalid.
func pollIntervalOrDefault(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}
