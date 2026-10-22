/* lolssd: подкладывается wayvnc через LD_PRELOAD (cmd/lolssd-wsl/desktop.go).
 *
 * wayvnc 0.9 (Debian trixie) захватывает экран и отдельно курсор через ext-image-copy-capture,
 * а при любом сбое кадра курсора, кроме несовпадения буфера, завершается целиком: «Fatal
 * error while capturing. Exiting...». С headless-выходами Hyprland это происходит сразу.
 * Здесь из реестра Wayland для wayvnc убираются глобалы ext-image-copy-capture: он берёт
 * wlr-screencopy, а отдельный захват курсора без них не включается (курсор рисует Hyprland
 * в самой картинке, wayvnc --render-cursor).
 *
 * Курсор Hyprland рисует в картинке, а изображение курсора wayvnc без отдельного захвата не
 * присылает. Тогда TigerVNC под Windows рисует поверх стрелку Windows — курсоров два. Поэтому
 * wayvnc всегда передаёт просмотрщику курсор из одного непрозрачного чёрного пикселя с остриём
 * в нём (полностью прозрачный Windows рисует чёрным квадратом): он совпадает с остриём курсора
 * Hyprland, и виден только курсор Linux. Приём — из hyprland-wsl (src/no-cursor-image.c).
 *
 * wl_registry_add_listener — inline-обёртка над wl_proxy_add_listener из libwayland-client,
 * её и перехватываем. Собирается без заголовков Wayland и neatvnc: нужные типы объявлены здесь,
 * API neatvnc — 0.9 (wayvnc 0.9 требует >=0.9, <0.10). */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <stdbool.h>
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

/* neatvnc 0.9 */
struct nvnc;
struct nvnc_fb;
#define FOURCC_ARGB8888 0x34325241u /* AR24 */

static void set_dot(struct nvnc *server) {
	static void (*real)(struct nvnc *, struct nvnc_fb *, uint16_t, uint16_t, uint16_t, uint16_t, bool);
	static struct nvnc_fb *dot;
	if (!real)
		real = (void (*)(struct nvnc *, struct nvnc_fb *, uint16_t, uint16_t, uint16_t, uint16_t, bool))dlsym(RTLD_NEXT, "nvnc_set_cursor");
	if (!dot) {
		struct nvnc_fb *(*fb_new)(uint16_t, uint16_t, uint32_t, uint16_t) =
		    (struct nvnc_fb * (*)(uint16_t, uint16_t, uint32_t, uint16_t)) dlsym(RTLD_DEFAULT, "nvnc_fb_new");
		void *(*fb_addr)(const struct nvnc_fb *) = (void *(*)(const struct nvnc_fb *))dlsym(RTLD_DEFAULT, "nvnc_fb_get_addr");
		if (fb_new && fb_addr && (dot = fb_new(1, 1, FOURCC_ARGB8888, 1)))
			*(uint32_t *)fb_addr(dot) = 0xff000000u;
	}
	if (real && dot && server)
		real(server, dot, 1, 1, 0, 0, true);
}

/* Любое изображение курсора от wayvnc (в том числе «нет курсора» при отключении клиента)
 * заменяется точкой. */
void nvnc_set_cursor(struct nvnc *server, struct nvnc_fb *fb, uint16_t width, uint16_t height, uint16_t hx, uint16_t hy, bool damaged) {
	(void)fb, (void)width, (void)height, (void)hx, (void)hy, (void)damaged;
	set_dot(server);
}

struct nvnc *nvnc_open(const char *address, uint16_t port) {
	static struct nvnc *(*real)(const char *, uint16_t);
	if (!real)
		real = (struct nvnc * (*)(const char *, uint16_t)) dlsym(RTLD_NEXT, "nvnc_open");
	struct nvnc *server = real(address, port);
	set_dot(server);
	return server;
}
