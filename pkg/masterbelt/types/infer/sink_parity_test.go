package infer

import (
	"reflect"
	"testing"
)

// streamFields are the Sink callbacks that are informational streams, not
// findings: observe forwards them but must NOT flip *fired through them (a walk
// that only emitted a stream reported no finding). Every other func field of
// Sink is a finding and must both forward and flip *fired. Keeping this set
// explicit means a new field defaults to "is a finding" — the safe direction:
// if it is really a stream, this set is updated deliberately.
var streamFields = map[string]bool{
	"Checked":        true,
	"SolvedFuncLit":  true,
	"ResolvedMethod": true,
	"ResolvedStatic": true,
	"ResolvedFunc":   true,
	"CallSubst":      true,
}

// TestObserveForwardsEverySinkField is the structural guard against the defect
// class this fix closes: observe() used to rebuild Sink by hand-listing a stale
// subset of callbacks, so a field added to Sink was silently dropped on the
// lambda-argument path (TernaryCondNotBool, TernaryBranchMismatch,
// ScalarConversion, and others were lost). This test reflects over every func
// field of Sink, wires each to a recorder, wraps the sink with observe, and
// proves the wrapper forwards that field's callback through to the source — and
// flips *fired for findings, leaving it untouched for the informational streams.
// A new Sink field that observe forgets to carry fails here.
func TestObserveForwardsEverySinkField(t *testing.T) {
	sinkType := reflect.TypeOf(Sink{})

	for i := 0; i < sinkType.NumField(); i++ {
		field := sinkType.Field(i)
		if field.Type.Kind() != reflect.Func {
			t.Fatalf("Sink.%s is not a func field; the parity guard assumes every callback is a func", field.Name)
		}

		t.Run(field.Name, func(t *testing.T) {
			// A source sink whose only set callback is this field, wired to a
			// recorder that records the call.
			called := false
			src := &Sink{}
			recorder := reflect.MakeFunc(field.Type, func(args []reflect.Value) []reflect.Value {
				called = true
				return nil
			})
			reflect.ValueOf(src).Elem().Field(i).Set(recorder)

			fired := false
			wrapped := observe(src, &fired)

			// Forwarding parity: the wrapper must carry a callback for this field.
			wrappedField := reflect.ValueOf(wrapped).Elem().Field(i)
			if wrappedField.IsNil() {
				t.Fatalf("observe dropped Sink.%s: the wrapper's field is nil though the source set it", field.Name)
			}

			// Invoke the wrapper's callback with zero-value arguments; it must
			// reach the source recorder.
			args := make([]reflect.Value, field.Type.NumIn())
			for j := range args {
				args[j] = reflect.Zero(field.Type.In(j))
			}
			wrappedField.Call(args)

			if !called {
				t.Fatalf("observe's Sink.%s wrapper did not delegate to the source callback", field.Name)
			}

			// *fired parity: a finding flips it, a stream leaves it untouched.
			if streamFields[field.Name] {
				if fired {
					t.Fatalf("observe's Sink.%s wrapper flipped *fired, but %s is an informational stream, not a finding", field.Name, field.Name)
				}
			} else if !fired {
				t.Fatalf("observe's Sink.%s wrapper did not flip *fired, but %s is a finding", field.Name, field.Name)
			}
		})
	}
}

// TestObserveNilSink checks observe stays valid for a nil sink — the silent
// typing walk shares the call rule by wrapping a nil sink. Every wrapper
// callback must be invokable without panicking (it delegates through a guarded
// method that no-ops for a nil sink), and a finding must still flip *fired so the
// call rule learns a lambda argument failed even when no diagnostics are rendered.
func TestObserveNilSink(t *testing.T) {
	sinkType := reflect.TypeOf(Sink{})
	wrapped := observe(nil, new(bool))
	if wrapped == nil {
		t.Fatal("observe(nil, ...) returned nil")
	}
	wv := reflect.ValueOf(wrapped).Elem()
	for i := 0; i < sinkType.NumField(); i++ {
		field := sinkType.Field(i)
		f := wv.Field(i)
		if f.IsNil() {
			t.Fatalf("observe(nil, ...) dropped Sink.%s", field.Name)
		}
		// Invoke through the wrapper with zero-value args; a nil source must not
		// panic. A fresh *fired per call confirms findings flip it, streams do not.
		fired := false
		wrapped := observe(nil, &fired)
		args := make([]reflect.Value, field.Type.NumIn())
		for j := range args {
			args[j] = reflect.Zero(field.Type.In(j))
		}
		reflect.ValueOf(wrapped).Elem().Field(i).Call(args)
		if streamFields[field.Name] {
			if fired {
				t.Errorf("observe(nil)'s Sink.%s flipped *fired, but it is a stream", field.Name)
			}
		} else if !fired {
			t.Errorf("observe(nil)'s Sink.%s did not flip *fired, but it is a finding", field.Name)
		}
	}
}
