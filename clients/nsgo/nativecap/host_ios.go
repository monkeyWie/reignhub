//go:build ios

package nativecap

/*
#cgo LDFLAGS: -framework UIKit -framework Foundation
#include "host_ios.h"
*/
import "C"

import (
	"unsafe"

	"github.com/nsgo-dev/nsgo"
	"github.com/nsgo-dev/nsgo/core"
)

func platformOptions() []nsgo.Option {
	host := core.NewHost()
	host.Handle("ras.timezone", func(call *core.HostCall) {
		p := C.ras_timezone_name()
		defer C.free(unsafe.Pointer(p))
		call.Resolve(map[string]any{"name": C.GoString(p), "offsetSeconds": int(C.ras_timezone_offset())})
	})
	host.Handle("ras.device", func(call *core.HostCall) {
		p := C.ras_device_name()
		defer C.free(unsafe.Pointer(p))
		model := C.ras_device_model()
		defer C.free(unsafe.Pointer(model))
		call.Resolve(map[string]any{"name": C.GoString(p), "model": C.GoString(model), "platform": "ios"})
	})
	return []nsgo.Option{nsgo.HostChannel(host)}
}

// FocusComposer gives the mounted conversation editor keyboard focus for hardware typing.
func FocusComposer(entryID string) {
	id := C.CString(entryID)
	defer C.free(unsafe.Pointer(id))
	C.ras_focus_composer(id)
}
