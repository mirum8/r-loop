package core

import (
	"errors"
	"fmt"
	"runtime/debug"
)

var errPanic = errors.New("panic")

func panicked(phase, step, where string, v any) (Event, error) {
	err := fmt.Errorf("%w in %s: %v", errPanic, where, v)
	return Event{Kind: "error", Phase: phase, Step: step, Fields: map[string]string{"reason": err.Error(), "stack": string(debug.Stack())}}, err
}

func quietly(fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w in recovery: %v", errPanic, r)
		}
	}()
	fn()
	return nil
}
