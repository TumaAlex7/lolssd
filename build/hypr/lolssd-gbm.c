/* lolssd: подкладывается программам рабочего стола через LD_PRELOAD (cmd/lolssd-wsl/desktop.go).
 *
 * В WSL рисует Mesa d3d12 на одолженном устройстве vgem, и обмениваться с Hyprland буферами
 * видеокарты (dmabuf) программы не могут: Hyprland их не принимает (hyprpaper: «unknown object,
 * message attach», затем падение), а GBM d3d12 не даёт буферов для вывода на экран — Chromium и
 * всё на Electron (Discord) молча завершаются (то же у hyprland-wsl: «Chromium needs
 * --disable-gpu»). Поэтому здесь, как в WSLg:
 *   - протокол zwp_linux_dmabuf_v1 из реестра Wayland программы не видит: Mesa рисует через d3d12 и
 *     отдаёт кадры через общую память (wl_shm), Chromium/Electron берут свой путь без dmabuf;
 *   - если GBM всё же просят о буфере для экрана (GBM_BO_USE_SCANOUT), с модификаторами или о
 *     проверке формата для экрана, отказ повторяется без scanout и затем как простой буфер.
 *
 * Собирается без заголовков gbm и Wayland: нужные типы и флаги объявлены здесь (значения из
 * gbm.h), функции libwayland-client ищутся через dlsym — библиотека грузится и в программы без
 * Wayland. */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <stddef.h>
#include <stdint.h>
#include <string.h>

struct gbm_device;
struct gbm_bo;

#define GBM_BO_USE_SCANOUT (1 << 0)
#define GBM_BO_USE_RENDERING (1 << 2)

typedef struct gbm_bo *(*create_fn)(struct gbm_device *, uint32_t, uint32_t, uint32_t, uint32_t);
typedef struct gbm_bo *(*create_mod_fn)(struct gbm_device *, uint32_t, uint32_t, uint32_t, const uint64_t *, unsigned int);
typedef struct gbm_bo *(*create_mod2_fn)(struct gbm_device *, uint32_t, uint32_t, uint32_t, const uint64_t *, unsigned int,
					 uint32_t);
typedef int (*supported_fn)(struct gbm_device *, uint32_t, uint32_t);

/* Настоящая функция: следующая после нашей в глобальном порядке, а если библиотеку загрузили через
 * dlopen локально (kitty так грузит libwayland-client), — прямо из неё. */
static void *find(const char *name, const char *lib) {
	void *f = dlsym(RTLD_NEXT, name);
	if (!f) {
		void *h = dlopen(lib, RTLD_LAZY | RTLD_NOLOAD);
		if (h)
			f = dlsym(h, name);
	}
	return f;
}

#define REAL(name, type, lib) \
	static type real; \
	if (!real) \
		real = (type)find(name, lib)

struct gbm_bo *gbm_bo_create(struct gbm_device *dev, uint32_t w, uint32_t h, uint32_t format, uint32_t flags) {
	REAL("gbm_bo_create", create_fn, "libgbm.so.1");
	if (!real)
		return NULL;
	struct gbm_bo *bo = real(dev, w, h, format, flags);
	if (!bo && (flags & GBM_BO_USE_SCANOUT))
		bo = real(dev, w, h, format, flags & ~GBM_BO_USE_SCANOUT);
	if (!bo && flags != GBM_BO_USE_RENDERING)
		bo = real(dev, w, h, format, GBM_BO_USE_RENDERING);
	return bo;
}

struct gbm_bo *gbm_bo_create_with_modifiers(struct gbm_device *dev, uint32_t w, uint32_t h, uint32_t format,
					    const uint64_t *modifiers, unsigned int count) {
	REAL("gbm_bo_create_with_modifiers", create_mod_fn, "libgbm.so.1");
	if (!real)
		return NULL;
	struct gbm_bo *bo = real(dev, w, h, format, modifiers, count);
	return bo ? bo : gbm_bo_create(dev, w, h, format, GBM_BO_USE_RENDERING);
}

struct gbm_bo *gbm_bo_create_with_modifiers2(struct gbm_device *dev, uint32_t w, uint32_t h, uint32_t format,
					     const uint64_t *modifiers, unsigned int count, uint32_t flags) {
	REAL("gbm_bo_create_with_modifiers2", create_mod2_fn, "libgbm.so.1");
	if (!real)
		return NULL;
	struct gbm_bo *bo = real(dev, w, h, format, modifiers, count, flags);
	if (!bo && (flags & GBM_BO_USE_SCANOUT))
		bo = real(dev, w, h, format, modifiers, count, flags & ~GBM_BO_USE_SCANOUT);
	return bo ? bo : gbm_bo_create(dev, w, h, format, GBM_BO_USE_RENDERING);
}

int gbm_device_is_format_supported(struct gbm_device *dev, uint32_t format, uint32_t usage) {
	REAL("gbm_device_is_format_supported", supported_fn, "libgbm.so.1");
	if (!real)
		return 0;
	if (real(dev, format, usage))
		return 1;
	/* scanout на деле не нужен (см. выше): формат годится, если годится для рисования */
	return (usage & GBM_BO_USE_SCANOUT) && real(dev, format, usage & ~GBM_BO_USE_SCANOUT);
}

/* --- реестр Wayland без zwp_linux_dmabuf_v1 ---
 * wl_registry_add_listener — inline-обёртка над wl_proxy_add_listener из libwayland-client. */
struct wl_proxy;
struct wl_registry;

struct wl_registry_listener {
	void (*global)(void *data, struct wl_registry *registry, uint32_t name, const char *interface, uint32_t version);
	void (*global_remove)(void *data, struct wl_registry *registry, uint32_t name);
};

typedef int (*add_listener_fn)(struct wl_proxy *, void (**)(void), void *);
typedef const char *(*get_class_fn)(struct wl_proxy *);

/* Настоящие обработчики — по реестру: данные программы (user data) не трогаются, Mesa и другие
 * читают их и через wl_proxy_get_user_data. Реестров у программы единицы. */
#define MAX_REGISTRIES 64
static struct {
	struct wl_registry *registry;
	const struct wl_registry_listener *orig;
} registries[MAX_REGISTRIES];

static const struct wl_registry_listener *orig_for(struct wl_registry *registry) {
	for (int i = 0; i < MAX_REGISTRIES; i++)
		if (registries[i].registry == registry)
			return registries[i].orig;
	return NULL;
}

static void global(void *data, struct wl_registry *registry, uint32_t name, const char *interface, uint32_t version) {
	const struct wl_registry_listener *orig = orig_for(registry);
	if (!orig || strcmp(interface, "zwp_linux_dmabuf_v1") == 0)
		return;
	if (orig->global)
		orig->global(data, registry, name, interface, version);
}

static void global_remove(void *data, struct wl_registry *registry, uint32_t name) {
	const struct wl_registry_listener *orig = orig_for(registry);
	if (orig && orig->global_remove)
		orig->global_remove(data, registry, name);
}

static const struct wl_registry_listener filtered = {global, global_remove};

int wl_proxy_add_listener(struct wl_proxy *proxy, void (**implementation)(void), void *data) {
	REAL("wl_proxy_add_listener", add_listener_fn, "libwayland-client.so.0");
	if (!real)
		return -1;
	static get_class_fn get_class;
	if (!get_class)
		get_class = (get_class_fn)find("wl_proxy_get_class", "libwayland-client.so.0");
	if (get_class && strcmp(get_class(proxy), "wl_registry") == 0) {
		for (int i = 0; i < MAX_REGISTRIES; i++) {
			if (!registries[i].registry) {
				registries[i].registry = (struct wl_registry *)proxy;
				registries[i].orig = (const struct wl_registry_listener *)implementation;
				return real(proxy, (void (**)(void))&filtered, data);
			}
		}
	}
	return real(proxy, implementation, data);
}
