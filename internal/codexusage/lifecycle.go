package codexusage

import (
	"errors"
	"time"
)

// Confirmation is bounded and independent of the cancelled query context. A
// timeout is uncertainty, never permission to inspect auth or remove its home.
func waitForServerExit(empty func() (bool, error)) error {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		done, err := empty()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-timer.C:
			return errors.New("server lifecycle termination was not confirmed")
		case <-ticker.C:
		}
	}
}
