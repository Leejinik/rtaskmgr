package monitor

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"rtaskmgr/internal/host"
)

// OneShot is a short-lived SSH connection for a single lookup, outside the
// monitoring sessions: "서버에서 불러오기" has to work for a cluster that is not
// connected yet (or not even saved). It dials exactly like a monitoring session —
// the same jump-host routes, tried in the same order — and elevates the same way
// (sudo -S with the login password), but keeps nothing running afterwards.
type OneShot struct {
	client   *ssh.Client
	ups      []*ssh.Client
	password string
	once     sync.Once

	Via  string // hops the route went through ("" = direct)
	Root bool   // logged in as root
	Sudo bool   // sudo -S with the login password works
}

// OpenOneShot connects to h. resolve looks up jump hosts by id; nil uses the
// manager's resolver (the saved host list). ctx bounds the whole opening —
// dialing (abandoned and closed if it finishes late) and the root/sudo probes.
func (m *Manager) OpenOneShot(ctx context.Context, h host.Host, resolve HostResolver) (*OneShot, error) {
	if resolve == nil {
		resolve = m.resolveHost
	}
	paths, err := jumpPaths(h, resolve)
	if err != nil {
		return nil, err
	}
	type dialed struct {
		client *ssh.Client
		ups    []*ssh.Client
		via    string
		err    error
	}
	ch := make(chan dialed, 1)
	go func() {
		c, ups, via, err := dialWithFallback(paths)
		ch <- dialed{c, ups, via, err}
	}()
	var d dialed
	select {
	case d = <-ch:
	case <-ctx.Done():
		go func() { // close a connection that arrives after the caller gave up
			if late := <-ch; late.err == nil {
				(&OneShot{client: late.client, ups: late.ups}).Close()
			}
		}()
		return nil, fmt.Errorf("시간 초과: %w", ctx.Err())
	}
	if d.err != nil {
		return nil, d.err
	}
	o := &OneShot{client: d.client, ups: d.ups, password: h.Password, Via: d.via}
	if out, err := o.run(ctx, "id -u", false); err == nil {
		o.Root = strings.TrimSpace(out) == "0"
	}
	if !o.Root && h.Password != "" {
		// Same probe as sudoWorks, but bounded by ctx.
		out, _ := o.run(ctx, "id -u", true)
		for _, line := range strings.Split(out, "\n") {
			o.Sudo = o.Sudo || strings.TrimSpace(line) == "0"
		}
	}
	if ctx.Err() != nil {
		o.Close()
		return nil, fmt.Errorf("시간 초과: %w", ctx.Err())
	}
	return o, nil
}

// Elevated reports whether Run executes as root.
func (o *OneShot) Elevated() bool { return o.Root || o.Sudo }

// Run executes script (elevated when possible) and returns its stdout. Closing
// the connection is what unblocks a hung command, so a cancelled ctx closes it.
func (o *OneShot) Run(ctx context.Context, script string) (string, error) {
	return o.run(ctx, script, !o.Root && o.Sudo)
}

func (o *OneShot) run(ctx context.Context, script string, sudo bool) (string, error) {
	sess, err := o.client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	stop := context.AfterFunc(ctx, o.Close)
	defer stop()
	cmd := "bash -c " + shellQuote(script)
	if sudo {
		sess.Stdin = strings.NewReader(o.password + "\n")
		cmd = "sudo -S -p '' bash -c " + shellQuote(script)
	}
	out, err := sess.Output(cmd)
	if ctx.Err() != nil {
		return string(out), fmt.Errorf("시간 초과: %w", ctx.Err())
	}
	return string(out), err
}

// Close drops the connection and every tunnel it rode through.
func (o *OneShot) Close() {
	o.once.Do(func() {
		o.client.Close()
		for _, c := range o.ups {
			c.Close()
		}
	})
}
