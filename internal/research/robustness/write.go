package robustness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

func WriteResultNew(path string, result Result) (writeErr error) {
	if path == "" {
		return errors.New("robustness result path is required")
	}
	if err := result.Verify(); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create robustness result: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); writeErr == nil && closeErr != nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			_ = os.Remove(path)
		}
	}()
	written, err := file.Write(encoded)
	if err != nil {
		return err
	}
	if written != len(encoded) {
		return errors.New("write robustness result: short write")
	}
	return file.Sync()
}
