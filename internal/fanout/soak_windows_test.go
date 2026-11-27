package fanout

import (
	"syscall"
	"unsafe"
)

var (
	soakKernel       = syscall.NewLazyDLL("kernel32.dll")
	soakHandles      = soakKernel.NewProc("GetProcessHandleCount")
	soakProcessTimes = soakKernel.NewProc("GetProcessTimes")
	soakMemory       = syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")
)

type soakMemoryCounters struct {
	Size, PageFaults                                                               uint32
	PeakRSS, RSS, PeakPaged, Paged, PeakNonPaged, NonPaged, Pagefile, PeakPagefile uintptr
}

func readSoakOS() (soakOS, error) {
	self := ^uintptr(0)
	var times [4]syscall.Filetime
	if ok, _, err := soakProcessTimes.Call(self, uintptr(unsafe.Pointer(&times[0])), uintptr(unsafe.Pointer(&times[1])), uintptr(unsafe.Pointer(&times[2])), uintptr(unsafe.Pointer(&times[3]))); ok == 0 {
		return soakOS{}, err
	}
	var rawCount uint32
	if ok, _, err := soakHandles.Call(self, uintptr(unsafe.Pointer(&rawCount))); ok == 0 {
		return soakOS{}, err
	}
	memory := soakMemoryCounters{}
	memory.Size = uint32(unsafe.Sizeof(memory))
	if ok, _, err := soakMemory.Call(self, uintptr(unsafe.Pointer(&memory)), uintptr(memory.Size)); ok == 0 {
		return soakOS{}, err
	}
	count := int(rawCount)
	ticks := func(t syscall.Filetime) uint64 { return uint64(t.HighDateTime)<<32 | uint64(t.LowDateTime) }
	return soakOS{CPUSeconds: float64(ticks(times[2])+ticks(times[3])) / 1e7, RSSBytes: uint64(memory.RSS), Handles: &count}, nil
}
