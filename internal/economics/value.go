package economics

import (
	"errors"
	"math/big"
	"time"

	"tradex/internal/domain"
)

// Rate expresses units of To currency per one unit of From currency.
// Example: JPY->USD at 0.0068 is Numerator=6800, Denominator=1_000_000.
type Rate struct {
	From        string
	To          string
	Numerator   int64
	Denominator int64
	Source      string
	ObservedAt  time.Time
}

func (r Rate) Validate() error {
	if r.From == "" || r.To == "" || r.From == r.To || r.Numerator <= 0 || r.Denominator <= 0 ||
		r.Source == "" || r.ObservedAt.IsZero() {
		return errors.New("invalid currency conversion rate")
	}
	return nil
}

// PriceValue is account-currency minor units per one integer Price unit and
// one instrument quantity step. It remains rational so JPY crosses and other
// fractional-cent values are not rounded before total trade P&L is known.
type PriceValue struct {
	AccountCurrency      string
	Numerator            int64
	Denominator          int64
	ConversionSource     string
	ConversionObservedAt time.Time
}

func NewPriceValue(instrument domain.Instrument, accountCurrency string, accountMinorScale int64, conversion *Rate) (PriceValue, error) {
	if err := instrument.Validate(); err != nil {
		return PriceValue{}, err
	}
	if accountCurrency == "" || accountMinorScale <= 0 {
		return PriceValue{}, errors.New("account currency and minor scale are required")
	}

	numerator := big.NewInt(instrument.QuantityStep)
	numerator.Mul(numerator, big.NewInt(accountMinorScale))
	denominator := big.NewInt(instrument.PriceScale)
	if instrument.QuoteCurrency != accountCurrency {
		if conversion == nil {
			return PriceValue{}, errors.New("quote-to-account conversion rate is required")
		}
		if err := conversion.Validate(); err != nil {
			return PriceValue{}, err
		}
		if conversion.From != instrument.QuoteCurrency || conversion.To != accountCurrency {
			return PriceValue{}, errors.New("conversion direction does not match quote and account currencies")
		}
		numerator.Mul(numerator, big.NewInt(conversion.Numerator))
		denominator.Mul(denominator, big.NewInt(conversion.Denominator))
	} else if conversion != nil {
		return PriceValue{}, errors.New("conversion must be omitted when quote equals account currency")
	}

	gcd := new(big.Int).GCD(nil, nil, numerator, denominator)
	numerator.Quo(numerator, gcd)
	denominator.Quo(denominator, gcd)
	if !numerator.IsInt64() || !denominator.IsInt64() {
		return PriceValue{}, errors.New("price value ratio exceeds int64 storage")
	}
	value := PriceValue{AccountCurrency: accountCurrency, Numerator: numerator.Int64(), Denominator: denominator.Int64()}
	if conversion != nil {
		value.ConversionSource = conversion.Source
		value.ConversionObservedAt = conversion.ObservedAt.UTC()
	}
	return value, nil
}

func (v PriceValue) Validate() error {
	if v.AccountCurrency == "" || v.Numerator <= 0 || v.Denominator <= 0 {
		return errors.New("invalid account-currency price value")
	}
	return nil
}

func (v PriceValue) UsesConversion() bool {
	return v.ConversionSource != "" || !v.ConversionObservedAt.IsZero()
}

func (v PriceValue) ValidateAt(now time.Time, maxAge time.Duration) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if !v.UsesConversion() {
		return nil
	}
	if v.ConversionSource == "" || v.ConversionObservedAt.IsZero() || now.IsZero() || maxAge <= 0 {
		return errors.New("conversion source, observation time, current time, and maximum age are required")
	}
	age := now.Sub(v.ConversionObservedAt)
	if age < 0 || age > maxAge {
		return errors.New("currency conversion rate is stale or from the future")
	}
	return nil
}

// LossMinor rounds away from zero so risk is never understated.
func (v PriceValue) LossMinor(distance domain.Price, quantity, quantityStep int64) (int64, error) {
	if distance <= 0 {
		return 0, errors.New("loss distance must be positive")
	}
	return v.amount(int64(distance), quantity, quantityStep, true)
}

// RewardMinor rounds down so expected reward is never overstated.
func (v PriceValue) RewardMinor(distance domain.Price, quantity, quantityStep int64) (int64, error) {
	if distance <= 0 {
		return 0, errors.New("reward distance must be positive")
	}
	return v.amount(int64(distance), quantity, quantityStep, false)
}

// PnLMinor rounds signed P&L toward zero after applying the full quantity.
func (v PriceValue) PnLMinor(delta domain.Price, quantity, quantityStep int64) (int64, error) {
	return v.amount(int64(delta), quantity, quantityStep, false)
}

// PriceDistanceForMinorCeil returns the smallest whole price-unit distance
// whose value for one quantity step is at least the requested minor amount.
func (v PriceValue) PriceDistanceForMinorCeil(minor int64) (domain.Price, error) {
	if err := v.Validate(); err != nil {
		return 0, err
	}
	if minor <= 0 {
		return 0, errors.New("minor amount must be positive")
	}
	numerator := big.NewInt(minor)
	numerator.Mul(numerator, big.NewInt(v.Denominator))
	divisor := big.NewInt(v.Numerator)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, divisor, remainder)
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, errors.New("price distance exceeds int64")
	}
	return domain.Price(quotient.Int64()), nil
}

func (v PriceValue) amount(priceUnits, quantity, quantityStep int64, roundAway bool) (int64, error) {
	if err := v.Validate(); err != nil {
		return 0, err
	}
	if quantity <= 0 || quantityStep <= 0 || quantity%quantityStep != 0 {
		return 0, errors.New("quantity must be a positive multiple of its step")
	}
	steps := quantity / quantityStep
	numerator := big.NewInt(priceUnits)
	numerator.Mul(numerator, big.NewInt(steps))
	numerator.Mul(numerator, big.NewInt(v.Numerator))
	denominator := big.NewInt(v.Denominator)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	if roundAway && remainder.Sign() != 0 {
		if numerator.Sign() > 0 {
			quotient.Add(quotient, big.NewInt(1))
		} else {
			quotient.Sub(quotient, big.NewInt(1))
		}
	}
	if !quotient.IsInt64() {
		return 0, errors.New("account-currency amount exceeds int64")
	}
	return quotient.Int64(), nil
}
