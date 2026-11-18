//go:build !darwin || !cgo

package tray

import "os"

func Main(run func() int) {
	os.Exit(run())
}
