package lifecycle

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type recorder struct{ calls []string }

func (r *recorder) hook(name string, startErr error) Hook {
	return Hook{
		Name: name,
		OnStart: func(context.Context) error {
			r.calls = append(r.calls, "start:"+name)
			return startErr
		},
		OnStop: func(context.Context) error {
			r.calls = append(r.calls, "stop:"+name)
			return nil
		},
	}
}

func TestStartOrderAndReverseStop(t *testing.T) {
	var r recorder
	var m Manager
	m.Append(r.hook("a", nil))
	m.Append(r.hook("b", nil))
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"start:a", "start:b", "stop:b", "stop:a"}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls = %v, want %v", r.calls, want)
	}
}

func TestStartFailureRollsBackStartedHooks(t *testing.T) {
	var r recorder
	var m Manager
	m.Append(r.hook("a", nil))
	m.Append(r.hook("b", nil))
	m.Append(r.hook("c", errors.New("boom")))
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("want error")
	}
	want := []string{"start:a", "start:b", "start:c", "stop:b", "stop:a"}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls = %v, want %v", r.calls, want)
	}
}

func TestInsertRemoveIndex(t *testing.T) {
	var m Manager
	m.Append(Hook{Name: "a"})
	m.Append(Hook{Name: "c"})
	m.Insert(m.Index("c"), Hook{Name: "b"})
	m.Insert(-5, Hook{Name: "first"})
	m.Insert(99, Hook{Name: "last"})

	names := func() []string {
		var out []string
		for _, h := range m.Hooks() {
			out = append(out, h.Name)
		}
		return out
	}
	if got, want := names(), []string{"first", "a", "b", "c", "last"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after insert = %v, want %v", got, want)
	}
	if !m.Remove("b") || m.Remove("missing") {
		t.Fatal("Remove reported the wrong result")
	}
	if got, want := names(), []string{"first", "a", "c", "last"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after remove = %v, want %v", got, want)
	}
}

func TestHooksReturnsACopy(t *testing.T) {
	var m Manager
	m.Append(Hook{Name: "a"})
	hs := m.Hooks()
	hs[0].Name = "mutated"
	if m.Hooks()[0].Name != "a" {
		t.Fatal("Hooks exposed the manager's backing slice")
	}
}
