//go:build unix

package daemon

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
)

// Keep the shared Node status rules in one long-lived companion. The Go daemon
// supplies its cached record and rows; status never launches a process per read.
type statusProcess struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output io.ReadCloser
	encode *json.Encoder
	decode *json.Decoder
}

func (s *server) closeStatus() {
	if p := s.statusProcess; p != nil {
		p.input.Close()
		p.output.Close()
		p.cmd.Process.Kill()
		p.cmd.Wait()
		s.statusProcess = nil
	}
}

func (s *server) status() object {
	if s.statusProcess == nil {
		executable, err := os.Executable()
		if err != nil {
			return failure("status-unreadable")
		}
		// Release binaries live at <package>/native/bin/<platform>/.
		helper := filepath.Join(filepath.Dir(executable), "..", "..", "..", "src", "cli", "native-status.mjs")
		cmd := exec.Command("node", helper)
		cmd.Dir = s.workspace
		for _, env := range os.Environ() {
			if !strings.HasPrefix(env, "HOME=") {
				cmd.Env = append(cmd.Env, env)
			}
		}
		cmd.Env = append(cmd.Env, "HOME="+s.home)
		input, err := cmd.StdinPipe()
		if err != nil {
			return failure("status-unreadable")
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			input.Close()
			return failure("status-unreadable")
		}
		if err := cmd.Start(); err != nil {
			input.Close()
			output.Close()
			return failure("status-unreadable")
		}
		s.statusProcess = &statusProcess{cmd, input, output, json.NewEncoder(input), json.NewDecoder(output)}
	}
	p := s.statusProcess
	payload := object{"record": s.record, "rows": s.rows}
	response := make(chan object, 1)
	go func() {
		var result object
		if err := p.encode.Encode(payload); err != nil {
			response <- nil
			return
		}
		if err := p.decode.Decode(&result); err != nil {
			result = nil
		}
		response <- result
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case result := <-response:
		if result != nil {
			s.statusGateDetails(result)
			return result
		}
	case <-timer.C:
		// Killing and reaping the helper also unblocks the pipe goroutine.
	}
	s.closeStatus()
	return failure("status-unreadable")
}

// Match Node timeoutStatus using the ledger rows already cached by refresh.
// A failed gate.done retains a timeout only from its pending gate.red; another
// done without a red, a successful done, or a non-timeout red clears it.
func (s *server) statusGateDetails(result object) {
	pending, timedOut := make(map[string]bool), make(map[string]bool)
	flaky := make(map[string]bool)
	for _, value := range s.rows {
		row := mapping(value)
		if row["run"] != s.run {
			continue
		}
		if row["event"] == "gate.done" {
			files, _ := row["flaky"].([]any)
			for _, value := range files {
				if file, ok := value.(string); ok {
					flaky[file] = true
				}
			}
		}
		block, ok := row["block"].(string)
		if !ok {
			continue
		}
		switch row["event"] {
		case "gate.red":
			if row["reason"] == "timeout" || row["timed_out"] == true {
				pending[block], timedOut[block] = true, true
			} else {
				delete(pending, block)
				delete(timedOut, block)
			}
		case "gate.done":
			if row["all_ok"] == true || !pending[block] {
				delete(timedOut, block)
			}
			delete(pending, block)
		}
	}
	files := make([]string, 0, len(flaky))
	for file := range flaky {
		files = append(files, file)
	}
	// JavaScript's default sort compares UTF-16 units, including astral names.
	sort.Slice(files, func(i, j int) bool {
		return slices.Compare(utf16.Encode([]rune(files[i])), utf16.Encode([]rune(files[j]))) < 0
	})
	runs, _ := result["runs"].([]any)
	for _, value := range runs {
		run := mapping(value)
		if run["run_id"] != s.run {
			continue
		}
		delete(run, "flaky")
		if len(files) > 0 {
			run["flaky"] = files
		}
		blocks, _ := run["blocks"].([]any)
		for _, value := range blocks {
			block := mapping(value)
			gate := mapping(block["gate"])
			if gate["result"] == "fail" && timedOut[text(block, "id")] {
				gate["result"] = "timeout"
			}
		}
	}
}
