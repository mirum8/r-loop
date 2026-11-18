package app

import "r-loop/internal/core"

type faces []core.Face

func (fs faces) Emit(ev core.Event) {
	for _, f := range fs {
		f.Emit(ev)
	}
}

func (fs faces) Close() {
	for _, f := range fs {
		f.Close()
	}
}
