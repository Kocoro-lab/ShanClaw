//go:build darwin && !ios && cgo

package koe

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AudioToolbox -framework AudioUnit -framework CoreAudio -framework Foundation
#include <AudioToolbox/AudioToolbox.h>
#include <AudioUnit/AudioUnit.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    SInt16 *buf;
    int cap;
    int r;
    int w;
    int count;
    int initialized;
    unsigned long long *overwrites;
    pthread_mutex_t mu;
} vpioRing;

static void ringInit(vpioRing *ring, int cap, unsigned long long *overwrites) {
    ring->buf = (SInt16 *)calloc(cap, sizeof(SInt16));
    ring->cap = cap;
    ring->r = 0;
    ring->w = 0;
    ring->count = 0;
    ring->initialized = 1;
    ring->overwrites = overwrites;
    pthread_mutex_init(&ring->mu, NULL);
}

static void ringFree(vpioRing *ring) {
    if (!ring->initialized) return;
    if (ring->buf) {
        free(ring->buf);
        ring->buf = NULL;
    }
    ring->cap = 0;
    ring->r = 0;
    ring->w = 0;
    ring->count = 0;
    ring->initialized = 0;
    ring->overwrites = NULL;
    pthread_mutex_destroy(&ring->mu);
}

static void ringWrite(vpioRing *ring, const SInt16 *src, int n) {
    if (!ring->initialized || !ring->buf || ring->cap <= 0) return;
    pthread_mutex_lock(&ring->mu);
    for (int i = 0; i < n; i++) {
        ring->buf[ring->w] = src[i];
        ring->w = (ring->w + 1) % ring->cap;
        if (ring->count < ring->cap) {
            ring->count++;
        } else {
            ring->r = (ring->r + 1) % ring->cap;
            if (ring->overwrites) (*ring->overwrites)++;
        }
    }
    pthread_mutex_unlock(&ring->mu);
}

static int ringRead(vpioRing *ring, SInt16 *dst, int n) {
    if (!ring->initialized || !ring->buf || ring->cap <= 0) {
        for (int i = 0; i < n; i++) dst[i] = 0;
        return 0;
    }
    pthread_mutex_lock(&ring->mu);
    int got = 0;
    while (got < n && ring->count > 0) {
        dst[got++] = ring->buf[ring->r];
        ring->r = (ring->r + 1) % ring->cap;
        ring->count--;
    }
    pthread_mutex_unlock(&ring->mu);
    for (int i = got; i < n; i++) dst[i] = 0;
    return got;
}

static int ringCount(vpioRing *ring) {
    if (!ring->initialized) return 0;
    pthread_mutex_lock(&ring->mu);
    int c = ring->count;
    pthread_mutex_unlock(&ring->mu);
    return c;
}

static void ringClear(vpioRing *ring) {
    if (!ring->initialized) return;
    pthread_mutex_lock(&ring->mu);
    ring->r = 0;
    ring->w = 0;
    ring->count = 0;
    pthread_mutex_unlock(&ring->mu);
}

static AudioUnit gVAU = 0;
static vpioRing gMicRing;
static vpioRing gPlayRing;
static Float32 *gInputFloatScratch = NULL;
static SInt16 *gInputIntScratch = NULL;
static SInt16 *gOutputScratch = NULL;
static int gInputScratchCap = 0;
static int gPlayPrimed = 0;
static int gPlayPrerollSamples = 0;
static unsigned long long gInputCallbacks = 0;
static unsigned long long gOutputCallbacks = 0;
static unsigned long long gInputFrames = 0;
static unsigned long long gOutputFrames = 0;
static unsigned long long gPlayUnderruns = 0;
static unsigned long long gPlayOverwrites = 0;
static int gPlayPaused = 0;

static void vpioSetPlaybackPaused(int paused) {
    if (!gPlayRing.initialized) {
        gPlayPaused = paused ? 1 : 0;
        return;
    }
    pthread_mutex_lock(&gPlayRing.mu);
    gPlayPaused = paused ? 1 : 0;
    pthread_mutex_unlock(&gPlayRing.mu);
}

static int vpioPlaybackPaused(void) {
    if (!gPlayRing.initialized) return gPlayPaused;
    pthread_mutex_lock(&gPlayRing.mu);
    int paused = gPlayPaused;
    pthread_mutex_unlock(&gPlayRing.mu);
    return paused;
}

static int vpioProbeEnabled(void) {
    const char *v = getenv("KOE_VPIO_PROBE");
    return v && v[0] && strcmp(v, "0") != 0;
}

static void vpioProbe(const char *step) {
    if (!vpioProbeEnabled()) return;
    fprintf(stderr, "koe[vpio]: %s\n", step);
    fflush(stderr);
}

static void zeroABL(AudioBufferList *ioData) {
    if (!ioData) return;
    for (UInt32 i = 0; i < ioData->mNumberBuffers; i++) {
        if (ioData->mBuffers[i].mData && ioData->mBuffers[i].mDataByteSize > 0) {
            memset(ioData->mBuffers[i].mData, 0, ioData->mBuffers[i].mDataByteSize);
        }
    }
}

static void vpioFreeScratchC(void) {
    if (gInputFloatScratch) {
        free(gInputFloatScratch);
        gInputFloatScratch = NULL;
    }
    if (gInputIntScratch) {
        free(gInputIntScratch);
        gInputIntScratch = NULL;
    }
    if (gOutputScratch) {
        free(gOutputScratch);
        gOutputScratch = NULL;
    }
    gInputScratchCap = 0;
}

static OSStatus vpioInputCB(void *inRefCon, AudioUnitRenderActionFlags *flags,
                            const AudioTimeStamp *ts, UInt32 bus, UInt32 nFrames,
                            AudioBufferList *ioData) {
    if (!gVAU || !gInputFloatScratch || !gInputIntScratch || (int)nFrames > gInputScratchCap) return noErr;
    AudioBufferList abl;
    abl.mNumberBuffers = 1;
    abl.mBuffers[0].mNumberChannels = 1;
    abl.mBuffers[0].mDataByteSize = nFrames * sizeof(Float32);
    abl.mBuffers[0].mData = gInputFloatScratch;
    OSStatus st = AudioUnitRender(gVAU, flags, ts, 1, nFrames, &abl);
    if (st != noErr) return st;
    for (UInt32 i = 0; i < nFrames; i++) {
        Float32 x = gInputFloatScratch[i];
        if (x > 1.0f) x = 1.0f;
        if (x < -1.0f) x = -1.0f;
        gInputIntScratch[i] = (SInt16)(x * 32767.0f);
    }
    gInputCallbacks++;
    gInputFrames += nFrames;
    ringWrite(&gMicRing, gInputIntScratch, (int)nFrames);
    return noErr;
}

static OSStatus vpioOutputCB(void *inRefCon, AudioUnitRenderActionFlags *flags,
                             const AudioTimeStamp *ts, UInt32 bus, UInt32 nFrames,
                             AudioBufferList *ioData) {
    gOutputCallbacks++;
    gOutputFrames += nFrames;
    if (!ioData) return noErr;
    if (vpioPlaybackPaused()) {
        zeroABL(ioData);
        if (flags) *flags |= kAudioUnitRenderAction_OutputIsSilence;
        return noErr;
    }
    if (!gPlayPrimed) {
        if (ringCount(&gPlayRing) < gPlayPrerollSamples) {
            zeroABL(ioData);
            if (flags) *flags |= kAudioUnitRenderAction_OutputIsSilence;
            return noErr;
        }
        gPlayPrimed = 1;
    }
    if (!gOutputScratch || (int)nFrames > gInputScratchCap) {
        zeroABL(ioData);
        if (flags) *flags |= kAudioUnitRenderAction_OutputIsSilence;
        return noErr;
    }
    int got = ringRead(&gPlayRing, gOutputScratch, (int)nFrames);
    if (got < (int)nFrames) {
        gPlayPrimed = 0;
        gPlayUnderruns++;
        if (got == 0 && flags) *flags |= kAudioUnitRenderAction_OutputIsSilence;
    } else if (flags) {
        *flags &= ~kAudioUnitRenderAction_OutputIsSilence;
    }
    for (UInt32 i = 0; i < ioData->mNumberBuffers; i++) {
        Float32 *f = (Float32 *)ioData->mBuffers[i].mData;
        int n = ioData->mBuffers[i].mDataByteSize / sizeof(Float32);
        if (!f) continue;
        for (int j = 0; j < n; j++) {
            SInt16 sample = 0;
            if (j < (int)nFrames) {
                sample = gOutputScratch[j];
            }
            f[j] = ((Float32)sample) / 32768.0f;
        }
    }
    return noErr;
}

static void vpioStopUnitC(void) {
    if (gVAU) {
        AudioOutputUnitStop(gVAU);
        AudioUnitUninitialize(gVAU);
        AudioComponentInstanceDispose(gVAU);
        gVAU = 0;
    }
}

static void vpioFreeRingsC(void) {
    ringFree(&gMicRing);
    ringFree(&gPlayRing);
    vpioFreeScratchC();
}

static void vpioCleanupC(void) {
    vpioStopUnitC();
    vpioFreeRingsC();
}

// vpioDeviceForUID resolves a CoreAudio device UID to its AudioDeviceID.
// Returns kAudioObjectUnknown for empty/NULL/unresolvable UIDs (caller keeps
// the system default in that case).
static AudioDeviceID vpioDeviceForUID(const char *uid) {
    if (!uid || uid[0] == '\0') return kAudioObjectUnknown;
    CFStringRef cfuid = CFStringCreateWithCString(NULL, uid, kCFStringEncodingUTF8);
    if (!cfuid) return kAudioObjectUnknown;
    AudioDeviceID dev = kAudioObjectUnknown;
    UInt32 size = sizeof(dev);
    AudioObjectPropertyAddress addr = {
        kAudioHardwarePropertyTranslateUIDToDevice,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain
    };
    OSStatus st = AudioObjectGetPropertyData(kAudioObjectSystemObject, &addr,
        sizeof(cfuid), &cfuid, &size, &dev);
    CFRelease(cfuid);
    if (st != noErr) return kAudioObjectUnknown;
    return dev;
}

static OSStatus vpioStartC(double sampleRate, int ringCap, int prerollSamples,
                           const char *micUID, const char *spkUID,
                           int bypassVoiceProcessing) {
    vpioProbe("start enter");
    ringInit(&gMicRing, ringCap, NULL);
    ringInit(&gPlayRing, ringCap, &gPlayOverwrites);
    gInputFloatScratch = (Float32 *)calloc(ringCap, sizeof(Float32));
    gInputIntScratch = (SInt16 *)calloc(ringCap, sizeof(SInt16));
    gOutputScratch = (SInt16 *)calloc(ringCap, sizeof(SInt16));
    gInputScratchCap = ringCap;
    gPlayPrimed = 0;
    gPlayPrerollSamples = prerollSamples;
    gInputCallbacks = 0;
    gOutputCallbacks = 0;
    gInputFrames = 0;
    gOutputFrames = 0;
    gPlayUnderruns = 0;
    gPlayOverwrites = 0;
    gPlayPaused = 0;
    if (!gInputFloatScratch || !gInputIntScratch || !gOutputScratch) {
        vpioCleanupC();
        return -2;
    }

    AudioComponentDescription desc = {0};
    desc.componentType = kAudioUnitType_Output;
    desc.componentSubType = kAudioUnitSubType_VoiceProcessingIO;
    desc.componentManufacturer = kAudioUnitManufacturer_Apple;
    AudioComponent comp = AudioComponentFindNext(NULL, &desc);
    if (!comp) {
        vpioCleanupC();
        return -1;
    }
    vpioProbe("component found");
    vpioProbe("AudioComponentInstanceNew begin");
    OSStatus st = AudioComponentInstanceNew(comp, &gVAU);
    if (st != noErr) {
        vpioCleanupC();
        return st;
    }
    vpioProbe("AudioComponentInstanceNew done");

    UInt32 one = 1;
    UInt32 bypass = bypassVoiceProcessing ? 1 : 0;
    UInt32 agc = bypassVoiceProcessing ? 0 : 1;
    vpioProbe("EnableIO input begin");
    st = AudioUnitSetProperty(gVAU, kAudioOutputUnitProperty_EnableIO, kAudioUnitScope_Input, 1, &one, sizeof(one));
    if (st != noErr) { vpioCleanupC(); return st; }
    vpioProbe("EnableIO input done");
    vpioProbe("EnableIO output begin");
    st = AudioUnitSetProperty(gVAU, kAudioOutputUnitProperty_EnableIO, kAudioUnitScope_Output, 0, &one, sizeof(one));
    if (st != noErr) { vpioCleanupC(); return st; }
    vpioProbe("EnableIO output done");
    // Optional device binding (voice-settings wave §W4): input on element 1,
    // output on element 0 — the Chromium VPIO pattern. Failure is non-fatal:
    // warn to stderr (→ koe.log) and keep the system default.
    AudioDeviceID inDev = vpioDeviceForUID(micUID);
    if (inDev != kAudioObjectUnknown) {
        st = AudioUnitSetProperty(gVAU, kAudioOutputUnitProperty_CurrentDevice,
            kAudioUnitScope_Global, 1, &inDev, sizeof(inDev));
        if (st != noErr) fprintf(stderr, "koe[vpio]: bind input device failed OSStatus %d - using default\n", (int)st);
        else fprintf(stderr, "koe[vpio]: bound input device %s\n", micUID);
    } else if (micUID && micUID[0]) {
        fprintf(stderr, "koe[vpio]: input device UID not found - using default\n");
    }
    AudioDeviceID outDev = vpioDeviceForUID(spkUID);
    if (outDev != kAudioObjectUnknown) {
        st = AudioUnitSetProperty(gVAU, kAudioOutputUnitProperty_CurrentDevice,
            kAudioUnitScope_Global, 0, &outDev, sizeof(outDev));
        if (st != noErr) fprintf(stderr, "koe[vpio]: bind output device failed OSStatus %d - using default\n", (int)st);
        else fprintf(stderr, "koe[vpio]: bound output device %s\n", spkUID);
    } else if (spkUID && spkUID[0]) {
        fprintf(stderr, "koe[vpio]: output device UID not found - using default\n");
    }
    vpioProbe("BypassVoiceProcessing begin");
    st = AudioUnitSetProperty(gVAU, kAUVoiceIOProperty_BypassVoiceProcessing, kAudioUnitScope_Global, 0, &bypass, sizeof(bypass));
    if (st != noErr) { vpioCleanupC(); return st; }
    vpioProbe("BypassVoiceProcessing done");
    vpioProbe("VoiceProcessingEnableAGC begin");
    st = AudioUnitSetProperty(gVAU, kAUVoiceIOProperty_VoiceProcessingEnableAGC, kAudioUnitScope_Global, 0, &agc, sizeof(agc));
    if (st != noErr) { vpioCleanupC(); return st; }
    vpioProbe("VoiceProcessingEnableAGC done");
    fprintf(stderr, "koe[vpio]: voice processing bypass=%u agc=%u\n", bypass, agc);

    AudioStreamBasicDescription fmt = {0};
    fmt.mSampleRate = sampleRate;
    fmt.mFormatID = kAudioFormatLinearPCM;
    fmt.mFormatFlags = kAudioFormatFlagIsFloat | kAudioFormatFlagIsPacked;
    fmt.mFramesPerPacket = 1;
    fmt.mChannelsPerFrame = 1;
    fmt.mBitsPerChannel = 32;
    fmt.mBytesPerFrame = 4;
    fmt.mBytesPerPacket = 4;
    vpioProbe("StreamFormat input-side begin");
    st = AudioUnitSetProperty(gVAU, kAudioUnitProperty_StreamFormat, kAudioUnitScope_Output, 1, &fmt, sizeof(fmt));
    if (st != noErr) { vpioCleanupC(); return st; }
    vpioProbe("StreamFormat input-side done");
    vpioProbe("StreamFormat output-side begin");
    st = AudioUnitSetProperty(gVAU, kAudioUnitProperty_StreamFormat, kAudioUnitScope_Input, 0, &fmt, sizeof(fmt));
    if (st != noErr) { vpioCleanupC(); return st; }
    vpioProbe("StreamFormat output-side done");

    AURenderCallbackStruct inputCB = {0};
    inputCB.inputProc = vpioInputCB;
    vpioProbe("SetInputCallback begin");
    st = AudioUnitSetProperty(gVAU, kAudioOutputUnitProperty_SetInputCallback, kAudioUnitScope_Global, 1, &inputCB, sizeof(inputCB));
    if (st != noErr) { vpioCleanupC(); return st; }
    vpioProbe("SetInputCallback done");

    AURenderCallbackStruct outputCB = {0};
    outputCB.inputProc = vpioOutputCB;
    vpioProbe("SetRenderCallback begin");
    st = AudioUnitSetProperty(gVAU, kAudioUnitProperty_SetRenderCallback, kAudioUnitScope_Input, 0, &outputCB, sizeof(outputCB));
    if (st != noErr) { vpioCleanupC(); return st; }
    vpioProbe("SetRenderCallback done");

    vpioProbe("AudioUnitInitialize begin");
    st = AudioUnitInitialize(gVAU);
    if (st != noErr) { vpioCleanupC(); return st; }
    vpioProbe("AudioUnitInitialize done");
    vpioProbe("AudioOutputUnitStart begin");
    st = AudioOutputUnitStart(gVAU);
    if (st != noErr) { vpioCleanupC(); return st; }
    vpioProbe("AudioOutputUnitStart done");
    return noErr;
}

static int vpioReadMic(SInt16 *dst, int n) { return ringRead(&gMicRing, dst, n); }
static void vpioWritePlay(SInt16 *src, int n) { ringWrite(&gPlayRing, src, n); }
static void vpioClearBuffers(void) {
    ringClear(&gMicRing);
    ringClear(&gPlayRing);
    gPlayPrimed = 0;
}
static int vpioMicCount(void) { return ringCount(&gMicRing); }
static int vpioPlayCount(void) { return ringCount(&gPlayRing); }
static int vpioPlayCapacity(void) { return gPlayRing.cap; }
static unsigned long long vpioInputCallbackCount(void) { return gInputCallbacks; }
static unsigned long long vpioOutputCallbackCount(void) { return gOutputCallbacks; }
static unsigned long long vpioInputFrameCount(void) { return gInputFrames; }
static unsigned long long vpioOutputFrameCount(void) { return gOutputFrames; }
static unsigned long long vpioPlayUnderrunCount(void) { return gPlayUnderruns; }
static unsigned long long vpioPlayOverwriteCount(void) { return gPlayOverwrites; }
*/
import "C"

import (
	"fmt"
	"math"
	"sync"
	"time"
	"unsafe"
)

const vpioPlaybackHighWaterSamples = audioSampleRate / 2 // keep hardware ring latency around 500 ms

// vpioLifecycleMu serializes StartVPIO / stopVPIO across DIFFERENT AudioIO
// instances, and vpioOwner names the AudioIO that currently owns the process-global
// VPIO unit + rings (gVAU / gMicRing / gPlayRing). cmd/koe.go tears down the OLD
// session's audio (a.Stop → stopVPIO) AFTER releasing sessMu, while a NEW session's
// StartVPIO can run under the lock — so without this the old stopVPIO could dispose
// the new session's live unit/rings (silence or a use-after-free crash). Holding the
// mutex across both operations makes them strictly ordered: Stop-then-Start (old
// fully torn down first) OR Start-then-(stale)Stop-noop (old sees it is no longer the
// owner and skips ALL C disposal). Guarded by vpioLifecycleMu.
var (
	vpioLifecycleMu sync.Mutex
	vpioOwner       *AudioIO
)

type vpioDebugStats struct {
	InputCallbacks  uint64
	OutputCallbacks uint64
	InputFrames     uint64
	OutputFrames    uint64
	PlayUnderruns   uint64
	PlayOverwrites  uint64
	PlayQueueDrops  uint64
	PlayQueueMax    int64
	PlayBuffered    int
	PlayCapacity    int
	ForwardedFrames uint64
	GateDropped     uint64
	BargePassed     uint64
	MaxInputLevel   float64
	MaxOutputLevel  float64
}

// StartVPIO opens Apple's VoiceProcessingIO backend. It is opt-in and macOS-only:
// VPIO provides native echo cancellation, while SetSpeaking still keeps the mic
// quiet during playback by default. Experimental barge-in must opt in.
func (a *AudioIO) StartVPIO() error {
	const ringCap = audioSampleRate * 2 // ~2s of mono S16 per direction
	micUID := C.CString(a.preferredMicUID)
	spkUID := C.CString(a.preferredSpeakerUID)
	defer C.free(unsafe.Pointer(micUID))
	defer C.free(unsafe.Pointer(spkUID))
	// Hold vpioLifecycleMu across init + ownership claim so a concurrent stopVPIO on a
	// different instance can never interleave on the process-global unit/rings (see
	// vpioLifecycleMu / stopVPIO). Ownership is claimed only after vpioStartC succeeds,
	// so a failed start leaves any prior owner intact.
	vpioLifecycleMu.Lock()
	defer vpioLifecycleMu.Unlock()
	bypass := C.int(0)
	if a.vpioBypassVoiceProcessing.Load() {
		bypass = 1
	}
	if st := C.vpioStartC(C.double(audioSampleRate), C.int(ringCap), C.int(prerollFrames*audioFrameSize), micUID, spkUID, bypass); st != 0 {
		return fmt.Errorf("vpio start: OSStatus %d", int(st))
	}
	vpioOwner = a
	a.vpioActive.Store(true)
	a.vpioDone = make(chan struct{})
	a.vpioWG.Add(2)
	go a.vpioCaptureLoop()
	go a.vpioPlaybackLoop()
	return nil
}

func (a *AudioIO) clearVPIOBuffers() {
	if a.vpioActive.Load() {
		C.vpioClearBuffers()
	}
}

func (a *AudioIO) setBackendPlaybackPaused(paused bool) {
	if !a.vpioActive.Load() {
		return
	}
	value := C.int(0)
	if paused {
		value = 1
	}
	C.vpioSetPlaybackPaused(value)
}

// claimVPIOTeardown reports, under vpioLifecycleMu, whether this AudioIO still owns
// the process-global VPIO unit/rings and must therefore run the C teardown, clearing
// the owner when it does. A newer StartVPIO on a different instance transfers
// ownership, so a late stopVPIO from the old instance returns false — the pure-Go
// ownership gate that keeps the old teardown from disposing the new session's live
// unit/rings. Caller MUST hold vpioLifecycleMu.
func (a *AudioIO) claimVPIOTeardown() bool {
	if vpioOwner != a {
		return false
	}
	vpioOwner = nil
	return true
}

func (a *AudioIO) stopVPIO() {
	vpioLifecycleMu.Lock()
	defer vpioLifecycleMu.Unlock()
	owner := a.claimVPIOTeardown()
	if owner {
		C.vpioStopUnitC()
	}
	// Always stop THIS instance's goroutines, owner or not: they select on vpioDone
	// (bounded — the capture loop's inner drain empties the ring in ~1 tick and
	// breaks), so closing it makes them exit and Wait blocks until they have. In the
	// stale (non-owner) case the rings are the NEW owner's LIVE buffers, not freed
	// memory, so the departing goroutines touching them is at worst a stray frame, not
	// a UAF — and we skip both C.vpioStopUnitC and C.vpioFreeRingsC so the new session
	// is untouched.
	if a.vpioDone != nil {
		close(a.vpioDone)
	}
	a.vpioWG.Wait()
	if owner {
		C.vpioFreeRingsC()
	}
	a.vpioActive.Store(false)
	a.vpioDone = nil
}

func (a *AudioIO) vpioCaptureLoop() {
	defer a.vpioWG.Done()
	ticker := time.NewTicker(audioFrameMs * time.Millisecond / 2)
	defer ticker.Stop()
	for {
		select {
		case <-a.vpioDone:
			return
		case <-ticker.C:
		}
		for int(C.vpioMicCount()) >= audioFrameSize {
			frame := make([]int16, audioFrameSize)
			n := int(C.vpioReadMic((*C.SInt16)(unsafe.Pointer(&frame[0])), C.int(audioFrameSize)))
			if n == 0 {
				break
			}
			level := rmsLevel(frame)
			a.setInputLevel(level)
			a.trackVPIOMaxInput(level)
			// Suppressed frames become silent keepalives so the send track's RTP
			// timeline stays continuous while Kocoro speaks (see resolveCaptureFrame).
			frame = a.resolveCaptureFrame(frame, a.shouldForwardVPIOCapture(level))
			if frame == nil {
				continue
			}
			select {
			case a.frames <- frame:
			default:
			}
		}
	}
}

func (a *AudioIO) vpioPlaybackLoop() {
	defer a.vpioWG.Done()
	for {
		select {
		case <-a.vpioDone:
			return
		case pcm := <-a.playBuf:
			if len(pcm) == 0 {
				continue
			}
			if !a.waitForVPIOPlaySpace(len(pcm)) {
				return
			}
			a.setOutputLevel(rmsLevel(pcm))
			C.vpioWritePlay((*C.SInt16)(unsafe.Pointer(&pcm[0])), C.int(len(pcm)))
		case <-time.After(3 * audioFrameMs * time.Millisecond):
			// No inbound frames → the playout is draining; zero the level so
			// PlaybackIdle (the speaking watchdog's drain signal) turns true
			// instead of sticking at the last frame's amplitude.
			a.setOutputLevel(0)
		}
	}
}

func (a *AudioIO) waitForVPIOPlaySpace(samples int) bool {
	if samples <= 0 {
		return true
	}
	ticker := time.NewTicker(audioFrameMs * time.Millisecond / 2)
	defer ticker.Stop()
	for {
		capacity := int(C.vpioPlayCapacity())
		if capacity <= 0 || samples > capacity {
			return false
		}
		limit := capacity
		if limit > vpioPlaybackHighWaterSamples {
			limit = vpioPlaybackHighWaterSamples
		}
		if samples > limit {
			limit = samples
		}
		if int(C.vpioPlayCount()) <= limit-samples {
			return true
		}
		select {
		case <-a.vpioDone:
			return false
		case <-ticker.C:
		}
	}
}

func (a *AudioIO) vpioDebugStats() vpioDebugStats {
	return vpioDebugStats{
		InputCallbacks:  uint64(C.vpioInputCallbackCount()),
		OutputCallbacks: uint64(C.vpioOutputCallbackCount()),
		InputFrames:     uint64(C.vpioInputFrameCount()),
		OutputFrames:    uint64(C.vpioOutputFrameCount()),
		PlayUnderruns:   uint64(C.vpioPlayUnderrunCount()),
		PlayOverwrites:  uint64(C.vpioPlayOverwriteCount()),
		PlayQueueDrops:  a.playbackQueueDrops.Load(),
		PlayQueueMax:    a.playbackQueueMax.Load(),
		PlayBuffered:    int(C.vpioPlayCount()),
		PlayCapacity:    int(C.vpioPlayCapacity()),
		ForwardedFrames: a.vpioForwarded.Load(),
		GateDropped:     a.vpioGateDropped.Load(),
		BargePassed:     a.vpioBargePassed.Load(),
		MaxInputLevel:   math.Float64frombits(a.vpioMaxInput.Load()),
		MaxOutputLevel:  math.Float64frombits(a.vpioMaxOutput.Load()),
	}
}
