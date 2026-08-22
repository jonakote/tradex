package derive

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"tradex/internal/domain"
	"tradex/internal/features"
	"tradex/internal/marketdata"
)

type Config struct {
	SwingLeft  int `json:"swing_left"`
	SwingRight int `json:"swing_right"`
}

func (c Config) Validate() error {
	if c.SwingLeft <= 0 || c.SwingRight <= 0 {
		return errors.New("derivation swing windows must be positive")
	}
	return nil
}

type Event struct {
	At              time.Time                 `json:"at"`
	Symbol          string                    `json:"symbol"`
	Timeframe       time.Duration             `json:"timeframe"`
	Candle          domain.Candle             `json:"candle"`
	ConfirmedSwings []features.Swing          `json:"confirmed_swings,omitempty"`
	Trend           features.Trend            `json:"trend"`
	StructureBreaks []features.StructureBreak `json:"structure_breaks,omitempty"`
}

func (e Event) EventTime() time.Time { return e.At }

type StreamSummary struct {
	Symbol          string         `json:"symbol"`
	Timeframe       time.Duration  `json:"timeframe"`
	Candles         int            `json:"candles"`
	ConfirmedSwings int            `json:"confirmed_swings"`
	StructureBreaks int            `json:"structure_breaks"`
	FinalTrend      features.Trend `json:"final_trend"`
}

type Report struct {
	DatasetFingerprint string          `json:"dataset_fingerprint"`
	Events             []Event         `json:"events"`
	Streams            []StreamSummary `json:"streams"`
}

// Structure derives only from completed candles. A pivot appears on the event
// that supplies its full right-hand confirmation window, never at pivot time.
func Structure(dataset marketdata.CandleDataset, config Config) (Report, error) {
	if err := dataset.Validate(); err != nil {
		return Report{}, err
	}
	if err := config.Validate(); err != nil {
		return Report{}, err
	}
	fingerprint, err := dataset.Fingerprint()
	if err != nil {
		return Report{}, err
	}
	report := Report{DatasetFingerprint: fingerprint}
	for _, stream := range dataset.Streams {
		swingDetector, err := features.NewSwingDetector(config.SwingLeft, config.SwingRight)
		if err != nil {
			return Report{}, err
		}
		structureDetector := features.StructureDetector{}
		allSwings := make([]features.Swing, 0)
		summary := StreamSummary{Symbol: stream.Symbol, Timeframe: stream.Timeframe, Candles: len(stream.Candles)}
		for index, candle := range stream.Candles {
			confirmed, err := swingDetector.Push(candle)
			if err != nil {
				return Report{}, fmt.Errorf("derive stream %s/%s candle %d: %w", stream.Symbol, stream.Timeframe, index, err)
			}
			allSwings = append(allSwings, confirmed...)
			breaks := structureDetector.OnCandle(candle, confirmed)
			trend := features.ClassifyTrend(allSwings)
			report.Events = append(report.Events, Event{
				At: candle.CloseTime, Symbol: stream.Symbol, Timeframe: stream.Timeframe,
				Candle: candle, ConfirmedSwings: confirmed, Trend: trend, StructureBreaks: breaks,
			})
			summary.ConfirmedSwings += len(confirmed)
			summary.StructureBreaks += len(breaks)
			summary.FinalTrend = trend
		}
		report.Streams = append(report.Streams, summary)
	}
	sort.SliceStable(report.Events, func(i, j int) bool {
		if report.Events[i].At.Equal(report.Events[j].At) {
			if report.Events[i].Timeframe == report.Events[j].Timeframe {
				return report.Events[i].Symbol < report.Events[j].Symbol
			}
			return report.Events[i].Timeframe > report.Events[j].Timeframe
		}
		return report.Events[i].At.Before(report.Events[j].At)
	})
	return report, nil
}
