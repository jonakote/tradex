package replay

import (
	"errors"
	"fmt"
	"time"

	"tradex/internal/economics"
	"tradex/internal/marketdata"
)

func CompileConversionDataset(base Scenario, dataset marketdata.ConversionDataset, accountMinorScale int64, observedAt time.Time, maxAge time.Duration) (Scenario, error) {
	if err := base.Validate(); err != nil {
		return Scenario{}, fmt.Errorf("validate base scenario: %w", err)
	}
	if err := dataset.Validate(); err != nil {
		return Scenario{}, err
	}
	if accountMinorScale <= 0 || observedAt.IsZero() || maxAge <= 0 {
		return Scenario{}, errors.New("conversion compilation requires minor scale, observation time, and maximum age")
	}
	rate, err := dataset.RateAt(base.Instrument.QuoteCurrency, dataset.AccountCurrency, observedAt, maxAge)
	if err != nil {
		return Scenario{}, err
	}
	value, err := economics.NewPriceValue(base.Instrument, dataset.AccountCurrency, accountMinorScale, &rate)
	if err != nil {
		return Scenario{}, err
	}
	result := base
	result.Name = base.Name + "-conversion-" + dataset.Provenance.DatasetID
	result.PriceValue = value
	result.Execution.PriceValue = value
	result.ConversionDataset = &dataset
	if err := result.Validate(); err != nil {
		return Scenario{}, fmt.Errorf("validate conversion-compiled scenario: %w", err)
	}
	return result, nil
}
