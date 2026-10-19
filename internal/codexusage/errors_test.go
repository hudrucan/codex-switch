package codexusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRPCErrorRedactionSurvivesWrappingAndFormatting(t *testing.T) {
	var rpc rpcError
	if err := json.Unmarshal([]byte(`{"code":-32001,"message":"`+rpcCanary+`","data":{"refresh_token":"`+rpcCanary+`"}}`), &rpc); err != nil {
		t.Fatal("invalid synthetic RPC fixture")
	}
	original := responseError("account/read", &rpc)
	err := RetryError(3, fmt.Errorf("%s: %w", rpcCanary, original))
	for _, format := range []string{"%v", "%+v", "%#v", "%q"} {
		text := fmt.Sprintf(format, err)
		if strings.Contains(text, rpcCanary) || !strings.Contains(text, "account/read") || !strings.Contains(text, "RPC code -32001") {
			t.Fatal("redaction lost safe RPC metadata or exposed diagnostics")
		}
	}
	if !errors.Is(err, original) {
		t.Fatal("redaction lost original error identity")
	}
	encoded, marshalErr := json.Marshal(map[string]string{"error": err.Error()})
	if marshalErr != nil || strings.Contains(string(encoded), rpcCanary) {
		t.Fatal("JSON error exposed diagnostics")
	}
}

func TestUntrustedMessagesAndCodesAreBounded(t *testing.T) {
	for _, message := range []string{rpcCanary, "account/read failed (RPC code -32001, diagnostics redacted) " + rpcCanary, strings.Repeat(rpcCanary, 1000), ""} {
		err := RedactError(errors.New(message))
		if err.Error() == "" || len(err.Error()) > maxPublicError || strings.Contains(err.Error(), rpcCanary) {
			t.Fatal("unsafe or empty public failure")
		}
	}
	if message := responseError("account/read", &rpcError{Code: 123456789}).Error(); strings.Contains(message, "123456789") {
		t.Fatal("unbounded RPC code was exposed")
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		err := RedactError(fmt.Errorf("%s: %w", rpcCanary, cause))
		if !errors.Is(err, cause) || strings.Contains(err.Error(), rpcCanary) {
			t.Fatal("safe cancellation classification was lost")
		}
	}
}

func TestErrorMessagePreservesJoinedClassifications(t *testing.T) {
	safe := responseError("account/read", &rpcError{Code: -32001})
	for name, input := range map[string]error{
		"direct":  safe,
		"wrapped": fmt.Errorf("%s: %w", rpcCanary, safe),
		"joined":  errors.Join(safe, context.Canceled),
	} {
		t.Run(name, func(t *testing.T) {
			redacted := RedactError(input)
			message := redacted.Error()
			if strings.Contains(message, rpcCanary) || !strings.Contains(message, "account/read") || !strings.Contains(message, "RPC code -32001") || !errors.Is(redacted, safe) {
				t.Fatal("safe error metadata or identity changed during redaction")
			}
			if name == "joined" && (!strings.Contains(message, "cancelled") || !errors.Is(redacted, context.Canceled)) {
				t.Fatal("joined error lost its cancellation classification")
			}
		})
	}
}
