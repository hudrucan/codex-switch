package codexusage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
	"github.com/SilkageNet/codex-switch/internal/authschema"
)

const maxProtocolMessage = 8 << 20

type Runner struct {
	Binary        string
	BinaryError   error
	ClientVersion string
	command       func(context.Context, string, ...string) *exec.Cmd
	confirmExit   func(func() (bool, error)) error
}

type rpcRequest struct {
	Method string `json:"method"`
	ID     *int   `json:"id,omitempty"`
	Params any    `json:"params,omitempty"`
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code int `json:"code"`
}

func (runner Runner) Query(ctx context.Context, auth json.RawMessage) (snapshot Snapshot, updated json.RawMessage, result error) {
	defer func() { result = RedactError(result) }()
	if runner.BinaryError != nil {
		return Snapshot{}, nil, failure("Codex app server", "unavailable", runner.BinaryError)
	}
	if runner.Binary == "" {
		return Snapshot{}, nil, failure("Codex app server", "unavailable", nil)
	}
	document, err := authschema.Parse(auth)
	if err != nil {
		return Snapshot{}, nil, failure("account usage query", "credential validation failed", err)
	}
	temporaryHome, err := os.MkdirTemp("", "codex-switch-usage-*")
	if err != nil {
		return Snapshot{}, nil, failure("temporary CODEX_HOME", "I/O failure", err)
	}
	removeHome := true
	defer func() {
		if !removeHome {
			return
		}
		if err := os.RemoveAll(temporaryHome); err != nil {
			result = errors.Join(result, failure("temporary CODEX_HOME", "cleanup failed", err))
			snapshot = Snapshot{}
		}
	}()
	if err := os.Chmod(temporaryHome, 0o700); err != nil {
		return Snapshot{}, nil, failure("temporary CODEX_HOME", "I/O failure", err)
	}
	if err := atomicfile.Write(filepath.Join(temporaryHome, "config.toml"), []byte("cli_auth_credentials_store = \"file\"\n"), 0o600); err != nil {
		return Snapshot{}, nil, failure("temporary CODEX_HOME", "I/O failure", err)
	}
	if err := atomicfile.Write(filepath.Join(temporaryHome, "auth.json"), append(append([]byte(nil), document.Raw...), '\n'), 0o600); err != nil {
		return Snapshot{}, nil, failure("temporary CODEX_HOME", "I/O failure", err)
	}

	commandFactory := runner.command
	if commandFactory == nil {
		commandFactory = exec.CommandContext
	}
	command := commandFactory(ctx, runner.Binary, "app-server")
	environment := command.Env
	if environment == nil {
		environment = os.Environ()
	}
	command.Env = withEnvironment(environment, "CODEX_HOME", temporaryHome)
	stdin, err := command.StdinPipe()
	if err != nil {
		return Snapshot{}, nil, failure("Codex app server", "I/O failure", err)
	}
	defer func() { _ = stdin.Close() }()
	stdout, err := command.StdoutPipe()
	if err != nil {
		return Snapshot{}, nil, failure("Codex app server", "I/O failure", err)
	}
	defer func() { _ = stdout.Close() }()
	command.Stderr = io.Discard
	command.WaitDelay = 2 * time.Second
	lifecycle, err := newServerLifecycle(command)
	if err != nil {
		return Snapshot{}, nil, failure("Codex app server", "process failure", err)
	}
	defer func() {
		if err := lifecycle.close(); err != nil {
			result = errors.Join(result, failure("Codex app server", "cleanup failed", err))
			snapshot = Snapshot{}
		}
	}()
	if command.Cancel != nil {
		command.Cancel = lifecycle.terminate
	}
	if err := command.Start(); err != nil {
		return Snapshot{}, nil, failure("Codex app server", "process failure", err)
	}
	removeHome = false
	waited := false
	cancellationDone := make(chan struct{})
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = stdin.Close()
		_ = stdout.Close()
		defer close(cancellationDone)
		_ = lifecycle.terminate()
	})
	defer func() {
		if !stopCancellation() {
			<-cancellationDone
		}
		_ = stdin.Close()
		_ = lifecycle.terminate()
		if !waited {
			_ = command.Wait()
		}
		_ = stdout.Close()
		if result != nil && ctx.Err() != nil {
			result = errors.Join(result, ctx.Err())
		}
		confirmExit := runner.confirmExit
		if confirmExit == nil {
			confirmExit = waitForServerExit
		}
		if err := confirmExit(lifecycle.empty); err != nil || command.ProcessState == nil {
			result = errors.Join(result, failure("Codex app server", "cleanup failed", err))
			snapshot = Snapshot{}
			return
		}
		removeHome = true
		recovered, recoveryErr := recoverCredentials(filepath.Join(temporaryHome, "auth.json"), document)
		if recoveryErr != nil {
			result = errors.Join(result, recoveryErr)
		} else {
			updated = recovered
		}
		if result != nil {
			snapshot = Snapshot{}
		}
	}()

	if err := lifecycle.started(); err != nil {
		return Snapshot{}, nil, failure("Codex app server", "process failure", err)
	}

	encoder := json.NewEncoder(stdin)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), maxProtocolMessage)
	version := runner.ClientVersion
	if version == "" {
		version = "dev"
	}
	if err := sendRequest(encoder, 0, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "codex_switch", "title": "codex-switch", "version": version},
	}); err != nil {
		return Snapshot{}, nil, err
	}
	initialize, err := readResponse(scanner, 0)
	if err != nil {
		return Snapshot{}, nil, protocolError("initialize", err)
	}
	if initialize.Error != nil {
		return Snapshot{}, nil, responseError("initialize", initialize.Error)
	}
	if err := encoder.Encode(rpcRequest{Method: "initialized", Params: map[string]any{}}); err != nil {
		return Snapshot{}, nil, failure("initialized", "request failed", err)
	}
	if err := sendRequest(encoder, 1, "account/read", map[string]bool{"refreshToken": false}); err != nil {
		return Snapshot{}, nil, err
	}
	accountResponse, err := readResponse(scanner, 1)
	if err != nil {
		return Snapshot{}, nil, protocolError("account/read", err)
	}
	if accountResponse.Error != nil {
		return Snapshot{}, nil, responseError("account/read", accountResponse.Error)
	}
	var account AccountResponse
	if err := json.Unmarshal(accountResponse.Result, &account); err != nil {
		return Snapshot{}, nil, failure("account/read", "invalid response", err)
	}
	if account.Account == nil || account.Account.Type != "chatgpt" {
		return Snapshot{}, nil, errors.New("saved profile is not recognized as a ChatGPT account by Codex")
	}

	if err := sendRequest(encoder, 2, "account/rateLimits/read", nil); err != nil {
		return Snapshot{}, nil, err
	}
	rateLimitsResponse, err := readResponse(scanner, 2)
	if err != nil {
		return Snapshot{}, nil, protocolError("account/rateLimits/read", err)
	}
	if err := sendRequest(encoder, 3, "account/usage/read", nil); err != nil {
		return Snapshot{}, nil, err
	}
	tokenUsageResponse, err := readResponse(scanner, 3)
	if err != nil {
		return Snapshot{}, nil, protocolError("account/usage/read", err)
	}
	_ = stdin.Close()
	if err := command.Wait(); err != nil {
		waited = true
		if ctx.Err() != nil {
			return Snapshot{}, nil, fmt.Errorf("query account usage: %w", ctx.Err())
		}
		return Snapshot{}, nil, failure("Codex app server", "process failure", err)
	}
	waited = true

	snapshot = Snapshot{FetchedAt: time.Now().UTC(), PlanType: account.Account.PlanType}
	if rateLimitsResponse.Error != nil {
		snapshot.Partial = append(snapshot.Partial, "rateLimits")
	} else {
		var value RateLimits
		if err := json.Unmarshal(rateLimitsResponse.Result, &value); err != nil {
			return Snapshot{}, nil, failure("account/rateLimits/read", "invalid response", err)
		}
		snapshot.RateLimits = &value
	}
	if tokenUsageResponse.Error != nil {
		snapshot.Partial = append(snapshot.Partial, "tokenUsage")
	} else {
		var value TokenUsage
		if err := json.Unmarshal(tokenUsageResponse.Result, &value); err != nil {
			return Snapshot{}, nil, failure("account/usage/read", "invalid response", err)
		}
		snapshot.TokenUsage = &value
	}
	if snapshot.RateLimits == nil && snapshot.TokenUsage == nil {
		return Snapshot{}, nil, errors.Join(
			responseError("account/rateLimits/read", rateLimitsResponse.Error),
			responseError("account/usage/read", tokenUsageResponse.Error),
		)
	}
	return snapshot, nil, nil
}

func sendRequest(encoder *json.Encoder, id int, method string, params any) error {
	if err := encoder.Encode(rpcRequest{Method: method, ID: &id, Params: params}); err != nil {
		return failure(method, "request failed", err)
	}
	return nil
}

func readResponse(scanner *bufio.Scanner, expected int) (rpcResponse, error) {
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var response rpcResponse
		if err := json.Unmarshal(line, &response); err != nil {
			return rpcResponse{}, failure("account usage query", "invalid response", err)
		}
		if len(response.ID) == 0 || bytes.Equal(response.ID, []byte("null")) {
			continue
		}
		id, err := strconv.Atoi(strings.Trim(string(response.ID), `"`))
		if err != nil || id != expected {
			continue
		}
		// A final JSON token can arrive together with a non-EOF read failure.
		if err := scanner.Err(); err != nil {
			return rpcResponse{}, err
		}
		return response, nil
	}
	if err := scanner.Err(); err != nil {
		return rpcResponse{}, err
	}
	return rpcResponse{}, io.ErrUnexpectedEOF
}

func responseError(method string, rpcErr *rpcError) error {
	if rpcErr == nil {
		return failure(method, "protocol failure", nil)
	}
	if rpcErr.Code < -32768 || rpcErr.Code > 32767 {
		return failure(method, "protocol failure", nil)
	}
	message := fmt.Sprintf("%s failed (RPC code %d, diagnostics redacted)", safeOperation(method), rpcErr.Code)
	return &publicError{message: NormalizeErrorMessage(message)}
}

func protocolError(operation string, err error) error {
	return failure(operation, "protocol failure", err)
}

func recoverCredentials(path string, original authschema.Document) (json.RawMessage, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, failure("refreshed credentials", "I/O failure", err)
	}
	raw, err := atomicfile.ReadLimited(path, 2<<20)
	if err != nil {
		return nil, failure("refreshed credentials", "I/O failure", err)
	}
	candidate, err := authschema.Parse(raw)
	if err != nil {
		return nil, errors.New("refreshed credentials are invalid")
	}
	decision, err := authschema.ValidateRefresh(original, candidate)
	if err != nil {
		return nil, err
	}
	if decision == authschema.GenerationUseSaved {
		return nil, errors.New("refreshed credentials are older than the validated generation")
	}
	return candidate.Raw, nil
}

func withEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}
