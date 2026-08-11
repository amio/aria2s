//go:build darwin

package publication

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	attributeBitmapCount = 5
	attributeVolumeInfo  = 0x80000000
	attributeVolumeUUID  = 0x00040000
)

type attributeList struct {
	BitmapCount uint16
	Reserved    uint16
	Common      uint32
	Volume      uint32
	Directory   uint32
	File        uint32
	Fork        uint32
}

// StableStorageID returns the native volume UUID exposed by getattrlist.
// Device nodes and stat.st_dev are intentionally excluded because macOS may
// reassign both when the same volume is mounted after a reboot.
func StableStorageID(path string) (string, bool, error) {
	pathPointer, err := unix.BytePtrFromString(path)
	if err != nil {
		return "", false, err
	}
	attributes := attributeList{BitmapCount: attributeBitmapCount, Volume: attributeVolumeInfo | attributeVolumeUUID}
	var buffer [20]byte // uint32 result length followed by uuid_t
	_, _, errno := unix.Syscall6(
		unix.SYS_GETATTRLIST,
		uintptr(unsafe.Pointer(pathPointer)),
		uintptr(unsafe.Pointer(&attributes)),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
		0,
		0,
	)
	if errno != 0 {
		return "", false, errno
	}
	if binary.NativeEndian.Uint32(buffer[:4]) < uint32(len(buffer)) {
		return "", false, fmt.Errorf("volume UUID attribute response is truncated")
	}
	uuid := buffer[4:]
	allZero := true
	for _, value := range uuid {
		allZero = allZero && value == 0
	}
	if allZero {
		return "", false, nil
	}
	return fmt.Sprintf("darwin-volume:%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(uuid[0:4]),
		binary.BigEndian.Uint16(uuid[4:6]),
		binary.BigEndian.Uint16(uuid[6:8]),
		binary.BigEndian.Uint16(uuid[8:10]),
		uuid[10:16],
	), true, nil
}
