/* lolssd: подкладывается программам рабочего стола через LD_PRELOAD (cmd/lolssd-wsl/boot.go).
 *
 * В WSL рисует Mesa d3d12 на одолженном устройстве vgem, и её GBM выдаёт только простые буферы:
 * запрос буфера для вывода на экран (GBM_BO_USE_SCANOUT), с явным списком модификаторов или
 * проверка формата для scanout получают отказ. Chromium и всё на Electron (Discord) спрашивают
 * именно так и молча завершаются (то же у hyprland-wsl: «Chromium needs --disable-gpu»).
 * Здесь каждый такой отказ повторяется без scanout, а затем как простой буфер для рисования —
 * программам этого достаточно: на экран их буферы выводит Hyprland, а не они сами.
 *
 * Собирается без заголовков gbm: нужные типы и флаги объявлены здесь (значения из gbm.h). */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <stddef.h>
#include <stdint.h>

struct gbm_device;
struct gbm_bo;

#define GBM_BO_USE_SCANOUT (1 << 0)
#define GBM_BO_USE_RENDERING (1 << 2)

typedef struct gbm_bo *(*create_fn)(struct gbm_device *, uint32_t, uint32_t, uint32_t, uint32_t);
typedef struct gbm_bo *(*create_mod_fn)(struct gbm_device *, uint32_t, uint32_t, uint32_t, const uint64_t *, unsigned int);
typedef struct gbm_bo *(*create_mod2_fn)(struct gbm_device *, uint32_t, uint32_t, uint32_t, const uint64_t *, unsigned int,
					 uint32_t);
typedef int (*supported_fn)(struct gbm_device *, uint32_t, uint32_t);

#define REAL(name, type) \
	static type real; \
	if (!real) \
		real = (type)dlsym(RTLD_NEXT, name)

struct gbm_bo *gbm_bo_create(struct gbm_device *dev, uint32_t w, uint32_t h, uint32_t format, uint32_t flags) {
	REAL("gbm_bo_create", create_fn);
	struct gbm_bo *bo = real(dev, w, h, format, flags);
	if (!bo && (flags & GBM_BO_USE_SCANOUT))
		bo = real(dev, w, h, format, flags & ~GBM_BO_USE_SCANOUT);
	if (!bo && flags != GBM_BO_USE_RENDERING)
		bo = real(dev, w, h, format, GBM_BO_USE_RENDERING);
	return bo;
}

struct gbm_bo *gbm_bo_create_with_modifiers(struct gbm_device *dev, uint32_t w, uint32_t h, uint32_t format,
					    const uint64_t *modifiers, unsigned int count) {
	REAL("gbm_bo_create_with_modifiers", create_mod_fn);
	struct gbm_bo *bo = real(dev, w, h, format, modifiers, count);
	return bo ? bo : gbm_bo_create(dev, w, h, format, GBM_BO_USE_RENDERING);
}

struct gbm_bo *gbm_bo_create_with_modifiers2(struct gbm_device *dev, uint32_t w, uint32_t h, uint32_t format,
					     const uint64_t *modifiers, unsigned int count, uint32_t flags) {
	REAL("gbm_bo_create_with_modifiers2", create_mod2_fn);
	struct gbm_bo *bo = real(dev, w, h, format, modifiers, count, flags);
	if (!bo && (flags & GBM_BO_USE_SCANOUT))
		bo = real(dev, w, h, format, modifiers, count, flags & ~GBM_BO_USE_SCANOUT);
	return bo ? bo : gbm_bo_create(dev, w, h, format, GBM_BO_USE_RENDERING);
}

int gbm_device_is_format_supported(struct gbm_device *dev, uint32_t format, uint32_t usage) {
	REAL("gbm_device_is_format_supported", supported_fn);
	if (real(dev, format, usage))
		return 1;
	/* scanout на деле не нужен (см. выше): формат годится, если годится для рисования */
	return (usage & GBM_BO_USE_SCANOUT) && real(dev, format, usage & ~GBM_BO_USE_SCANOUT);
}
