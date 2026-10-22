/* lolssd: подкладывается wayvnc через LD_PRELOAD (cmd/lolssd-wsl/desktop.go).
 *
 * wayvnc 0.9 (Debian trixie) захватывает экран и отдельно курсор через ext-image-copy-capture,
 * а при любом сбое кадра курсора, кроме несовпадения буфера, завершается целиком: «Fatal
 * error while capturing. Exiting...». С headless-выходами Hyprland это происходит сразу.
 * Здесь из реестра Wayland для wayvnc убираются глобалы ext-image-copy-capture: он берёт
 * wlr-screencopy, а отдельный захват курсора без них не включается (курсор рисует Hyprland
 * в самой картинке, wayvnc --render-cursor).
 *
 * wl_registry_add_listener — inline-обёртка над wl_proxy_add_listener из libwayland-client,
 * её и перехватываем. Собирается без заголовков Wayland: нужные типы объявлены здесь. */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <stdint.h>
#include <string.h>

struct wl_proxy;
struct wl_registry;

struct wl_registry_listener {
	void (*global)(void *data, struct wl_registry *registry, uint32_t name, const char *interface, uint32_t version);
	void (*global_remove)(void *data, struct wl_registry *registry, uint32_t name);
};

const char *wl_proxy_get_class(struct wl_proxy *proxy);

static const struct wl_registry_listener *orig;

static void global(void *data, struct wl_registry *registry, uint32_t name, const char *interface, uint32_t version) {
	if (strncmp(interface, "ext_image_copy_capture_", 23) == 0 || strcmp(interface, "ext_output_image_capture_source_manager_v1") == 0)
		return;
	orig->global(data, registry, name, interface, version);
}

static void global_remove(void *data, struct wl_registry *registry, uint32_t name) {
	orig->global_remove(data, registry, name);
}

static const struct wl_registry_listener filtered = {global, global_remove};

int wl_proxy_add_listener(struct wl_proxy *proxy, void (**implementation)(void), void *data) {
	static int (*real)(struct wl_proxy *, void (**)(void), void *);
	if (!real)
		real = (int (*)(struct wl_proxy *, void (**)(void), void *))dlsym(RTLD_NEXT, "wl_proxy_add_listener");
	if (strcmp(wl_proxy_get_class(proxy), "wl_registry") == 0) {
		orig = (const struct wl_registry_listener *)implementation;
		implementation = (void (**)(void))&filtered;
	}
	return real(proxy, implementation, data);
}
