// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWatchInterruptsFirstSignalCancelsSecondExits(t *testing.T) {
	signals := make(chan os.Signal, 2)
	var stderr bytes.Buffer
	exited := make(chan int, 1)
	ctx, release := watchInterrupts(signals, &stderr, func(code int) { exited <- code })
	signals <- os.Interrupt
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("first interrupt did not cancel the context")
	}
	select {
	case code := <-exited:
		t.Fatalf("first interrupt must not exit (code %d)", code)
	case <-time.After(20 * time.Millisecond):
	}
	signals <- os.Interrupt
	select {
	case code := <-exited:
		if code == 0 {
			t.Fatal("forced exit must be non-zero")
		}
	case <-time.After(time.Second):
		t.Fatal("second interrupt did not exit")
	}
	release()
	if !strings.Contains(stderr.String(), "waiting for the current uninterruptible Gonum solve") {
		t.Fatalf("missing cancellation notice: %q", stderr.String())
	}
}

func TestWatchInterruptsReleaseWithoutSignal(t *testing.T) {
	signals := make(chan os.Signal, 2)
	ctx, release := watchInterrupts(signals, io.Discard, func(int) { t.Fatal("unexpected exit") })
	if ctx.Err() != nil {
		t.Fatal("context cancelled without a signal")
	}
	release()
	if ctx.Err() == nil {
		t.Fatal("release must cancel the context")
	}
}

// Subprocess check with real SIGINTs: the helper blocks in work that ignores
// cancellation (like a Gonum solve); the second Ctrl+C must end the process.
func TestWatchInterruptsRealSignalsSubprocess(t *testing.T) {
	if os.Getenv("OPT_INTERRUPT_HELPER") == "1" {
		signals := make(chan os.Signal, 2)
		signal.Notify(signals, os.Interrupt)
		ctx, release := watchInterrupts(signals, os.Stderr, os.Exit)
		defer release()
		_, _ = os.Stdout.WriteString("ready\n")
		<-ctx.Done()
		_, _ = os.Stdout.WriteString("cancelled\n")
		time.Sleep(time.Minute) // uninterruptible work
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestWatchInterruptsRealSignalsSubprocess$")
	command.Env = append(os.Environ(), "OPT_INTERRUPT_HELPER=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor := func(word string) {
		buffer := make([]byte, 64)
		deadline := time.Now().Add(10 * time.Second)
		var seen string
		for !strings.Contains(seen, word) {
			if time.Now().After(deadline) {
				t.Fatalf("helper never printed %q (got %q)", word, seen)
			}
			n, _ := stdout.Read(buffer)
			seen += string(buffer[:n])
		}
	}
	waitFor("ready")
	_ = command.Process.Signal(syscall.SIGINT)
	waitFor("cancelled")
	_ = command.Process.Signal(syscall.SIGINT)
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != interruptExitCode {
			t.Fatalf("exit = %v, want code %d", err, interruptExitCode)
		}
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("second SIGINT did not end the process")
	}
}
