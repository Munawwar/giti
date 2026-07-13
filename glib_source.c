#include <glib.h>
#include <stdint.h>

#include "_cgo_export.h"

static gboolean giti_source_func(gpointer data) {
	return gitiSourceFunc((uintptr_t)data);
}

static void giti_source_destroy(gpointer data) {
	gitiSourceDestroy((uintptr_t)data);
}

guint giti_add_source(guint seconds, uintptr_t handle) {
	if (seconds != 0) {
		return g_timeout_add_seconds_full(G_PRIORITY_DEFAULT, seconds, giti_source_func,
		                                  (gpointer)handle, giti_source_destroy);
	}
	return g_idle_add_full(G_PRIORITY_DEFAULT_IDLE, giti_source_func, (gpointer)handle,
	                       giti_source_destroy);
}
