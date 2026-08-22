package marketdata

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"tradex/internal/domain"
)

type ResampleReport struct {
	SourceTimeframe time.Duration `json:"source_timeframe"`
	TargetTimeframe time.Duration `json:"target_timeframe"`
	InputCandles    int           `json:"input_candles"`
	OutputCandles   int           `json:"output_candles"`
	DroppedBuckets  int           `json:"dropped_buckets"`
}

func (r ResampleReport) Validate() error {
	if r.SourceTimeframe <= 0 || r.TargetTimeframe <= r.SourceTimeframe ||
		r.TargetTimeframe%r.SourceTimeframe != 0 || r.InputCandles <= 0 ||
		r.OutputCandles < 0 || r.DroppedBuckets < 0 {
		return errors.New("invalid resample report")
	}
	return nil
}

func Resample(candles []domain.Candle, target time.Duration) ([]domain.Candle, ResampleReport, error) {
	if len(candles) == 0 {
		return nil, ResampleReport{}, errors.New("resample source candles are required")
	}
	if _, err := ValidateCandles(candles); err != nil {
		return nil, ResampleReport{}, err
	}
	source := candles[0].Timeframe
	report := ResampleReport{SourceTimeframe: source, TargetTimeframe: target, InputCandles: len(candles)}
	if target <= source || target%source != 0 {
		return nil, report, errors.New("target timeframe must be a larger whole multiple of source timeframe")
	}
	expected := int(target / source)
	result := make([]domain.Candle, 0, len(candles)/expected)
	group := make([]domain.Candle, 0, expected)
	var groupBucket time.Time
	flush := func() error {
		if len(group) == 0 {
			return nil
		}
		complete := len(group) == expected && group[0].OpenTime.Equal(groupBucket) &&
			group[len(group)-1].CloseTime.Equal(groupBucket.Add(target))
		for index := 1; complete && index < len(group); index++ {
			complete = group[index].OpenTime.Equal(group[index-1].CloseTime)
		}
		if !complete {
			report.DroppedBuckets++
			group = group[:0]
			return nil
		}
		bar := domain.Candle{
			Source: group[0].Source, Symbol: group[0].Symbol, Timeframe: target,
			OpenTime: groupBucket, CloseTime: groupBucket.Add(target), Open: group[0].Open,
			High: group[0].High, Low: group[0].Low, Close: group[len(group)-1].Close,
		}
		for _, candle := range group {
			if candle.High > bar.High {
				bar.High = candle.High
			}
			if candle.Low < bar.Low {
				bar.Low = candle.Low
			}
			if math.MaxUint64-bar.TickCount < candle.TickCount {
				return errors.New("resampled tick count overflow")
			}
			bar.TickCount += candle.TickCount
		}
		result = append(result, bar)
		group = group[:0]
		return nil
	}

	for _, candle := range candles {
		bucket := candle.OpenTime.UTC().Truncate(target)
		if len(group) > 0 && !bucket.Equal(groupBucket) {
			previousBucket := groupBucket
			if err := flush(); err != nil {
				return nil, report, err
			}
			if skipped := int(bucket.Sub(previousBucket)/target) - 1; skipped > 0 {
				report.DroppedBuckets += skipped
			}
		}
		if len(group) == 0 {
			groupBucket = bucket
		}
		group = append(group, candle)
	}
	if err := flush(); err != nil {
		return nil, report, err
	}
	report.OutputCandles = len(result)
	if err := report.Validate(); err != nil {
		return nil, report, err
	}
	return result, report, nil
}

// ResampleDataset accepts exactly one raw base stream and returns that stream
// plus each requested derived timeframe with explicit transformation lineage.
func ResampleDataset(dataset CandleDataset, targets []time.Duration) (CandleDataset, []ResampleReport, error) {
	if err := dataset.Validate(); err != nil {
		return CandleDataset{}, nil, err
	}
	if len(dataset.Streams) != 1 || len(targets) == 0 {
		return CandleDataset{}, nil, errors.New("dataset resampling requires one base stream and at least one target")
	}
	parentFingerprint, err := dataset.Fingerprint()
	if err != nil {
		return CandleDataset{}, nil, err
	}
	targets = append([]time.Duration(nil), targets...)
	sort.Slice(targets, func(i, j int) bool { return targets[i] < targets[j] })
	base := dataset.Streams[0]
	output := dataset
	output.Provenance.DatasetID = dataset.Provenance.DatasetID + "/resampled-v1"
	output.ParentFingerprint = parentFingerprint
	output.Transformations = nil
	output.Streams = []CandleStream{base}
	reports := make([]ResampleReport, 0, len(targets))
	seen := make(map[time.Duration]bool, len(targets))
	for _, target := range targets {
		if seen[target] {
			return CandleDataset{}, nil, fmt.Errorf("duplicate resample target %s", target)
		}
		seen[target] = true
		candles, report, err := Resample(base.Candles, target)
		if err != nil {
			return CandleDataset{}, nil, fmt.Errorf("resample %s: %w", target, err)
		}
		if len(candles) == 0 {
			return CandleDataset{}, nil, fmt.Errorf("resample %s produced no complete candles", target)
		}
		output.Streams = append(output.Streams, CandleStream{Symbol: base.Symbol, Timeframe: target, Candles: candles})
		output.Transformations = append(output.Transformations, Transformation{
			Kind: "OHLC_RESAMPLE", Version: "v1", SourceTimeframe: base.Timeframe, TargetTimeframe: target,
		})
		reports = append(reports, report)
	}
	if err := output.Validate(); err != nil {
		return CandleDataset{}, nil, err
	}
	return output, reports, nil
}
