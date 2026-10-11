package main

/*
#include <stdint.h>
#include <stdlib.h>
typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct { uint32_t abi_version; void* host_ctx; cliproxy_host_call_fn call; cliproxy_host_free_fn free_buffer; } cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct { uint32_t abi_version; cliproxy_plugin_call_fn call; cliproxy_plugin_free_fn free_buffer; cliproxy_plugin_shutdown_fn shutdown; } cliproxy_plugin_api;
extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
static const cliproxy_host_api* stored_host;
static void store_host(const cliproxy_host_api* host) { stored_host = host; }
static int call_host(const char* method, const uint8_t* request, size_t len, cliproxy_buffer* response) {
    if (!stored_host || !stored_host->call) return 1;
    return stored_host->call(stored_host->host_ctx, method, request, len, response);
}
static void free_host(void* ptr, size_t len) {
    if (ptr && stored_host && stored_host->free_buffer) stored_host->free_buffer(ptr, len);
}
*/
import "C"

import (
	"encoding/json"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

var nativeRuntime struct {
	sync.Mutex
	provider *provider
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, api *C.cliproxy_plugin_api) C.int {
	if host == nil || api == nil || uint32(host.abi_version) != pluginabi.ABIVersion {
		return 1
	}
	C.store_host(host)
	api.abi_version = C.uint32_t(pluginabi.ABIVersion)
	api.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	api.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	api.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, length C.size_t, out *C.cliproxy_buffer) (rc C.int) {
	if out == nil {
		return 1
	}
	out.ptr, out.len = nil, 0
	defer func() {
		if recover() != nil {
			writeNativeResponse(out, failureEnvelope(&pluginabi.Error{Code: "internal_error", Message: "Muse plugin internal error", HTTPStatus: 500, NoRetry: true}))
			rc = 1
		}
	}()
	if method == nil || uint64(length) > 128<<20 {
		writeNativeResponse(out, failureEnvelope(requestError("invalid_request", "Invalid native request")))
		return 1
	}
	var raw []byte
	if request != nil && length > 0 {
		raw = C.GoBytes(unsafe.Pointer(request), C.int(length))
	}
	result, errHandle := dispatch(C.GoString(method), raw)
	if errHandle != nil {
		writeNativeResponse(out, failureEnvelope(asPluginError(errHandle)))
		return 1
	}
	payload, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		writeNativeResponse(out, failureEnvelope(asPluginError(errMarshal)))
		return 1
	}
	rawEnvelope, errMarshal := json.Marshal(pluginabi.Envelope{OK: true, Result: payload})
	if errMarshal != nil {
		return 1
	}
	writeNativeResponse(out, rawEnvelope)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	nativeRuntime.Lock()
	p := nativeRuntime.provider
	nativeRuntime.provider = nil
	nativeRuntime.Unlock()
	if p != nil {
		p.close()
	}
}

func writeNativeResponse(out *C.cliproxy_buffer, raw []byte) {
	if len(raw) > 0 {
		out.ptr = C.CBytes(raw)
		out.len = C.size_t(len(raw))
	}
}

func failureEnvelope(err *pluginabi.Error) []byte {
	raw, _ := json.Marshal(pluginabi.Envelope{Error: err})
	return raw
}

func nativeHostCall(method string, payload []byte) ([]byte, error) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var req *C.uint8_t
	if len(payload) > 0 {
		req = (*C.uint8_t)(C.CBytes(payload))
		defer C.free(unsafe.Pointer(req))
	}
	var out C.cliproxy_buffer
	rc := C.call_host(cMethod, req, C.size_t(len(payload)), &out)
	if out.ptr != nil {
		defer C.free_host(out.ptr, out.len)
	}
	var raw []byte
	if out.ptr != nil && out.len > 0 {
		if uint64(out.len) > 128<<20 {
			return nil, requestError("host_response_too_large", "Host callback response exceeded size limit")
		}
		raw = C.GoBytes(out.ptr, C.int(out.len))
	}
	if rc != 0 && len(raw) == 0 {
		return nil, &pluginabi.Error{Code: "host_callback_failed", Message: "Host callback failed", HTTPStatus: 502, NoRetry: true}
	}
	return raw, nil
}
