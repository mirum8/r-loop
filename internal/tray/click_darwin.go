//go:build darwin && cgo

package tray

import "C"

//export trayClicked
func trayClicked(id *C.char) {
	go clicked(C.GoString(id))
}
