package verification

import (
	"bufio"
	"context"
	"dev-sandbox/internal/codexpolicy"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
)

var featureKeys = []string{"apps", "plugins", "browser_use", "in_app_browser", "computer_use"}

type reply struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}
type received struct {
	reply reply
	err   error
}
type client struct {
	input   io.Writer
	replies <-chan received
}

// One reader retains buffered replies across requests. Cancellation unblocks it
// even when the caller stops consuming replies; the owner closes the pipe.
func newClient(ctx context.Context, input io.Writer, output io.Reader) *client {
	replies := make(chan received)
	go func() {
		defer close(replies)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 16384), 8*1024*1024)
		for scanner.Scan() {
			var r reply
			err := json.Unmarshal(scanner.Bytes(), &r)
			select {
			case replies <- received{r, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
		err := scanner.Err()
		if err == nil {
			err = errors.New("Codex app-server exited before replying")
		}
		select {
		case replies <- received{err: err}:
		case <-ctx.Done():
		}
	}()
	return &client{input, replies}
}
func (c *client) send(message any) error { return json.NewEncoder(c.input).Encode(message) }
func (c *client) request(ctx context.Context, method string, params any, id int, result any) error {
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", method, ctx.Err())
		case msg, ok := <-c.replies:
			if !ok {
				return errors.New("Codex app-server exited before replying")
			}
			if msg.err != nil {
				return msg.err
			}
			if msg.reply.ID != id {
				continue
			}
			if len(msg.reply.Error) != 0 {
				return fmt.Errorf("%s: %s", method, msg.reply.Error)
			}
			return json.Unmarshal(msg.reply.Result, result)
		}
	}
}

func checkSelectedPolicy(out io.Writer, expected codexpolicy.Requirements) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	overrides := []string{}
	for key, value := range expected.Features {
		if !value {
			overrides = append(overrides, "-c", "features."+key+"=true")
		}
	}
	args := append([]string{"codex", "app-server", "--stdio", "--strict-config"}, overrides...)
	cmd := command(ctx, args...)
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer output.Close()
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { stop(cmd); _ = cmd.Wait() }()
	c := newClient(ctx, input, output)
	request := func(method string, params any, id int, result any) error {
		deadline, done := context.WithTimeout(ctx, 20*time.Second)
		defer done()
		return c.request(deadline, method, params, id, result)
	}
	var init any
	if err := request("initialize", map[string]any{"clientInfo": map[string]string{"name": "dev_sandbox_verify", "version": "1.0"}, "capabilities": map[string]bool{"experimentalApi": true}}, 1, &init); err != nil {
		return err
	}
	if err := c.send(map[string]string{"method": "initialized"}); err != nil {
		return err
	}
	var req struct {
		Requirements struct {
			Profiles map[string]bool  `json:"allowedPermissionProfiles"`
			Default  string           `json:"defaultPermissions"`
			Features map[string]*bool `json:"featureRequirements"`
		} `json:"requirements"`
	}
	if err := request("configRequirements/read", map[string]any{}, 2, &req); err != nil {
		return err
	}
	var config struct {
		Config struct {
			Default string `json:"default_permissions"`
		} `json:"config"`
	}
	if err := request("config/read", map[string]bool{"includeLayers": false}, 3, &config); err != nil {
		return err
	}
	if !reflect.DeepEqual(req.Requirements.Profiles, expected.Profiles) {
		return errors.New("Unexpected managed profiles")
	}
	if req.Requirements.Default != expected.Default {
		return errors.New("Unexpected managed default")
	}
	if expected.Default != "" && config.Config.Default != expected.Default {
		return errors.New("Unexpected configured default")
	}
	for key, want := range expected.Features {
		if value := req.Requirements.Features[key]; value == nil || *value != want {
			return errors.New("Missing managed feature restrictions")
		}
	}
	stop(cmd)
	featureCtx, done := context.WithTimeout(ctx, 20*time.Second)
	defer done()
	stdout, stderr, err := run(featureCtx, append(append([]string{"codex"}, overrides...), "features", "list")...)
	if err != nil {
		return fmt.Errorf("features list: %w: %s", err, stderr)
	}
	features := map[string]string{}
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			features[fields[0]] = fields[len(fields)-1]
		}
	}
	for key, want := range expected.Features {
		if features[key] != fmt.Sprint(want) {
			return errors.New("Managed feature pin was not enforced")
		}
	}
	fmt.Fprintln(out, "PASS Codex app-server reads selected managed defaults/profiles/features; resolved feature requirements are enforced")
	return nil
}
