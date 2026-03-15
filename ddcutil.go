package main

/*
#cgo pkg-config: ddcutil
#include <stdlib.h>
#include <ddcutil_c_api.h>
#include <ddcutil_types.h>

#ifdef DDCA_SYSLOG_WARNING
#define DDCFAST_HAVE_DDCA_INIT 1
#endif

#ifndef DDCA_SYSLOG_WARNING
#define DDCA_SYSLOG_WARNING 6
#endif

#ifndef DDCA_INIT_OPTIONS_DISABLE_CONFIG_FILE
#define DDCA_INIT_OPTIONS_DISABLE_CONFIG_FILE 1
#endif

static DDCA_Status ddcfast_init(const char* opts) {
#ifdef DDCFAST_HAVE_DDCA_INIT
	return ddca_init(
		opts,
		DDCA_SYSLOG_WARNING,
		DDCA_INIT_OPTIONS_DISABLE_CONFIG_FILE
	);
#else
	(void) opts;
	return 0;
#endif
}

#ifdef DDCA_DRM_CONNECTOR_FIELD_SIZE
typedef DDCA_Display_Info2 ddcfast_display_info_t;

static DDCA_Status ddcfast_get_display_info(DDCA_Display_Ref dref, ddcfast_display_info_t** info) {
	return ddca_get_display_info2(dref, info);
}

static void ddcfast_free_display_info(ddcfast_display_info_t* info) {
	ddca_free_display_info2(info);
}

static const char* ddcfast_connector(ddcfast_display_info_t* info) {
	return info->drm_card_connector;
}
#else
typedef DDCA_Display_Info ddcfast_display_info_t;

static DDCA_Status ddcfast_get_display_info(DDCA_Display_Ref dref, ddcfast_display_info_t** info) {
	return ddca_get_display_info(dref, info);
}

static void ddcfast_free_display_info(ddcfast_display_info_t* info) {
	ddca_free_display_info(info);
}

static const char* ddcfast_connector(ddcfast_display_info_t* info) {
	return "";
}
#endif

static int ddcfast_busno(ddcfast_display_info_t* info) {
	if (info->path.io_mode != DDCA_IO_I2C) {
		return -1;
	}
	return info->path.path.i2c_busno;
}

static int ddcfast_dispno(ddcfast_display_info_t* info) {
	return info->dispno;
}

static const char* ddcfast_mfg_id(ddcfast_display_info_t* info) {
	return info->mfg_id;
}

static const char* ddcfast_model_name(ddcfast_display_info_t* info) {
	return info->model_name;
}

static const char* ddcfast_serial(ddcfast_display_info_t* info) {
	return info->sn;
}
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const (
	vcpBrightness = 0x10
	vcpContrast   = 0x12
	vcpPowerMode  = 0xD6
)

type displayRef struct {
	Display
	dref uintptr
}

type Display struct {
	DisplayNo int    `json:"display_no"`
	BusNo     int    `json:"bus_no"`
	Connector string `json:"connector"`
	MfgID     string `json:"mfg_id"`
	Model     string `json:"model"`
	Serial    string `json:"serial"`
}

type FeatureValue struct {
	Current int `json:"current"`
	Max     int `json:"max"`
}

type persistedFeatureState struct {
	Value int `json:"value"`
	Max   int `json:"max,omitempty"`
}

type persistedDisplayState struct {
	Brightness *persistedFeatureState `json:"brightness,omitempty"`
	Contrast   *persistedFeatureState `json:"contrast,omitempty"`
}

type persistedState struct {
	Displays map[string]persistedDisplayState `json:"displays"`
}

type ddcRuntime struct {
	config    daemonConfig
	statePath string

	displays []displayRef
	handles  map[string]uintptr
	features map[string]FeatureValue
	state    persistedState
}

var (
	initOnce sync.Once
	initErr  error
	ddcMu    sync.Mutex
)

func initDDC() error {
	if err := requireI2CDevices(); err != nil {
		return err
	}

	initOnce.Do(func() {
		opts := C.CString("--disable-watch-displays --disable-usb")
		defer C.free(unsafe.Pointer(opts))

		rc := C.ddcfast_init(opts)
		if rc != 0 {
			initErr = statusError("initialize libddcutil", rc)
			return
		}

		C.ddca_enable_verify(C.bool(false))
	})
	return initErr
}

func prepareDDCThread() error {
	if err := initDDC(); err != nil {
		return err
	}
	C.ddca_set_fout(nil)
	C.ddca_set_ferr(nil)
	return nil
}

func requireI2CDevices() error {
	matches, err := filepath.Glob("/dev/i2c-*")
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return errors.New("no /dev/i2c-* devices found; load the i2c-dev module first")
	}

	for _, match := range matches {
		if _, err := os.Stat(match); err == nil {
			return nil
		}
	}

	return errors.New("no usable /dev/i2c-* devices found")
}

func statusError(op string, rc C.DDCA_Status) error {
	desc := C.GoString(C.ddca_rc_desc(rc))
	msg := fmt.Sprintf("%s: %s (%d)", op, desc, int(rc))

	if detail := C.ddca_get_error_detail(); detail != nil {
		defer C.ddca_free_error_detail(detail)
		if detail.detail != nil {
			msg = fmt.Sprintf("%s: %s", msg, C.GoString(detail.detail))
		}
	}

	return errors.New(msg)
}

func listDisplays() ([]displayRef, error) {
	if err := initDDC(); err != nil {
		return nil, err
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	C.ddca_set_fout(nil)
	C.ddca_set_ferr(nil)

	ddcMu.Lock()
	defer ddcMu.Unlock()

	return listDisplaysLocked()
}

func listDisplaysLocked() ([]displayRef, error) {
	var drefs *C.DDCA_Display_Ref
	rc := C.ddca_get_display_refs(C.bool(false), &drefs)
	if rc != 0 {
		return nil, statusError("enumerate displays", rc)
	}
	if drefs == nil {
		return nil, nil
	}

	ptrSize := unsafe.Sizeof(uintptr(0))
	var displays []displayRef

	for idx := 0; ; idx++ {
		slot := unsafe.Pointer(uintptr(unsafe.Pointer(drefs)) + uintptr(idx)*ptrSize)
		dref := *(*C.DDCA_Display_Ref)(slot)
		if dref == nil {
			break
		}

		var info *C.ddcfast_display_info_t
		rc = C.ddcfast_get_display_info(dref, &info)
		if rc != 0 {
			return nil, statusError("read display info", rc)
		}

		display := displayRef{
			Display: Display{
				DisplayNo: int(C.ddcfast_dispno(info)),
				BusNo:     int(C.ddcfast_busno(info)),
				Connector: strings.TrimSpace(C.GoString(C.ddcfast_connector(info))),
				MfgID:     strings.TrimSpace(C.GoString(C.ddcfast_mfg_id(info))),
				Model:     strings.TrimSpace(C.GoString(C.ddcfast_model_name(info))),
				Serial:    strings.TrimSpace(C.GoString(C.ddcfast_serial(info))),
			},
			dref: uintptr(unsafe.Pointer(dref)),
		}
		displays = append(displays, display)
		C.ddcfast_free_display_info(info)
	}

	return displays, nil
}

func redetectDisplays() error {
	if err := initDDC(); err != nil {
		return err
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	C.ddca_set_fout(nil)
	C.ddca_set_ferr(nil)

	ddcMu.Lock()
	defer ddcMu.Unlock()

	rc := C.ddca_redetect_displays()
	if rc != 0 {
		return statusError("redetect displays", rc)
	}
	return nil
}

func resolveDisplay(selector string) (displayRef, error) {
	displays, err := listDisplays()
	if err != nil {
		return displayRef{}, err
	}
	return selectDisplay(displays, selector)
}

func selectDisplay(displays []displayRef, selector string) (displayRef, error) {
	if len(displays) == 0 {
		return displayRef{}, errors.New("no DDC-capable displays found")
	}

	if selector == "" {
		if len(displays) == 1 {
			return displays[0], nil
		}
		return displayRef{}, fmt.Errorf("multiple displays found, pass --display: %s", joinDisplayIDs(displays))
	}

	if exact := filterDisplays(displays, selector, false); len(exact) == 1 {
		return exact[0], nil
	} else if len(exact) > 1 {
		return displayRef{}, fmt.Errorf("display selector %q is ambiguous: %s", selector, joinDisplayIDs(exact))
	}

	if fuzzy := filterDisplays(displays, selector, true); len(fuzzy) == 1 {
		return fuzzy[0], nil
	} else if len(fuzzy) > 1 {
		return displayRef{}, fmt.Errorf("display selector %q matches multiple displays: %s", selector, joinDisplayIDs(fuzzy))
	}

	return displayRef{}, fmt.Errorf("display %q not found; available: %s", selector, joinDisplayIDs(displays))
}

func filterDisplays(displays []displayRef, selector string, fuzzy bool) []displayRef {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil
	}

	var matched []displayRef
	for _, d := range displays {
		if displayMatches(d, selector, fuzzy) {
			matched = append(matched, d)
		}
	}
	return matched
}

func displayMatches(d displayRef, selector string, fuzzy bool) bool {
	sel := strings.ToLower(strings.TrimSpace(selector))
	shortConnector := shortConnectorName(d.Connector)

	values := []string{
		d.Connector,
		shortConnector,
		d.MfgID,
		d.Model,
		d.Serial,
		fmt.Sprintf("%d", d.DisplayNo),
		fmt.Sprintf("%d", d.BusNo),
		fmt.Sprintf("disp:%d", d.DisplayNo),
		fmt.Sprintf("bus:%d", d.BusNo),
		strings.TrimSpace(d.MfgID + " " + d.Model),
		strings.TrimSpace(d.MfgID + " " + d.Model + " " + d.Serial),
	}

	for _, raw := range values {
		v := strings.ToLower(strings.TrimSpace(raw))
		if v == "" {
			continue
		}
		if !fuzzy && v == sel {
			return true
		}
		if fuzzy && strings.Contains(v, sel) {
			return true
		}
	}

	if n, err := strconv.Atoi(sel); err == nil {
		if d.DisplayNo == n || d.BusNo == n {
			return true
		}
	}

	return false
}

func shortConnectorName(connector string) string {
	connector = strings.TrimSpace(connector)
	if connector == "" {
		return ""
	}
	if idx := strings.Index(connector, "-"); idx > 0 && strings.HasPrefix(connector, "card") {
		return connector[idx+1:]
	}
	return connector
}

func joinDisplayIDs(displays []displayRef) string {
	parts := make([]string, 0, len(displays))
	for _, d := range displays {
		label := d.Connector
		if label == "" {
			label = fmt.Sprintf("disp:%d", d.DisplayNo)
		}
		parts = append(parts, fmt.Sprintf("%s(bus:%d)", label, d.BusNo))
	}
	return strings.Join(parts, ", ")
}

func getFeature(display displayRef, code uint8) (FeatureValue, error) {
	if err := initDDC(); err != nil {
		return FeatureValue{}, err
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	C.ddca_set_fout(nil)
	C.ddca_set_ferr(nil)

	ddcMu.Lock()
	defer ddcMu.Unlock()

	return getFeatureLocked(display, code)
}

func getFeatureLocked(display displayRef, code uint8) (FeatureValue, error) {
	handle, err := openDisplayLocked(display)
	if err != nil {
		return FeatureValue{}, err
	}
	defer closeDisplayLocked(handle)

	var value C.DDCA_Non_Table_Vcp_Value
	rc := C.ddca_get_non_table_vcp_value(handle, C.DDCA_Vcp_Feature_Code(code), &value)
	if rc != 0 {
		return FeatureValue{}, statusError(fmt.Sprintf("get VCP 0x%02X", code), rc)
	}

	return FeatureValue{
		Current: int(value.sh)<<8 | int(value.sl),
		Max:     int(value.mh)<<8 | int(value.ml),
	}, nil
}

func setFeature(display displayRef, code uint8, value int) error {
	if err := initDDC(); err != nil {
		return err
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	C.ddca_set_fout(nil)
	C.ddca_set_ferr(nil)

	ddcMu.Lock()
	defer ddcMu.Unlock()

	handle, err := openDisplayLocked(display)
	if err != nil {
		return err
	}
	defer closeDisplayLocked(handle)

	rc := C.ddca_set_non_table_vcp_value(
		handle,
		C.DDCA_Vcp_Feature_Code(code),
		C.uint8_t((value>>8)&0xff),
		C.uint8_t(value&0xff),
	)
	if rc != 0 {
		return statusError(fmt.Sprintf("set VCP 0x%02X", code), rc)
	}
	return nil
}

func openDisplayLocked(display displayRef) (C.DDCA_Display_Handle, error) {
	var handle C.DDCA_Display_Handle
	rc := C.ddca_open_display2(C.DDCA_Display_Ref(unsafe.Pointer(display.dref)), C.bool(false), &handle)
	if rc != 0 {
		return nil, statusError(fmt.Sprintf("open display %s", displayLabel(display.Display)), rc)
	}
	return handle, nil
}

func closeDisplayLocked(handle C.DDCA_Display_Handle) {
	if handle == nil {
		return
	}
	_ = C.ddca_close_display(handle)
}

func displayLabel(display Display) string {
	if display.Connector != "" {
		return display.Connector
	}
	return fmt.Sprintf("disp:%d", display.DisplayNo)
}

func newDDCRuntime(config daemonConfig, statePath string) (*ddcRuntime, error) {
	if err := prepareDDCThread(); err != nil {
		return nil, err
	}

	rt := &ddcRuntime{
		config:    config,
		statePath: statePath,
		handles:   make(map[string]uintptr),
		features:  make(map[string]FeatureValue),
		state: persistedState{
			Displays: make(map[string]persistedDisplayState),
		},
	}

	if err := rt.loadPersistedState(); err != nil {
		warnDDCFast("load state: %v", err)
	}
	if rt.config.RestoreOnStart {
		if err := rt.restorePersistedState(); err != nil {
			warnDDCFast("%v", err)
		}
	}

	return rt, nil
}

func warnDDCFast(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "ddcfast: "+format+"\n", args...)
}

func (rt *ddcRuntime) listCachedDisplays() ([]Display, error) {
	if err := rt.ensureDisplaysLoaded(); err != nil {
		return nil, err
	}

	out := make([]Display, 0, len(rt.displays))
	for _, d := range rt.displays {
		out = append(out, d.Display)
	}
	return out, nil
}

func (rt *ddcRuntime) ensureDisplaysLoaded() error {
	if len(rt.displays) > 0 {
		return nil
	}

	displays, err := listDisplaysLocked()
	if err != nil {
		return err
	}
	rt.displays = displays
	return nil
}

func (rt *ddcRuntime) refreshDisplays() error {
	rt.closeAllHandles()
	rt.features = make(map[string]FeatureValue)
	rt.displays = nil

	rc := C.ddca_redetect_displays()
	if rc != 0 {
		return statusError("redetect displays", rc)
	}

	displays, err := listDisplaysLocked()
	if err != nil {
		return err
	}
	rt.displays = displays
	return nil
}

func (rt *ddcRuntime) resolveDisplay(selector string) (displayRef, error) {
	if err := rt.ensureDisplaysLoaded(); err != nil {
		return displayRef{}, err
	}

	display, err := selectDisplay(rt.displays, selector)
	if err == nil {
		return display, nil
	}

	if refreshErr := rt.refreshDisplays(); refreshErr == nil {
		return selectDisplay(rt.displays, selector)
	}
	return displayRef{}, err
}

func (rt *ddcRuntime) applyFeatureRequests(reqs []request, code uint8) (response, error) {
	if len(reqs) == 0 {
		return response{}, errors.New("no feature requests")
	}
	return rt.applyFeatureRequestsAttempt(reqs, code, true)
}

func (rt *ddcRuntime) applyFeatureRequestsAttempt(reqs []request, code uint8, retry bool) (response, error) {
	display, err := rt.resolveDisplay(reqs[0].Display)
	if err != nil {
		return response{}, err
	}

	current, err := rt.cachedOrReadFeature(display, code)
	if err != nil {
		if retry {
			if refreshErr := rt.refreshDisplays(); refreshErr == nil {
				return rt.applyFeatureRequestsAttempt(reqs, code, false)
			}
		}
		return response{}, err
	}
	if current.Max <= 0 {
		return response{}, fmt.Errorf("feature 0x%02X on %s has invalid max value %d", code, displayLabel(display.Display), current.Max)
	}

	targetValue := current
	for _, req := range reqs {
		target, err := computeScaledTarget(targetValue, req.Value, req.Scale)
		if err != nil {
			return response{}, err
		}
		targetValue.Current = target
	}

	if targetValue.Current != current.Current {
		if err := rt.writeFeature(display, code, targetValue.Current); err != nil {
			if retry {
				if refreshErr := rt.refreshDisplays(); refreshErr == nil {
					return rt.applyFeatureRequestsAttempt(reqs, code, false)
				}
			}
			return response{}, err
		}
	}

	rt.features[featureCacheKey(display.Display, code)] = targetValue
	rt.rememberPersistedFeature(display.Display, code, targetValue)

	name := "brightness"
	if code == vcpContrast {
		name = "contrast"
	}

	return response{
		OK:      true,
		Display: &display.Display,
		Current: current.Current,
		Max:     current.Max,
		Target:  targetValue.Current,
		Message: fmt.Sprintf("%s set to %d/%d on %s", name, targetValue.Current, current.Max, displayLabel(display.Display)),
	}, nil
}

func (rt *ddcRuntime) applyPower(req request) (response, error) {
	return rt.applyPowerAttempt(req, true)
}

func (rt *ddcRuntime) applyPowerAttempt(req request, retry bool) (response, error) {
	display, err := rt.resolveDisplay(req.Display)
	if err != nil {
		return response{}, err
	}

	var values []int
	switch req.Value {
	case "on":
		values = []int{0x01}
	case "off":
		values = []int{0x05, 0x04}
	default:
		return response{}, fmt.Errorf("invalid power mode %q", req.Value)
	}

	var lastErr error
	for _, value := range values {
		if err := rt.writeFeature(display, vcpPowerMode, value); err == nil {
			rt.closeHandle(display.Display)
			rt.clearFeatureCache(display.Display)
			return response{
				OK:      true,
				Display: &display.Display,
				Target:  value,
				Message: fmt.Sprintf("power %s on %s", req.Value, displayLabel(display.Display)),
			}, nil
		} else {
			lastErr = err
		}
	}

	if retry {
		if refreshErr := rt.refreshDisplays(); refreshErr == nil {
			return rt.applyPowerAttempt(req, false)
		}
	}

	if lastErr == nil {
		lastErr = errors.New("power operation failed")
	}
	return response{}, lastErr
}

func (rt *ddcRuntime) cachedOrReadFeature(display displayRef, code uint8) (FeatureValue, error) {
	key := featureCacheKey(display.Display, code)
	if value, ok := rt.features[key]; ok && value.Max > 0 {
		return value, nil
	}

	value, err := rt.readFeature(display, code)
	if err != nil {
		return FeatureValue{}, err
	}
	rt.features[key] = value
	return value, nil
}

func (rt *ddcRuntime) readFeature(display displayRef, code uint8) (FeatureValue, error) {
	for attempt := 0; attempt < 2; attempt++ {
		handle, err := rt.openHandle(display)
		if err != nil {
			return FeatureValue{}, err
		}

		var value C.DDCA_Non_Table_Vcp_Value
		rc := C.ddca_get_non_table_vcp_value(C.DDCA_Display_Handle(unsafe.Pointer(handle)), C.DDCA_Vcp_Feature_Code(code), &value)
		if rc == 0 {
			return FeatureValue{
				Current: int(value.sh)<<8 | int(value.sl),
				Max:     int(value.mh)<<8 | int(value.ml),
			}, nil
		}

		rt.closeHandle(display.Display)
		if attempt == 1 {
			return FeatureValue{}, statusError(fmt.Sprintf("get VCP 0x%02X", code), rc)
		}
	}

	return FeatureValue{}, errors.New("read feature failed")
}

func (rt *ddcRuntime) writeFeature(display displayRef, code uint8, value int) error {
	for attempt := 0; attempt < 2; attempt++ {
		handle, err := rt.openHandle(display)
		if err != nil {
			return err
		}

		rc := C.ddca_set_non_table_vcp_value(
			C.DDCA_Display_Handle(unsafe.Pointer(handle)),
			C.DDCA_Vcp_Feature_Code(code),
			C.uint8_t((value>>8)&0xff),
			C.uint8_t(value&0xff),
		)
		if rc == 0 {
			return nil
		}

		rt.closeHandle(display.Display)
		if attempt == 1 {
			return statusError(fmt.Sprintf("set VCP 0x%02X", code), rc)
		}
	}

	return errors.New("write feature failed")
}

func (rt *ddcRuntime) openHandle(display displayRef) (uintptr, error) {
	key := canonicalDisplayKey(display.Display)
	if handle, ok := rt.handles[key]; ok && handle != 0 {
		return handle, nil
	}

	handle, err := openDisplayLocked(display)
	if err != nil {
		return 0, err
	}
	raw := uintptr(unsafe.Pointer(handle))
	rt.handles[key] = raw
	return raw, nil
}

func (rt *ddcRuntime) closeHandle(display Display) {
	key := canonicalDisplayKey(display)
	handle, ok := rt.handles[key]
	if !ok {
		return
	}
	closeDisplayLocked(C.DDCA_Display_Handle(unsafe.Pointer(handle)))
	delete(rt.handles, key)
}

func (rt *ddcRuntime) closeAllHandles() {
	for key, handle := range rt.handles {
		closeDisplayLocked(C.DDCA_Display_Handle(unsafe.Pointer(handle)))
		delete(rt.handles, key)
	}
}

func (rt *ddcRuntime) clearFeatureCache(display Display) {
	prefix := canonicalDisplayKey(display) + "#"
	for key := range rt.features {
		if strings.HasPrefix(key, prefix) {
			delete(rt.features, key)
		}
	}
}

func featureCacheKey(display Display, code uint8) string {
	return fmt.Sprintf("%s#%02x", canonicalDisplayKey(display), code)
}

func canonicalDisplayKey(display Display) string {
	keys := displayStateKeys(display)
	if len(keys) == 0 {
		return fmt.Sprintf("disp:%d", display.DisplayNo)
	}
	return keys[0]
}

func displayStateKeys(display Display) []string {
	var keys []string

	if normalized := normalizeDisplayKeyPart(display.Serial); normalized != "" {
		keys = appendUnique(keys, "serial:"+strings.Join([]string{
			normalizeDisplayKeyPart(display.MfgID),
			normalizeDisplayKeyPart(display.Model),
			normalized,
		}, ":"))
	}

	if short := strings.ToLower(strings.TrimSpace(shortConnectorName(display.Connector))); short != "" {
		keys = appendUnique(keys, "connector:"+short)
	}
	if connector := strings.ToLower(strings.TrimSpace(display.Connector)); connector != "" {
		keys = appendUnique(keys, "connector:"+connector)
	}
	if display.BusNo > 0 {
		keys = appendUnique(keys, fmt.Sprintf("bus:%d", display.BusNo))
	}
	keys = appendUnique(keys, fmt.Sprintf("disp:%d", display.DisplayNo))
	return keys
}

func normalizeDisplayKeyPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "-")
	return value
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func (rt *ddcRuntime) rememberPersistedFeature(display Display, code uint8, value FeatureValue) {
	if code != vcpBrightness && code != vcpContrast {
		return
	}
	if rt.state.Displays == nil {
		rt.state.Displays = make(map[string]persistedDisplayState)
	}

	key := canonicalDisplayKey(display)
	entry := rt.state.Displays[key]
	saved := &persistedFeatureState{
		Value: value.Current,
		Max:   value.Max,
	}

	if code == vcpBrightness {
		entry.Brightness = saved
	} else {
		entry.Contrast = saved
	}

	rt.state.Displays[key] = entry
	if err := rt.savePersistedState(); err != nil {
		warnDDCFast("save state: %v", err)
	}
}

func (rt *ddcRuntime) loadPersistedState() error {
	data, err := os.ReadFile(rt.statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}

	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.Displays == nil {
		state.Displays = make(map[string]persistedDisplayState)
	}
	rt.state = state
	return nil
}

func (rt *ddcRuntime) savePersistedState() error {
	if rt.statePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(rt.statePath), 0o700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(rt.state, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := rt.statePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, rt.statePath)
}

func (rt *ddcRuntime) findPersistedState(display Display) (persistedDisplayState, bool) {
	for _, key := range displayStateKeys(display) {
		if state, ok := rt.state.Displays[key]; ok {
			return state, true
		}
	}
	return persistedDisplayState{}, false
}

func (rt *ddcRuntime) restorePersistedState() error {
	if len(rt.state.Displays) == 0 {
		return nil
	}

	retries := rt.config.RestoreRetryCount
	if retries < 1 {
		retries = 1
	}
	delay := time.Duration(rt.config.RestoreRetryDelayMs) * time.Millisecond

	var lastErr error
	for attempt := 0; attempt < retries; attempt++ {
		if err := rt.refreshDisplays(); err != nil {
			lastErr = err
		} else if len(rt.displays) == 0 {
			lastErr = errors.New("no DDC-capable displays found during restore")
		} else {
			matched := false
			var restoreErr error

			for _, display := range rt.displays {
				saved, ok := rt.findPersistedState(display.Display)
				if !ok {
					continue
				}
				matched = true

				if saved.Brightness != nil {
					if err := rt.writeFeature(display, vcpBrightness, saved.Brightness.Value); err != nil {
						restoreErr = err
					} else if saved.Brightness.Max > 0 {
						rt.features[featureCacheKey(display.Display, vcpBrightness)] = FeatureValue{
							Current: saved.Brightness.Value,
							Max:     saved.Brightness.Max,
						}
					}
				}
				if saved.Contrast != nil {
					if err := rt.writeFeature(display, vcpContrast, saved.Contrast.Value); err != nil {
						restoreErr = err
					} else if saved.Contrast.Max > 0 {
						rt.features[featureCacheKey(display.Display, vcpContrast)] = FeatureValue{
							Current: saved.Contrast.Value,
							Max:     saved.Contrast.Max,
						}
					}
				}
			}

			if restoreErr == nil {
				if !matched {
					return nil
				}
				return nil
			}
			lastErr = restoreErr
		}

		if attempt+1 < retries && delay > 0 {
			time.Sleep(delay)
		}
	}

	if lastErr != nil {
		return fmt.Errorf("restore state: %w", lastErr)
	}
	return nil
}
