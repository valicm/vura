package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/valicm/vura/internal/store"
)

// Op is one edit, keyed by what it touched rather than by row number, so a
// log of edits survives the day being rebuilt from fresh evidence. An op
// whose target no longer exists is skipped on replay.
type Op struct {
	Kind   string   `json:"kind"`             // logged | desc | assign | drop | merge | note
	Target string   `json:"target,omitempty"` // entry key
	Others []string `json:"others,omitempty"` // merge: keys folded into Target
	Dur    string   `json:"dur,omitempty"`
	Text   string   `json:"text,omitempty"`
	Bucket string   `json:"bucket,omitempty"`
	At     string   `json:"at,omitempty"` // note: RFC3339 start
}

// Key identifies an entry across rebuilds: kind, start, end, original bucket.
func (e Entry) Key() string {
	return fmt.Sprintf("%s|%s|%s|%s", e.Kind, e.Start.Format(time.RFC3339), e.End.Format(time.RFC3339), e.origBucket)
}

func (d *Day) byKey(key string) (int, bool) {
	for _, e := range d.Entries {
		if e.Dropped {
			continue
		}
		if e.Key() == key {
			return e.N, true
		}
	}
	return 0, false
}

// Apply performs one op and, on success, records it.
func (d *Day) Apply(op Op) error {
	if err := d.apply(op); err != nil {
		return err
	}
	d.Ops = append(d.Ops, op)
	return nil
}

func (d *Day) apply(op Op) error {
	n, ok := d.byKey(op.Target)
	switch op.Kind {
	case "note":
		dur, err := time.ParseDuration(op.Dur)
		if err != nil {
			return err
		}
		at, err := time.Parse(time.RFC3339, op.At)
		if err != nil {
			return err
		}
		return d.AddNote(op.Bucket, dur, op.Text, at.In(d.cfg.Location))
	case "merge":
		if !ok {
			return fmt.Errorf("merge target gone")
		}
		ns := []int{n}
		for _, k := range op.Others {
			m, ok := d.byKey(k)
			if !ok {
				return fmt.Errorf("merge source gone")
			}
			ns = append(ns, m)
		}
		return d.Merge(ns...)
	}
	if !ok {
		return fmt.Errorf("entry gone")
	}
	switch op.Kind {
	case "logged":
		dur, err := time.ParseDuration(op.Dur)
		if err != nil {
			return err
		}
		return d.SetLogged(n, dur)
	case "desc":
		return d.SetDesc(n, op.Text)
	case "assign":
		return d.Assign(n, strings.ToUpper(op.Bucket))
	case "drop":
		return d.Drop(n)
	}
	return fmt.Errorf("unknown op %q", op.Kind)
}

// Replay applies stored ops in order, skipping ones whose targets are gone,
// and returns how many were skipped.
func (d *Day) Replay(ops []Op) (skipped int) {
	for _, op := range ops {
		if err := d.apply(op); err != nil {
			skipped++
			continue
		}
		d.Ops = append(d.Ops, op)
	}
	return skipped
}

func opsKey(day string) string { return "reconcile.ops." + day }

// LoadOps reads the persisted edit log for a day.
func LoadOps(ctx context.Context, st *store.Store, day string) ([]Op, error) {
	v, err := st.GetState(ctx, opsKey(day))
	if err != nil || v == "" {
		return nil, err
	}
	var ops []Op
	if err := json.Unmarshal([]byte(v), &ops); err != nil {
		return nil, err
	}
	return ops, nil
}

// SaveOps persists the day's edit log; an empty log clears it.
func SaveOps(ctx context.Context, st *store.Store, day string, ops []Op) error {
	if len(ops) == 0 {
		return st.SetState(ctx, opsKey(day), "")
	}
	b, err := json.Marshal(ops)
	if err != nil {
		return err
	}
	return st.SetState(ctx, opsKey(day), string(b))
}
