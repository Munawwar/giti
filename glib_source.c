#include <glib.h>
#include <stdint.h>

#include "_cgo_export.h"

typedef struct _AtkObject AtkObject;
typedef struct _GtkWidget GtkWidget;
AtkObject *gtk_widget_get_accessible(GtkWidget *widget);
void atk_object_set_name(AtkObject *accessible, const gchar *name);
void atk_object_set_description(AtkObject *accessible, const gchar *description);

static gboolean giti_source_func(gpointer data) {
	return gitiSourceFunc((uintptr_t)data);
}

static void giti_source_destroy(gpointer data) {
	gitiSourceDestroy((uintptr_t)data);
}

guint giti_add_source(guint milliseconds, uintptr_t handle) {
	if (milliseconds != 0) {
		return g_timeout_add_full(G_PRIORITY_DEFAULT, milliseconds, giti_source_func,
		                          (gpointer)handle, giti_source_destroy);
	}
	return g_idle_add_full(G_PRIORITY_DEFAULT_IDLE, giti_source_func, (gpointer)handle,
	                       giti_source_destroy);
}

void giti_set_accessibility(uintptr_t widget, const char *name, const char *description) {
	AtkObject *accessible = gtk_widget_get_accessible((GtkWidget *)widget);
	atk_object_set_name(accessible, name);
	atk_object_set_description(accessible, description);
}
