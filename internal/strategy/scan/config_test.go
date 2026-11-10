package scan

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigStrictDetectorSelection(t *testing.T) {
	for _, name := range []string{"continuation", "ict", "amn", "ibs"} {
		t.Run(name, func(t *testing.T) {
			var c Config
			if err := json.Unmarshal([]byte(`{"name":"`+name+`"}`), &c); err != nil {
				t.Fatal(err)
			}
			d, err := c.Build()
			if err != nil || d == nil || d.Validated() {
				t.Fatalf("detector %v: %v", d, err)
			}
		})
	}
	for _, body := range []string{`{"name":"icc"}`, `{"icc":{}}`, `{"name":"ibs","unknown":1}`, `{"name":"ibs","hold_bars":-1}`, `{"name":"ibs","long_below_bps":10001}`, `{"name":"ibs","continuation":{}}`} {
		var c Config
		err := json.Unmarshal([]byte(body), &c)
		if err == nil {
			_, err = c.Build()
		}
		if err == nil {
			t.Fatalf("accepted %s", body)
		}
		if strings.Contains(body, "icc") && !strings.Contains(strings.ToLower(err.Error()), "migration") {
			t.Fatalf("missing migration error: %v", err)
		}
	}
}
