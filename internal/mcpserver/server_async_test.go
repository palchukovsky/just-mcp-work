// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/executor"
	"github.com/palchukovsky/just-mcp-work/internal/runmanager"
	"github.com/palchukovsky/just-mcp-work/internal/runstore"
)

func TestSyncReceiptCarriesStatisticsWithoutRunningFields(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	for range 2 {
		_, receipt, err := server.runShellCommand(
			context.Background(),
			nil,
			runShellCommandInput{Command: shellOutputCommand()},
		)
		if err != nil || !receipt.OK {
			t.Fatalf("run = %#v, %v", receipt, err)
		}
	}
	_, receipt, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: shellOutputCommand()},
	)
	if err != nil || !receipt.OK {
		t.Fatalf("third run = %#v, %v", receipt, err)
	}
	if receipt.runDetails == nil || receipt.Stats == nil || receipt.Stats.Exact == nil {
		t.Fatalf("completed receipt = %#v, want the pre-run statistics", receipt)
	}
	if receipt.Stats.Exact.Runs != 2 {
		t.Fatalf("exact runs = %d, want the two finished runs", receipt.Stats.Exact.Runs)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"completed", "promoted", "\"pid\"", "owned_by_this_server"} {
		if strings.Contains(string(encoded), unwanted) {
			t.Fatalf("completed receipt %s must not contain %q", encoded, unwanted)
		}
	}
}

func TestSyncShellReceiptTails(t *testing.T) {
	requested := int64(4096)
	small := int64(3)
	zero := int64(0)
	failingCommand := "printf stdout; printf stderr >&2; exit 1"
	smallTailCommand := "printf 0123456789; printf abcdefghijklmnop >&2"
	successTails := receiptTails{stdout: "shell-output"}
	failingTails := receiptTails{stdout: "stdout", stderr: "stderr"}
	smallTails := receiptTails{stdout: "789", stderr: "nop"}
	successStdoutBytes := int64(12)
	smallStdoutBytes := int64(10)
	smallStderrBytes := int64(16)
	if runtime.GOOS == "windows" {
		failingCommand = "echo stdout& echo stderr>&2& exit /b 1"
		smallTailCommand = "echo 0123456789& echo abcdefghijklmnop>&2"
		successTails = receiptTails{stdout: "shell-output\r\n"}
		failingTails = receiptTails{stdout: "stdout\r\n", stderr: "stderr\r\n"}
		smallTails = receiptTails{stdout: "9\r\n", stderr: "p\r\n"}
		successStdoutBytes += 2
		smallStdoutBytes += 2
		smallStderrBytes += 2
	}
	for _, test := range []struct {
		name            string
		command         string
		tailBytes       *int64
		status          runstore.Status
		wantTails       receiptTails
		wantTailLen     int
		wantStdoutBytes int64
		wantStderrBytes int64
	}{
		{
			name:            "requested success",
			command:         shellOutputCommand(),
			tailBytes:       &requested,
			status:          runstore.StatusOK,
			wantTails:       successTails,
			wantStdoutBytes: successStdoutBytes,
		},
		{
			name:            "omitted success",
			command:         shellOutputCommand(),
			status:          runstore.StatusOK,
			wantTails:       receiptTails{},
			wantStdoutBytes: successStdoutBytes,
		},
		{
			name:            "omitted failing preserves compact tails",
			command:         failingCommand,
			status:          runstore.StatusNonzero,
			wantTails:       failingTails,
			wantStdoutBytes: int64(len(failingTails.stdout)),
			wantStderrBytes: int64(len(failingTails.stderr)),
		},
		{
			name:            "zero failing",
			command:         failingCommand,
			tailBytes:       &zero,
			status:          runstore.StatusNonzero,
			wantTails:       receiptTails{},
			wantStdoutBytes: int64(len(failingTails.stdout)),
			wantStderrBytes: int64(len(failingTails.stderr)),
		},
		{
			name:            "requested small tail",
			command:         smallTailCommand,
			tailBytes:       &small,
			status:          runstore.StatusOK,
			wantTails:       smallTails,
			wantTailLen:     int(small),
			wantStdoutBytes: smallStdoutBytes,
			wantStderrBytes: smallStderrBytes,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newShellTestServer(t, t.TempDir())
			_, receipt, err := server.runShellCommand(
				context.Background(),
				nil,
				runShellCommandInput{Command: test.command, TailBytes: test.tailBytes},
			)
			if err != nil || receipt.Status != test.status {
				t.Fatalf("runShellCommand = %#v, %v", receipt, err)
			}
			gotTails := receiptTails{stdout: receipt.StdoutTail, stderr: receipt.StderrTail}
			if gotTails != test.wantTails {
				t.Fatalf("receipt tails = %#v, want %#v", gotTails, test.wantTails)
			}
			if test.wantTailLen > 0 &&
				(len(receipt.StdoutTail) != test.wantTailLen || len(receipt.StderrTail) != test.wantTailLen) {
				t.Fatalf(
					"tail lengths = %d/%d, want %d",
					len(receipt.StdoutTail),
					len(receipt.StderrTail),
					test.wantTailLen,
				)
			}
			if receipt.StdoutBytes != test.wantStdoutBytes {
				t.Fatalf("stdout_bytes = %d, want %d", receipt.StdoutBytes, test.wantStdoutBytes)
			}
			if receipt.StderrBytes != test.wantStderrBytes {
				t.Fatalf("stderr_bytes = %d, want %d", receipt.StderrBytes, test.wantStderrBytes)
			}
		})
	}
}

type receiptTails struct {
	stdout string
	stderr string
}

func TestSyncShellTailBytesRejectBeforeRunStart(t *testing.T) {
	for _, tailBytes := range []int64{-1, 65537} {
		t.Run(strconv.FormatInt(tailBytes, 10), func(t *testing.T) {
			root := t.TempDir()
			server := newShellTestServer(t, root)
			marker := filepath.Join(root, "tail-bytes-marker")
			result, receipt, err := server.runShellCommand(
				context.Background(),
				nil,
				runShellCommandInput{
					Command:   shellMarkerCommand(marker),
					TailBytes: &tailBytes,
				},
			)
			if err != nil || result == nil || !result.IsError ||
				receipt.Error == nil || receipt.Error.Message != "tail_bytes must be between 0 and 65536" {
				t.Fatalf("invalid tail_bytes result = %#v, %#v, %v", result, receipt, err)
			}
			page, listErr := server.store.ListRecent(1)
			if listErr != nil || len(page.Runs) != 0 {
				t.Fatalf("runs after invalid tail_bytes = %#v, %v", page, listErr)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatalf("invalid tail_bytes started command: %v", statErr)
			}
		})
	}
}

func TestSyncShellTailBytesAcceptsUpperBound(t *testing.T) {
	tailBytes := int64(65536)
	server := newShellTestServer(t, t.TempDir())
	result, receipt, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: shellOutputCommand(), TailBytes: &tailBytes},
	)
	if err != nil || result != nil || receipt.Status != runstore.StatusOK {
		t.Fatalf("tail_bytes=65536 = %#v, %#v, %v, want a completed run", result, receipt, err)
	}
}

func TestSyncShellStdoutJSON(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		mode           string
		stdout         string
		wantJSON       string
		wantStdoutTail string
		tailBytes      *int64
		wantErrorParts []string
		wantJSONSet    bool
	}{
		{
			name:           "object with independent tail",
			mode:           "stdout",
			stdout:         `{"answer":42}`,
			wantJSON:       `{"answer":42}`,
			wantJSONSet:    true,
			wantStdoutTail: `r":42}`,
			tailBytes:      int64Pointer(6),
		},
		{
			name:        "array",
			mode:        "stdout",
			stdout:      `["one",2,false]`,
			wantJSON:    `["one",2,false]`,
			wantJSONSet: true,
		},
		{
			name:        "largest exact integer",
			mode:        "stdout",
			stdout:      `{"id":9007199254740992}`,
			wantJSON:    `{"id":9007199254740992}`,
			wantJSONSet: true,
		},
		{
			name:   "integer beyond the exact float64 range",
			mode:   "stdout",
			stdout: `{"id":9007199254740993}`,
			wantErrorParts: []string{
				"stdout JSON number at $.id",
				"float64 round trip",
				"get_run_logs",
			},
		},
		{
			name:   "integer beyond int64",
			mode:   "stdout",
			stdout: `{"id":99999999999999999999}`,
			wantErrorParts: []string{
				"stdout JSON number at $.id",
				"get_run_logs",
			},
		},
		{
			name:   "nested inexact integer names its path",
			mode:   "stdout",
			stdout: `{"rows":[{"ok":1},{"id":9007199254740993}]}`,
			wantErrorParts: []string{
				"stdout JSON number at $.rows[1].id",
			},
		},
		{
			name:        "null",
			mode:        "stdout",
			stdout:      "null",
			wantJSON:    "null",
			wantJSONSet: true,
		},
		{
			name:           "invalid",
			mode:           "stdout",
			stdout:         "not JSON",
			wantErrorParts: []string{"parse stdout as JSON:", "invalid character"},
		},
		{
			name:           "trailing value",
			mode:           "stdout",
			stdout:         `{"answer":42} false`,
			wantErrorParts: []string{"parse stdout as JSON:", "multiple JSON values"},
		},
		{
			name:           "invalid UTF-8",
			mode:           "invalid-utf8",
			wantErrorParts: []string{"parse stdout as JSON:", "not valid UTF-8"},
		},
		{
			name: "oversize",
			mode: "oversize",
			wantErrorParts: []string{
				strconv.FormatInt(stdoutJSONLimit+1, 10),
				strconv.FormatInt(stdoutJSONLimit, 10),
				"get_run_logs",
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := newShellTestServer(t, t.TempDir())
			block := defineArgvShellBlockForTest(t, server, shellArgvHelperBase(testCase.mode), "")
			arguments := []string(nil)
			if testCase.mode == "stdout" {
				arguments = append(shellArgvHelperBase(testCase.mode), testCase.stdout)
			}
			_, receipt, err := server.runShellCommand(
				context.Background(),
				nil,
				runShellCommandInput{
					BlockID:      block.BlockID,
					Arguments:    arguments,
					TailBytes:    testCase.tailBytes,
					StdoutFormat: "json",
				},
			)
			if err != nil || !receipt.OK {
				t.Fatalf("stdout JSON run = %#v, %v", receipt, err)
			}
			if receipt.StdoutTail != testCase.wantStdoutTail {
				t.Fatalf("stdout tail = %q, want %q", receipt.StdoutTail, testCase.wantStdoutTail)
			}
			encodedValue, marshalErr := json.Marshal(receipt.StdoutJSON)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if testCase.wantJSONSet {
				if receipt.StdoutJSON == nil || string(encodedValue) != testCase.wantJSON ||
					receipt.StdoutJSONError != "" {
					t.Fatalf("structured stdout = %s, %q", encodedValue, receipt.StdoutJSONError)
				}
			} else {
				if receipt.StdoutJSON != nil {
					t.Fatalf("structured stdout = %#v, want unset", receipt.StdoutJSON)
				}
				for _, part := range testCase.wantErrorParts {
					if !strings.Contains(receipt.StdoutJSONError, part) {
						t.Fatalf("stdout_json_error = %q, want %q", receipt.StdoutJSONError, part)
					}
				}
			}
		})
	}
}

func TestSyncShellStdoutJSONAcceptsSizeLimit(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	block := defineArgvShellBlockForTest(t, server, shellArgvHelperBase("json-limit"), "")
	_, receipt, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{BlockID: block.BlockID, StdoutFormat: "json"},
	)
	var value string
	var ok bool
	if receipt.StdoutJSON != nil {
		value, ok = (*receipt.StdoutJSON).(string)
	}
	if err != nil || !receipt.OK || receipt.StdoutJSONError != "" || !ok ||
		len(value) != int(stdoutJSONLimit-2) || receipt.StdoutBytes != stdoutJSONLimit {
		t.Fatalf("size-limit structured stdout = %T/%d, %#v, %v", receipt.StdoutJSON, len(value), receipt, err)
	}
}

func TestSyncShellStdoutJSONIsAbsentFromPromotedReceipt(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	immediate := int64(0)
	_, receipt, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{
			Command:      shellSleepCommand(),
			MaxWaitMS:    &immediate,
			StdoutFormat: "json",
		},
	)
	if err != nil || receipt.Status != runstore.StatusRunning || !receipt.Promoted {
		t.Fatalf("promoted JSON run = %#v, %v", receipt, err)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "stdout_json") {
		t.Fatalf("promoted receipt has structured stdout fields: %s", encoded)
	}
	stopRunOrFail(t, server, receipt.RunID)
}

func TestSyncShellStdoutJSONOnAlreadyCompletedImmediateReceipt(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	run, stats, output := server.startShellRun(
		context.Background(),
		runShellCommandInput{
			Arguments: append(shellArgvHelperBase("stdout"), `{"done":true}`),
		},
	)
	if run == nil || output.Error != nil {
		t.Fatalf("start terminal receipt fixture = %#v", output)
	}
	<-run.Done()
	receipt := server.shellCommandReceipt(
		server.waitForSyncReceipt(context.Background(), nil, run, stats, syncWait{}, nil),
		run.Meta(),
		"json",
	)
	encoded, err := json.Marshal(receipt.StdoutJSON)
	if err != nil || receipt.Status != runstore.StatusOK || receipt.Promoted ||
		receipt.StdoutJSON == nil || receipt.StdoutJSONError != "" ||
		string(encoded) != `{"done":true}` {
		t.Fatalf("already-completed immediate receipt = %s, %#v, %v", encoded, receipt, err)
	}
}

func TestSyncShellStdoutFormatRejectsBeforeRunStart(t *testing.T) {
	root := t.TempDir()
	server := newShellTestServer(t, root)
	marker := filepath.Join(root, "stdout-format-marker")
	result, receipt, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{
			Command:      shellBlockMarkerCommand(marker),
			StdoutFormat: "yaml",
		},
	)
	if err != nil || result == nil || !result.IsError || receipt.Error == nil ||
		receipt.Error.Message != `stdout_format must be omitted or "json"` {
		t.Fatalf("invalid stdout_format result = %#v, %#v, %v", result, receipt, err)
	}
	if receipt.runDetails == nil || receipt.WorktreeRoot != server.store.WorktreeRoot() {
		t.Fatalf("invalid stdout_format details = %#v", receipt.runDetails)
	}
	page, listErr := server.store.ListRecent(1)
	if listErr != nil || len(page.Runs) != 0 {
		t.Fatalf("runs after invalid stdout_format = %#v, %v", page, listErr)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("invalid stdout_format started command: %v", statErr)
	}
}

// TestSyncShellStdoutJSONFollowsTheTerminalStatus keeps a cancelled or timed-out
// run from returning a confident stdout_json parsed from whatever reached the
// log before it stopped, and keeps a nonzero exit - which still printed its
// whole output - parsed as before.
func TestSyncShellStdoutJSONFollowsTheTerminalStatus(t *testing.T) {
	for _, testCase := range []struct {
		status   runstore.Status
		wantJSON bool
	}{
		{status: runstore.StatusNonzero, wantJSON: true},
		{status: runstore.StatusCancelled},
		{status: runstore.StatusTimeout},
		{status: runstore.StatusSpawnError},
	} {
		t.Run(string(testCase.status), func(t *testing.T) {
			server := newShellTestServer(t, t.TempDir())
			handle, err := server.store.Begin(runstore.Meta{TaskID: "shell:command"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = handle.Stdout().Write([]byte(`{"complete":true}`)); err != nil {
				t.Fatal(err)
			}
			if err = handle.Finish(testCase.status, 0, "", false, false); err != nil {
				t.Fatal(err)
			}
			receipt := server.shellCommandReceipt(
				runTaskOutput{
					Result: executor.Result{RunID: handle.Meta.RunID, Status: testCase.status},
				},
				handle.Meta,
				"json",
			)
			if testCase.wantJSON {
				encoded, marshalErr := json.Marshal(receipt.StdoutJSON)
				if marshalErr != nil || receipt.StdoutJSON == nil ||
					string(encoded) != `{"complete":true}` || receipt.StdoutJSONError != "" {
					t.Fatalf("terminal status structured stdout = %s, %q", encoded, receipt.StdoutJSONError)
				}
				return
			}
			if receipt.StdoutJSON != nil {
				t.Fatalf("unfinished run structured stdout = %#v, want unset", receipt.StdoutJSON)
			}
			for _, part := range []string{string(testCase.status), "get_run_logs"} {
				if !strings.Contains(receipt.StdoutJSONError, part) {
					t.Fatalf("stdout_json_error = %q, want %q", receipt.StdoutJSONError, part)
				}
			}
		})
	}
}

func TestSyncShellStdoutJSONReportsStoreReadFailure(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	handle, err := server.store.Begin(runstore.Meta{TaskID: "shell:command"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Stdout().Write([]byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := handle.Finish(runstore.StatusOK, 0, "", false, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(server.store.LogRoot(), handle.Meta.RunID, "stdout.log")); err != nil {
		t.Fatal(err)
	}
	output := runShellCommandOutput{}
	server.attachStdoutJSON(&output, handle.Meta)
	if output.StdoutJSON != nil ||
		!strings.Contains(output.StdoutJSONError, "read stdout for JSON:") {
		t.Fatalf("store read failure = %#v", output)
	}
}

//nolint:gocyclo // Both receipt surfaces and false-value omission form one response contract.
func TestStreamTruncationFlagsReachSyncReceiptAndStatus(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	block := defineArgvShellBlockForTest(t, server, shellArgvHelperBase("truncate"), "")
	_, receipt, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{BlockID: block.BlockID},
	)
	if err != nil || !receipt.OK || !receipt.StdoutTruncated || !receipt.StderrTruncated {
		t.Fatalf("truncated synchronous receipt = %#v, %v", receipt, err)
	}
	_, status, err := server.getRunStatus(
		context.Background(),
		nil,
		getRunStatusInput{RunID: receipt.RunID},
	)
	if err != nil || !status.StdoutTruncated || !status.StderrTruncated {
		t.Fatalf("truncated status details = %#v, %v", status.runDetails, err)
	}
	for name, value := range map[string]any{"receipt": receipt, "status": status} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if !strings.Contains(string(encoded), `"stdout_truncated":true`) ||
			!strings.Contains(string(encoded), `"stderr_truncated":true`) {
			t.Fatalf("%s JSON omits truncation flags: %s", name, encoded)
		}
	}

	_, shortReceipt, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: shellOutputCommand()},
	)
	if err != nil || !shortReceipt.OK {
		t.Fatalf("short run = %#v, %v", shortReceipt, err)
	}
	_, shortStatus, err := server.getRunStatus(
		context.Background(),
		nil,
		getRunStatusInput{RunID: shortReceipt.RunID},
	)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"receipt": shortReceipt, "status": shortStatus} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if strings.Contains(string(encoded), "stdout_truncated") ||
			strings.Contains(string(encoded), "stderr_truncated") {
			t.Fatalf("%s JSON has false truncation flags: %s", name, encoded)
		}
	}
}

func int64Pointer(value int64) *int64 {
	return &value
}

func TestSyncWaitBounds(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	immediate := int64(0)
	_, started, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: shellSleepCommand(), MaxWaitMS: &immediate},
	)
	if err != nil || started.Status != runstore.StatusRunning || !started.Promoted {
		t.Fatalf("max_wait_ms=0 = %#v, %v, want an immediate running receipt", started, err)
	}
	stopRunOrFail(t, server, started.RunID)

	untilTimeout := int64(-1)
	_, waited, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: shellOutputCommand(), MaxWaitMS: &untilTimeout},
	)
	if err != nil || waited.Status != runstore.StatusOK {
		t.Fatalf("max_wait_ms=-1 = %#v, %v, want a completed run", waited, err)
	}

	// The caller's wait budget is independent from the task timeout.
	beyondTimeout := int64(time.Hour / time.Millisecond)
	wait, err := server.syncWaitDuration(&beyondTimeout)
	if err != nil || wait.duration != time.Hour || wait.untilCompletion {
		t.Fatalf("explicit wait = %#v, %v, want one hour", wait, err)
	}
	server.config.Timeout = 0
	server.config.TimeoutUnlimited = true
	server.config.SyncDeadline = 40 * time.Millisecond
	defaultWait, err := server.syncWaitDuration(nil)
	if err != nil || defaultWait.duration != server.config.SyncDeadline || defaultWait.untilCompletion {
		t.Fatalf("default wait = %#v, %v", defaultWait, err)
	}
	_, unlimited, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: shellBriefSleepCommand(), MaxWaitMS: &untilTimeout},
	)
	if err != nil || unlimited.Status != runstore.StatusOK {
		t.Fatalf("unlimited max_wait_ms=-1 = %#v, %v", unlimited, err)
	}
	invalid := int64(-2)
	if _, err := server.syncWaitDuration(&invalid); err == nil {
		t.Fatal("max_wait_ms=-2 must be rejected")
	}
}

func TestWaitRunDurationAcceptsLegacyTimeoutMS(t *testing.T) {
	legacy := int64(25)
	wait, err := waitRunDuration(waitRunInput{TimeoutMS: &legacy})
	if err != nil || wait != 25*time.Millisecond {
		t.Fatalf("legacy timeout_ms wait = %v, %v", wait, err)
	}
	current := int64(25)
	wait, err = waitRunDuration(waitRunInput{MaxWaitMS: &current, TimeoutMS: &legacy})
	if err != nil || wait != 25*time.Millisecond {
		t.Fatalf("matching wait aliases = %v, %v", wait, err)
	}
	legacy++
	if _, err = waitRunDuration(waitRunInput{MaxWaitMS: &current, TimeoutMS: &legacy}); err == nil {
		t.Fatal("conflicting wait aliases must be rejected")
	}
}

func TestStartedRunIgnoresRequestCancellation(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	_, started, err := server.startShellCommand(
		ctx,
		nil,
		startShellCommandInput{Command: shellSleepCommand()},
	)
	if err != nil || started.Status != runstore.StatusRunning {
		t.Fatalf("start = %#v, %v", started, err)
	}
	cancel()
	time.Sleep(50 * time.Millisecond)
	_, status, err := server.getRunStatus(
		context.Background(),
		nil,
		getRunStatusInput{RunID: started.RunID},
	)
	if err != nil || status.Status != runstore.StatusRunning {
		t.Fatalf("status after request cancellation = %#v, %v, want still running", status, err)
	}
	if status.ProcessAlive == nil || !*status.ProcessAlive {
		t.Fatalf("process must stay alive after request cancellation: %#v", status.runDetails)
	}
	stopRunOrFail(t, server, started.RunID)
}

func TestForeignRunIsObservableWithABoundedWait(t *testing.T) {
	server, runID := newForeignRun(t)
	_, status, err := server.getRunStatus(context.Background(), nil, getRunStatusInput{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	if status.OwnedByThis == nil || *status.OwnedByThis {
		t.Fatalf("foreign run must not be reported as owned: %#v", status.runDetails)
	}
	timeout := int64(100)
	begin := time.Now()
	_, waited, err := server.waitRun(
		context.Background(),
		nil,
		waitRunInput{RunID: runID, MaxWaitMS: &timeout},
	)
	if err != nil || waited.Completed == nil || *waited.Completed {
		t.Fatalf("wait on a foreign run = %#v, %v, want an unfinished snapshot", waited, err)
	}
	if elapsed := time.Since(begin); elapsed > 5*time.Second {
		t.Fatalf("wait took %v, want the requested bound", elapsed)
	}
}

func TestForeignRunCannotBeStopped(t *testing.T) {
	server, runID := newForeignRun(t)
	result, stopped, err := server.stopRun(context.Background(), nil, stopRunInput{RunID: runID})
	if err != nil || result == nil || !result.IsError || stopped.Error == nil {
		t.Fatalf("stopping a foreign run = %#v, %#v, %v, want a refusal", result, stopped, err)
	}
	if !strings.Contains(stopped.Error.Message, "cannot be stopped by this server") {
		t.Fatalf("refusal message = %q", stopped.Error.Message)
	}
}

func TestStatusDoesNotTreatLiveForeignOwnerAsThisServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// #nosec G204,G702 -- fixed test binary and helper selector.
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestMCPServerHelperProcess")
	cmd.Env = append(os.Environ(), "JMW_TEST_HELPER_PROCESS=sleep")
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		//nolint:errcheck // Cancellation is the expected helper exit path.
		_ = cmd.Wait()
	})
	identity := runstore.ProcessIdentity(cmd.Process.Pid)
	if identity == "" {
		t.Skip("process identity is unavailable on this platform")
	}
	if !runstore.ProcessMatches(cmd.Process.Pid, identity) {
		t.Fatal("foreign helper must be live for the ownership assertion")
	}
	server := newShellTestServer(t, t.TempDir())
	handle, err := server.store.Begin(runstore.Meta{TaskID: "shell:command"})
	if err != nil {
		t.Fatal(err)
	}
	finishRunningRunAtCleanup(t, handle)
	handle.Meta.OwnerPID = cmd.Process.Pid
	handle.Meta.OwnerIdentity = identity
	if persistErr := handle.PersistRunning(); persistErr != nil {
		t.Fatal(persistErr)
	}
	_, status, err := server.getRunStatus(
		context.Background(),
		nil,
		getRunStatusInput{RunID: handle.Meta.RunID},
	)
	if err != nil || status.OwnedByThis == nil || *status.OwnedByThis {
		t.Fatalf("live foreign owner = %#v, %v", status.runDetails, err)
	}
}

func TestStatusDoesNotTreatReusedOwnerPIDAsThisServer(t *testing.T) {
	identity := runstore.ProcessIdentity(os.Getpid())
	if identity == "" {
		t.Skip("process identity is unavailable on this platform")
	}
	server := newShellTestServer(t, t.TempDir())
	handle, err := server.store.Begin(runstore.Meta{TaskID: "shell:command"})
	if err != nil {
		t.Fatal(err)
	}
	finishRunningRunAtCleanup(t, handle)
	handle.Meta.OwnerPID = os.Getpid()
	handle.Meta.OwnerIdentity = identity + ":reused"
	if persistErr := handle.PersistRunning(); persistErr != nil {
		t.Fatal(persistErr)
	}
	_, status, err := server.getRunStatus(
		context.Background(),
		nil,
		getRunStatusInput{RunID: handle.Meta.RunID},
	)
	if err != nil || status.OwnedByThis == nil || *status.OwnedByThis {
		t.Fatalf("reused owner identity = %#v, %v", status.runDetails, err)
	}
}

func TestRunStatusUsesPersistedTaskTimeout(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	timeout := int64((10 * time.Second) / time.Millisecond)
	handle, err := server.store.Begin(
		runstore.Meta{TaskID: "shell:command", TaskTimeoutMS: &timeout},
	)
	if err != nil {
		t.Fatal(err)
	}
	finishRunningRunAtCleanup(t, handle)
	server.config.Timeout = 0
	server.config.TimeoutUnlimited = true
	_, status, err := server.getRunStatus(
		context.Background(),
		nil,
		getRunStatusInput{RunID: handle.Meta.RunID},
	)
	if err != nil ||
		status.TaskTimeoutMS == nil ||
		*status.TaskTimeoutMS != timeout ||
		status.TimeToTaskTimeoutMS == nil ||
		status.TimeoutMS != timeout ||
		status.TimeToTimeoutMS == nil {
		t.Fatalf("persisted task timeout = %#v, %v", status.runDetails, err)
	}
}

func TestShutdownBudgetCoversTerminationAndWaitDelay(t *testing.T) {
	const grace = 3 * time.Second
	if got, want := shutdownBudget(grace), 2*grace+time.Second; got != want {
		t.Fatalf("shutdown budget = %v, want %v", got, want)
	}
}

func TestStatusZeroTailSuppressesLiveOutput(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	_, started, err := server.startShellCommand(
		context.Background(),
		nil,
		startShellCommandInput{Command: shellLongOutputCommand()},
	)
	if err != nil || started.Status != runstore.StatusRunning {
		t.Fatalf("start shell command = %#v, %v", started, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, stateErr := server.store.LogState(started.RunID)
		if stateErr == nil && state.StdoutBytes > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	zero := int64(0)
	_, status, err := server.getRunStatus(
		context.Background(),
		nil,
		getRunStatusInput{RunID: started.RunID, TailBytes: &zero},
	)
	if err != nil || status.StdoutTail != "" || status.StderrTail != "" {
		t.Fatalf("zero-tail status = %#v, %v", status, err)
	}
	stopRunOrFail(t, server, started.RunID)
}

func TestAdmissionRejectsBeforeRejectedShellCommandStarts(t *testing.T) {
	root := t.TempDir()
	server := newShellTestServer(t, root)
	server.manager = runmanager.New(server.stats.Invalidate, 1)
	_, first, err := server.startShellCommand(
		context.Background(),
		nil,
		startShellCommandInput{Command: shellSleepCommand()},
	)
	if err != nil || first.Status != runstore.StatusRunning {
		t.Fatalf("first shell command = %#v, %v", first, err)
	}
	marker := filepath.Join(root, "rejected-marker")
	_, rejected, err := server.startShellCommand(
		context.Background(),
		nil,
		startShellCommandInput{Command: shellMarkerCommand(marker)},
	)
	if err != nil || rejected.Status != runstore.StatusSpawnError {
		t.Fatalf("rejected shell command = %#v, %v", rejected, err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("rejected command created marker: %v", statErr)
	}
	stopRunOrFail(t, server, first.RunID)
}

func TestStatusRepairsFailedFinalMetadata(t *testing.T) {
	server, runID, _ := failedTerminalRun(t)
	_, status, err := server.getRunStatus(
		context.Background(),
		nil,
		getRunStatusInput{RunID: runID},
	)
	if err != nil || status.Status != runstore.StatusCancelled ||
		status.Completed == nil || !*status.Completed {
		t.Fatalf("repaired terminal status = %#v, %v", status, err)
	}
	_, stored, err := server.getRun(context.Background(), nil, getRunInput{RunID: runID})
	if err != nil || stored.Run.Status != runstore.StatusCancelled {
		t.Fatalf("repaired terminal metadata = %#v, %v", stored, err)
	}
}

func TestListRunsRepairsFailedFinalMetadata(t *testing.T) {
	server, runID, _ := failedTerminalRun(t)
	_, listed, err := server.listRuns(
		context.Background(),
		nil,
		listRunsInput{Status: []string{string(runstore.StatusCancelled)}},
	)
	if err != nil || len(listed.Runs) != 1 ||
		listed.Runs[0].RunID != runID || listed.Runs[0].Status != runstore.StatusCancelled {
		t.Fatalf("listed repaired terminal run = %#v, %v", listed, err)
	}
}

func TestStatisticsRepairFailedFinalMetadata(t *testing.T) {
	server, runID, command := failedTerminalRun(t)
	immediate := int64(0)
	_, started, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: command, MaxWaitMS: &immediate},
	)
	if err != nil || started.Status != runstore.StatusRunning ||
		started.Stats == nil || started.Stats.Exact == nil ||
		started.Stats.Exact.Runs != 1 || started.Stats.Exact.AbortedRuns != 1 {
		t.Fatalf("statistics after terminal repair = %#v, %v", started, err)
	}
	stopRunOrFail(t, server, started.RunID)
	meta, err := server.store.Get(runID)
	if err != nil || meta.Status != runstore.StatusCancelled {
		t.Fatalf("stored repaired terminal run = %#v, %v", meta, err)
	}
}

func failedTerminalRun(t *testing.T) (*Server, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not block metadata writes on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	server := newShellTestServer(t, t.TempDir())
	command := shellSleepCommand()
	_, started, err := server.startShellCommand(
		context.Background(),
		nil,
		startShellCommandInput{Command: command},
	)
	if err != nil || started.Status != runstore.StatusRunning {
		t.Fatalf("start shell command = %#v, %v", started, err)
	}
	runDir := filepath.Join(server.store.LogRoot(), started.RunID)
	if chmodErr := os.Chmod(runDir, 0o500); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	run, live := server.manager.Get(started.RunID)
	if !live {
		t.Fatal("started run is not locally managed")
	}
	if stopErr := run.Stop(); stopErr == nil {
		t.Fatal("terminal metadata write must fail while the run directory is read-only")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, live = server.manager.Get(started.RunID); !live {
			if _, fallback := server.manager.Terminal(started.RunID); fallback {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if _, fallback := server.manager.Terminal(started.RunID); !fallback {
		t.Fatal("failed terminal run was not retained for metadata repair")
	}
	if chmodErr := os.Chmod(runDir, 0o750); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	t.Cleanup(func() {
		//nolint:errcheck // Restoring test-directory permissions is best effort.
		_ = os.Chmod(runDir, 0o750)
	})
	return server, started.RunID, command
}

func TestTimeoutStatusUsesPersistedDiagnostic(t *testing.T) {
	meta := runstore.Meta{
		RunID:  "019f7ac4-0000-7000-8000-000000000000",
		Status: runstore.StatusTimeout,
		Error:  "Task exceeded configured timeout of 15m0s; adjust --timeout or JMW_TIMEOUT.",
	}
	if result := resultForMeta(meta); result.Message != meta.Error {
		t.Fatalf("timeout status result = %#v", result)
	}
}

func shellBriefSleepCommand() string {
	if runtime.GOOS == "windows" {
		return "ping -n 2 127.0.0.1 >NUL"
	}
	return "sleep 0.05"
}

func shellMarkerCommand(marker string) string {
	if runtime.GOOS == "windows" {
		return "echo started > \"" + marker + "\" & " + shellSleepCommand()
	}
	return "printf started > " + strconv.Quote(marker) + "; " + shellSleepCommand()
}

// newForeignRun publishes a running ledger entry owned by another server process.
func newForeignRun(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	server := newShellTestServer(t, root)
	store, err := runstore.NewForWorktree(root, root)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(runstore.Meta{ProjectPath: ".", TaskID: "shell:command"})
	if err != nil {
		t.Fatal(err)
	}
	finishRunningRunAtCleanup(t, handle)
	handle.Meta.OwnerPID = os.Getpid() + 1
	handle.Meta.OwnerIdentity = "other-server"
	if persistErr := handle.PersistRunning(); persistErr != nil {
		t.Fatal(persistErr)
	}
	return server, handle.Meta.RunID
}

func finishRunningRunAtCleanup(t *testing.T, handle *runstore.Handle) {
	t.Helper()
	t.Cleanup(func() {
		if err := handle.Finish(runstore.StatusCancelled, -1, "test cleanup", false, false); err != nil {
			t.Errorf("finish running test run cleanup: %v", err)
		}
	})
}

func stopRunOrFail(t *testing.T, server *Server, runID string) {
	t.Helper()
	_, stopped, err := server.stopRun(context.Background(), nil, stopRunInput{RunID: runID})
	if err != nil || stopped.Status != runstore.StatusCancelled {
		t.Fatalf("stopRun = %#v, %v", stopped, err)
	}
}
