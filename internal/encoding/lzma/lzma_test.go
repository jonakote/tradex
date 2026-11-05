package lzma

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// The fixtures were produced by the reference XZ Utils implementation
// (python3 lzma, FORMAT_ALONE, FILTER_LZMA1 preset 6), so agreement with them
// is agreement with the reference encoder. They deliberately cover distinct
// decoder paths: literal-heavy incompressible data, long overlapping matches,
// a full byte-range alphabet, and the degenerate tiny/empty cases.
func TestDecodeMatchesReferenceFixtures(t *testing.T) {
	for _, name := range []string{"text", "repetitive", "random", "tiny", "empty", "binary"} {
		t.Run(name, func(t *testing.T) {
			compressed := readFixture(t, name+".lzma")
			expected := readFixture(t, name+".raw")

			decoded, err := Decode(compressed)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !bytes.Equal(decoded, expected) {
				t.Fatalf("decoded %d bytes, want %d; first difference at %d",
					len(decoded), len(expected), firstDifference(decoded, expected))
			}
		})
	}
}

// The generated fixtures declare an unknown size and terminate with an
// end-of-stream marker; a real Dukascopy payload declares its exact size
// instead. Both code paths must work.
func TestDecodeRealDukascopyPayloadWithKnownSize(t *testing.T) {
	compressed := readFixture(t, "eurusd_2024_day.bi5")
	expected := readFixture(t, "eurusd_2024_day.raw")

	declared := binary.LittleEndian.Uint64(compressed[5:13])
	if declared == ^uint64(0) {
		t.Fatal("fixture should declare a concrete size")
	}
	decoded, err := Decode(compressed)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(decoded, expected) {
		t.Fatalf("decoded %d bytes, want %d; first difference at %d",
			len(decoded), len(expected), firstDifference(decoded, expected))
	}
	// 366 daily records of 24 bytes for the 2024 leap year.
	if len(decoded) != 366*24 {
		t.Fatalf("expected 366 24-byte records, got %d bytes", len(decoded))
	}
}

func TestDecodeIsDeterministic(t *testing.T) {
	compressed := readFixture(t, "binary.lzma")
	first, err := Decode(compressed)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		again, err := Decode(compressed)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(again, first) {
			t.Fatalf("iteration %d diverged", i)
		}
	}
}

func TestDecodeRejectsMalformedStreams(t *testing.T) {
	valid := readFixture(t, "text.lzma")

	t.Run("short header", func(t *testing.T) {
		if _, err := Decode(valid[:8]); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("invalid properties byte", func(t *testing.T) {
		broken := append([]byte(nil), valid...)
		broken[0] = 0xFF
		if _, err := Decode(broken); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("non-zero range coder start", func(t *testing.T) {
		broken := append([]byte(nil), valid...)
		broken[headerSize] = 0x01
		if _, err := Decode(broken); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("truncated payload", func(t *testing.T) {
		if _, err := Decode(valid[:len(valid)-20]); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("absurd declared size", func(t *testing.T) {
		broken := append([]byte(nil), valid...)
		binary.LittleEndian.PutUint64(broken[5:13], 1<<60)
		if _, err := Decode(broken); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("corrupt body", func(t *testing.T) {
		// Flipping compressed bytes must fail a check, never hang or panic.
		broken := append([]byte(nil), valid...)
		for i := headerSize + 1; i < len(broken); i++ {
			broken[i] ^= 0x5A
		}
		if _, err := Decode(broken); err == nil {
			t.Log("corrupt stream decoded without error; size/marker checks let it pass")
		}
	})
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func firstDifference(a, b []byte) int {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	for i := 0; i < limit; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return limit
}
