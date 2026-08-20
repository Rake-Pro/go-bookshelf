package api

import "runtime"

// runtimeVersion is reported by /system/status so an operator can see which
// toolchain produced the running binary.
var runtimeVersion = runtime.Version()
