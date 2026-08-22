#include "vectors.h"
#include <stdio.h>
#include <string.h>
#include <stdlib.h>

static char text[64 * 1024];
static size_t text_n;

int vec_load(const char *path) {
  FILE *f = fopen(path, "rb");
  if (!f) return 0;
  text_n = fread(text, 1, sizeof text - 1, f);
  fclose(f);
  text[text_n] = 0;
  return text_n > 0;
}

static const char *find_value(const char *key) {
  char pat[128];
  snprintf(pat, sizeof pat, "\"%s\":", key);
  const char *p = strstr(text, pat);
  if (!p) return NULL;
  p += strlen(pat);
  while (*p == ' ') p++;
  return p;
}

static int hexval(char c) {
  if (c >= '0' && c <= '9') return c - '0';
  if (c >= 'a' && c <= 'f') return c - 'a' + 10;
  if (c >= 'A' && c <= 'F') return c - 'A' + 10;
  return -1;
}

size_t vec_hex(const char *key, uint8_t *out, size_t cap) {
  const char *p = find_value(key);
  if (!p || *p != '"') return 0;
  p++;
  size_t n = 0;
  while (p[0] && p[0] != '"' && p[1] && p[1] != '"') {
    int a = hexval(p[0]), b = hexval(p[1]);
    if (a < 0 || b < 0 || n >= cap) return 0;
    out[n++] = (uint8_t)(a << 4 | b);
    p += 2;
  }
  return n;
}

uint64_t vec_u64(const char *key) {
  const char *p = find_value(key);
  return p ? strtoull(p, NULL, 10) : 0;
}

size_t vec_str(const char *key, char *out, size_t cap) {
  const char *p = find_value(key);
  if (!p || *p != '"') return 0;
  p++;
  size_t n = 0;
  while (*p && *p != '"' && n + 1 < cap) out[n++] = *p++;
  out[n] = 0;
  return n;
}
