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
	sig "tradex/internal/signal"
)

// SignalsConfig is the JSON configuration for the signals command.
type SignalsConfig struct {
	TwelveData   *feed.TwelveDataConfig `json:"twelve_data,omitempty"`
	FCS          *feed.FCSConfig        `json:"fcs,omitempty"`
	Webhook      *feed.WebhookConfig    `json:"webhook,omitempty"`
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

	// Check for API key from env if not in config
	if cfg.TwelveData != nil && cfg.TwelveData.APIKey == "" {
		cfg.TwelveData.APIKey = os.Getenv("TWELVE_DATA_API_KEY")
	}
	if cfg.FCS != nil && cfg.FCS.APIKey == "" {
		cfg.FCS.APIKey = os.Getenv("FCS_API_KEY")
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
	engine, signals := sig.NewEngine(engineCfg)

	// Print header
	fmt.Fprintln(stdout, "═══════════════════════════════════════════════════")
	fmt.Fprintln(stdout, "  TRADEX LIVE SIGNAL MONITOR")
	fmt.Fprintln(stdout, "═══════════════════════════════════════════════════")
	fmt.Fprintf(stdout, "  Started:  %s\n", time.Now().Format("2006-01-02 15:04 UTC"))

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
		TwelveData: &feed.TwelveDataConfig{
			Symbols:  feed.DefaultSymbols(),
			Interval: "1h",
		},
		FCS: &feed.FCSConfig{
			Symbols:  feed.DefaultFCSSymbols(),
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
