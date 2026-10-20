package codexusage

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestReadResponseMatchesOneIDAndLeavesTheNextReply(t *testing.T) {
	for _, id := range []string{"2", `"2"`} {
		t.Run(id, func(t *testing.T) {
			input := "\n" + `{"method":"notification"}` + "\n" +
				`{"id":null,"result":{}}` + "\n" +
				`{"id":"unknown","result":{}}` + "\n" +
				`{"id":1,"result":{}}` + "\n" +
				fmt.Sprintf(`{"id":%s,"result":{"value":21}}`, id) + "\n" +
				`{"id":3,"error":{"code":-32601}}` + "\n"
			scanner := bufio.NewScanner(strings.NewReader(input))
			response, err := readResponse(scanner, 2)
			if err != nil || string(response.Result) != `{"value":21}` || response.Error != nil {
				t.Fatalf("matching response was not returned: %v", err)
			}
			next, err := readResponse(scanner, 3)
			if err != nil || next.Error == nil || next.Error.Code != -32601 {
				t.Fatalf("the next RPC reply was consumed or changed: %v", err)
			}
		})
	}
}

func TestReadResponseDoesNotMaskReadFailureAfterCompleteJSON(t *testing.T) {
	scanner := bufio.NewScanner(iotest.TimeoutReader(strings.NewReader(`{"id":2,"result":{}}`)))
	response, err := readResponse(scanner, 2)
	if !errors.Is(err, iotest.ErrTimeout) || len(response.ID) != 0 {
		t.Fatalf("complete JSON hid the transport failure: %v", err)
	}
}

func TestReadResponseRejectsMissingMalformedAndOversizedReplies(t *testing.T) {
	for name, input := range map[string]string{
		"missing":   `{"id":1,"result":{}}` + "\n",
		"malformed": `{"id":2,"result":`,
		"oversized": strings.Repeat("x", 128),
	} {
		t.Run(name, func(t *testing.T) {
			scanner := bufio.NewScanner(strings.NewReader(input))
			scanner.Buffer(make([]byte, 32), 64)
			_, err := readResponse(scanner, 2)
			if err == nil || (name == "missing" && !errors.Is(err, io.ErrUnexpectedEOF)) {
				t.Fatalf("invalid reply was accepted or lost its EOF error: %v", err)
			}
		})
	}
}
