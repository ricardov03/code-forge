package daemon

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
		helper := filepath.Join(filepath.Dir(executable), "..", "..", "..", "src", "state", "native-status.mjs")
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
			return result
		}
	case <-timer.C:
		// Killing and reaping the helper also unblocks the pipe goroutine.
	}
	s.closeStatus()
	return failure("status-unreadable")
}
