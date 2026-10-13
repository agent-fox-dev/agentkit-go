package podman

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// reapEntry is an expired container or volume and its sandbox id.
type reapEntry struct{ name, id string }

// Reap removes the containers and volumes, labelled agentkit.sandbox, whose
// agentkit.sandbox.expires label parses and is not after now, containers
// first, and returns the sorted, distinct sandbox ids it removed something
// for. An entry that has not expired, has no or an unparsable expiry, or
// whose labels cannot be read is left alone: after its TTL a sandbox is dead
// whoever owns it, so the expiry rule never touches a live one, on this host
// or another. Failed removals do not stop the others; their errors are
// joined and returned with the ids that were removed.
func (p *provider) Reap(ctx context.Context) ([]string, error) {
	containers, err := p.list(ctx, "ps", "-a", "--filter", "label="+labelID, "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	volumes, err := p.list(ctx, "volume", "ls", "--filter", "label="+labelID, "--format", "{{.Name}}")
	if err != nil {
		return nil, err
	}
	now := p.now()
	var expiredC, expiredV []reapEntry
	for _, n := range containers {
		if id, ok := p.expired(ctx, now, "inspect", "--format", "{{json .Config.Labels}}", n); ok {
			expiredC = append(expiredC, reapEntry{n, id})
		}
	}
	for _, n := range volumes {
		if id, ok := p.expired(ctx, now, "volume", "inspect", "--format", "{{json .Labels}}", n); ok {
			expiredV = append(expiredV, reapEntry{n, id})
		}
	}
	var ids []string
	var errs []error
	for _, e := range expiredC {
		if err := p.remove(ctx, "rm", "-f", e.name); err != nil {
			errs = append(errs, err)
		} else {
			ids = append(ids, e.id)
		}
	}
	for _, e := range expiredV {
		if err := p.remove(ctx, "volume", "rm", e.name); err != nil {
			errs = append(errs, err)
		} else {
			ids = append(ids, e.id)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), errors.Join(errs...)
}

// list runs a listing and returns its non-empty lines.
func (p *provider) list(ctx context.Context, args ...string) ([]string, error) {
	res, err := p.r.run(ctx, append([]string{p.bin}, args...), shortTimeout)
	if err := p.cmdErr(args, res, err); err != nil {
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(res.Output, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names, nil
}

// expired reads an entry's labels with the inspect command args and
// reports its sandbox id when its expiry parses and is not after now.
func (p *provider) expired(ctx context.Context, now time.Time, args ...string) (string, bool) {
	res, err := p.r.run(ctx, append([]string{p.bin}, args...), shortTimeout)
	if p.cmdErr(args, res, err) != nil {
		return "", false
	}
	var labels map[string]string
	if json.Unmarshal([]byte(strings.TrimSpace(res.Output)), &labels) != nil {
		return "", false
	}
	exp, err := time.Parse(time.RFC3339, labels[labelExpires])
	if err != nil || exp.After(now) {
		return "", false
	}
	return labels[labelID], true
}
