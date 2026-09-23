package presence

import (
	"reflect"
	"testing"
)

func TestParseIOReg(t *testing.T) {
	out := `+-o Root  <class IORegistryEntry, id 0x100000100, retain 32>
  +-o IOHIDSystem  <class IOHIDSystem, id 0x1000004c1, registered, matched, active, busy 0 (0 ms), retain 26>
    | {
    |   "HIDIdleTime" = 4521000000
    |   "HIDParameters" = {"HIDClickTime"=500000000}
    | }
`
	ms, err := parseIOReg(out)
	if err != nil || ms != 4521 {
		t.Errorf("got %d, %v", ms, err)
	}
	if _, err := parseIOReg("+-o Root\n"); err == nil {
		t.Error("missing HIDIdleTime must fail")
	}
}

func TestParsePmset(t *testing.T) {
	out := `2026-09-23 21:20:00 +0200
Assertion status system-wide:
   BackgroundTask                 0
   UserIsActive                   1
   PreventUserIdleDisplaySleep    1
   PreventUserIdleSystemSleep     1
Listed by owning process:
   pid 97(powerd): [0x0000000100098001] 01:02:03 PreventUserIdleSystemSleep named: "Powerd - Prevent sleep while display is on"
   pid 412(caffeinate): [0x0000a1b20001a2c3] 00:10:00 PreventUserIdleDisplaySleep named: "caffeinate command-line tool"
   pid 233(coreaudiod): [0x0000a1b20001a2c4] 00:00:30 PreventUserIdleSystemSleep named: "com.apple.audio.AppleUSBAudioEngine.context.preventuseridlesleep"
   pid 612(WindowServer): [0x0000a1b20001a2c5] 00:00:01 UserIsActive named: "com.apple.iohideventsystem.queue.tickle"
Kernel Assertions: 0x4=USB
   id=500  level=255 0x4=USB mod=01/01/2026, 09:00 description=com.apple.usb.externaldevice.14100000 owner=AppleUSBXHCI
Idle sleep preventers: IODisplayWrapper
`
	flags, apps := parsePmset(out)
	if flags != FlagIdle|FlagSuspend || !reflect.DeepEqual(apps, []string{"caffeinate", "coreaudiod"}) {
		t.Errorf("flags %d apps %v", flags, apps)
	}
	if f, a := parsePmset("Listed by owning process:\n   pid 97(powerd): [0x1] 00:00:01 PreventUserIdleSystemSleep named: \"x\"\n"); f != 0 || a != nil {
		t.Errorf("system daemons only: %d %v", f, a)
	}
}
