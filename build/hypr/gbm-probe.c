/* lolssd: диагностика, почему GBM не создаётся на устройстве сеанса (cmd/lolssd-wsl/desktop.go).
 * Собирается и запускается в контейнере с тем же окружением, что Hyprland, если тот не
 * запустился. libgbm и библиотеки Mesa открываются через dlopen, чтобы видеть dlerror. */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <glob.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <unistd.h>

struct drm_version {
	int major, minor, patchlevel;
	size_t name_len;
	char *name;
	size_t date_len;
	char *date;
	size_t desc_len;
	char *desc;
};
#define DRM_IOCTL_VERSION _IOWR('d', 0x00, struct drm_version)

static void try_dlopen(const char *pattern) {
	glob_t g;
	if (glob(pattern, 0, NULL, &g) != 0) {
		printf("  %s: нет файлов\n", pattern);
		return;
	}
	for (size_t i = 0; i < g.gl_pathc; i++) {
		void *h = dlopen(g.gl_pathv[i], RTLD_NOW | RTLD_LOCAL);
		printf("  dlopen %s: %s\n", g.gl_pathv[i], h ? "ok" : dlerror());
	}
	globfree(&g);
}

int main(int argc, char **argv) {
	const char *dev = argc > 1 ? argv[1] : "/dev/dri/card0";
	printf("gbm-probe %s, GALLIUM_DRIVER=%s, LD_LIBRARY_PATH=%s\n", dev,
	       getenv("GALLIUM_DRIVER") ? getenv("GALLIUM_DRIVER") : "", getenv("LD_LIBRARY_PATH") ? getenv("LD_LIBRARY_PATH") : "");
	int fd = open(dev, O_RDWR | O_CLOEXEC);
	if (fd < 0) {
		printf("  open: %s\n", strerror(errno));
		return 1;
	}
	char name[64] = "";
	struct drm_version v = {.name_len = sizeof(name) - 1, .name = name};
	printf("  DRM_IOCTL_VERSION: %s, драйвер «%s»\n", ioctl(fd, DRM_IOCTL_VERSION, &v) ? strerror(errno) : "ok", name);

	char link[256] = "", path[128];
	snprintf(path, sizeof(path), "/sys/class/drm/%s/device/subsystem", strrchr(dev, '/') + 1);
	ssize_t n = readlink(path, link, sizeof(link) - 1);
	printf("  %s → %s\n", path, n > 0 ? link : strerror(errno));

	try_dlopen("/usr/lib*/libgallium-*.so");
	try_dlopen("/usr/lib/*/libgallium-*.so");
	try_dlopen("/usr/lib*/gbm/*_gbm.so");
	try_dlopen("/usr/lib/*/gbm/*_gbm.so");

	void *gbm = dlopen("libgbm.so.1", RTLD_NOW);
	if (!gbm) {
		printf("  dlopen libgbm.so.1: %s\n", dlerror());
		return 1;
	}
	void *(*create)(int) = (void *(*)(int))dlsym(gbm, "gbm_create_device");
	const char *(*backend)(void *) = (const char *(*)(void *))dlsym(gbm, "gbm_device_get_backend_name");
	void *(*bo_create)(void *, unsigned, unsigned, unsigned, unsigned) =
	    (void *(*)(void *, unsigned, unsigned, unsigned, unsigned))dlsym(gbm, "gbm_bo_create");
	errno = 0;
	void *d = create(fd);
	printf("  gbm_create_device: %s (%s)\n", d ? "ok" : "ошибка", d ? backend(d) : strerror(errno));
	if (!d)
		return 1;
	/* GBM_FORMAT_XRGB8888, GBM_BO_USE_RENDERING */
	void *bo = bo_create(d, 640, 480, 0x34325258, 1 << 2);
	printf("  gbm_bo_create 640x480 XRGB8888 rendering: %s\n", bo ? "ok" : strerror(errno));
	return bo ? 0 : 1;
}
