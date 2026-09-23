//go:build darwin && cgo

package audio

/*
#cgo LDFLAGS: -framework CoreAudio -framework CoreFoundation
#include <CoreAudio/CoreAudio.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>

// Selectors from AudioHardware.h (macOS 14.2+), spelled out so the file
// also builds against older SDKs; on older systems the calls just fail.
#define VURA_PROCESS_OBJECT_LIST 'prs#'
#define VURA_PROCESS_PID         'ppid'
#define VURA_PROCESS_BUNDLE_ID   'pbid'
#define VURA_PROCESS_RUNNING_IN  'piri'

static AudioObjectPropertyAddress vura_addr(AudioObjectPropertySelector sel) {
	AudioObjectPropertyAddress a = { sel, kAudioObjectPropertyScopeGlobal, 0 };
	return a;
}

// vura_capturing writes the pid and bundle id of every process with audio
// input running. Returns the count, or -1 when the process list is
// unavailable (macOS before 14.2).
static int vura_capturing(int *pids, char *bundles, int bundleLen, int max) {
	AudioObjectPropertyAddress a = vura_addr(VURA_PROCESS_OBJECT_LIST);
	UInt32 size = 0;
	if (AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &a, 0, NULL, &size) != noErr) return -1;
	if (size == 0) return 0;
	AudioObjectID *ids = malloc(size);
	if (ids == NULL) return -1;
	if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &a, 0, NULL, &size, ids) != noErr) {
		free(ids);
		return -1;
	}
	int n = size / sizeof(AudioObjectID), out = 0;
	for (int i = 0; i < n && out < max; i++) {
		UInt32 running = 0, sz = sizeof(running);
		AudioObjectPropertyAddress r = vura_addr(VURA_PROCESS_RUNNING_IN);
		if (AudioObjectGetPropertyData(ids[i], &r, 0, NULL, &sz, &running) != noErr || !running) continue;
		pid_t pid = 0;
		sz = sizeof(pid);
		AudioObjectPropertyAddress p = vura_addr(VURA_PROCESS_PID);
		if (AudioObjectGetPropertyData(ids[i], &p, 0, NULL, &sz, &pid) != noErr) continue;
		pids[out] = pid;
		char *b = bundles + out * bundleLen;
		b[0] = 0;
		CFStringRef s = NULL;
		sz = sizeof(s);
		AudioObjectPropertyAddress bp = vura_addr(VURA_PROCESS_BUNDLE_ID);
		if (AudioObjectGetPropertyData(ids[i], &bp, 0, NULL, &sz, &s) == noErr && s != NULL) {
			CFStringGetCString(s, b, bundleLen, kCFStringEncodingUTF8);
			CFRelease(s);
		}
		out++;
	}
	free(ids);
	return out;
}

// vura_input_running: 1 if the default input device is in use by anyone,
// 0 if not, -1 if there is no input device.
static int vura_input_running(void) {
	AudioObjectPropertyAddress a = vura_addr(kAudioHardwarePropertyDefaultInputDevice);
	AudioDeviceID dev = 0;
	UInt32 sz = sizeof(dev);
	if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &a, 0, NULL, &sz, &dev) != noErr || dev == 0) return -1;
	UInt32 running = 0;
	sz = sizeof(running);
	AudioObjectPropertyAddress r = vura_addr(kAudioDevicePropertyDeviceIsRunningSomewhere);
	if (AudioObjectGetPropertyData(dev, &r, 0, NULL, &sz, &running) != noErr) return -1;
	return running ? 1 : 0;
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unsafe"
)

func (p *Poller) refreshSources(context.Context) {}

// list asks CoreAudio which processes have input running. macOS does not
// say which device each one records from; Source is "default input".
func list(ctx context.Context) ([]Stream, error) {
	const max, blen = 64, 256
	pids := make([]C.int, max)
	bundles := (*C.char)(C.malloc(max * blen))
	defer C.free(unsafe.Pointer(bundles))
	n := int(C.vura_capturing(&pids[0], bundles, blen, max))
	if n < 0 {
		// Before macOS 14.2: only "someone is using the mic".
		switch C.vura_input_running() {
		case 1:
			return []Stream{{Index: "input", App: "microphone", Source: "default input"}}, nil
		case 0:
			return nil, nil
		}
		return nil, errors.New("coreaudio: no input device")
	}
	var streams []Stream
	for i := 0; i < n; i++ {
		pid := int(pids[i])
		bundle := C.GoString((*C.char)(unsafe.Add(unsafe.Pointer(bundles), i*blen)))
		if ignoredCapture(bundle) {
			continue
		}
		comm, _ := exec.CommandContext(ctx, "ps", "-p", fmt.Sprint(pid), "-o", "comm=").Output()
		streams = append(streams, Stream{
			Index: fmt.Sprintf("pid:%d", pid), App: appName(strings.TrimSpace(string(comm)), bundle),
			Binary: bundle, Source: "default input",
		})
	}
	return streams, nil
}
