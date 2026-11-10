package scan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"tradex/internal/features"
)

// Config is the versioned research detector configuration. Only the selected
// detector's parameters may be supplied; omitted parameters use its defaults.
// It contains no execution authorization or strategy validation override.
type Config struct {
	Name          string                       `json:"name"`
	Continuation  *features.ContinuationConfig `json:"continuation,omitempty"`
	ICT           *features.ICTConfig          `json:"ict,omitempty"`
	AMN           *features.AMNConfig          `json:"amn,omitempty"`
	HoldBars      int                          `json:"hold_bars,omitempty"`
	LongBelowBps  int64                        `json:"long_below_bps,omitempty"`
	ShortAboveBps int64                        `json:"short_above_bps,omitempty"`
}

func (c *Config) UnmarshalJSON(data []byte) error {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	for k := range keys {
		if k == "icc" || k == "ICC" {
			return errors.New("ICC migration required: replace ICC configuration with a scan detector configuration")
		}
	}
	type plain Config
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("detector config contains trailing JSON")
	}
	candidate := Config(value)
	if _, err := candidate.Build(); err != nil {
		return err
	}
	*c = candidate
	return nil
}

func (c Config) Build() (Detector, error) {
	if c.Name == "icc" || c.Name == "ICC" {
		return nil, errors.New("ICC migration required: choose continuation, ict, amn, or ibs and rebuild the manifest")
	}
	if c.Continuation != nil && c.Name != "continuation" || c.ICT != nil && c.Name != "ict" || c.AMN != nil && c.Name != "amn" || c.Name != "ibs" && (c.HoldBars != 0 || c.LongBelowBps != 0 || c.ShortAboveBps != 0) {
		return nil, errors.New("parameters belong to a different detector")
	}
	switch c.Name {
	case "continuation":
		p := features.DefaultContinuationConfig()
		if c.Continuation != nil {
			p = *c.Continuation
		}
		if err := p.Validate(); err != nil {
			return nil, err
		}
		return Continuation{Config: p}, nil
	case "ict":
		p := features.DefaultICTConfig()
		if c.ICT != nil {
			p = *c.ICT
		}
		if err := p.Validate(); err != nil {
			return nil, err
		}
		return ICT{Config: p}, nil
	case "amn":
		p := features.DefaultAMNConfig()
		if c.AMN != nil {
			p = *c.AMN
		}
		if err := p.Validate(); err != nil {
			return nil, err
		}
		return AMN{Config: p}, nil
	case "ibs":
		if c.HoldBars < 0 || c.LongBelowBps < 0 || c.ShortAboveBps < 0 || c.LongBelowBps > 10000 || c.ShortAboveBps > 10000 {
			return nil, errors.New("invalid IBS hold or threshold")
		}
		hold, long, short := c.HoldBars, c.LongBelowBps, c.ShortAboveBps
		if hold == 0 {
			hold = 1
		}
		if long == 0 && short == 0 {
			long, short = 2000, 8000
		}
		if short > 0 && long >= short {
			return nil, errors.New("IBS thresholds overlap")
		}
		return IBS{HoldBars: hold, LongBelowBps: long, ShortAboveBps: short}, nil
	default:
		return nil, fmt.Errorf("unknown detector %q", c.Name)
	}
}
